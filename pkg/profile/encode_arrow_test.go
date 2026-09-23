// Copyright 2026 The Parca Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package profile

import (
	"encoding/binary"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/stretchr/testify/require"
)

// A location line whose function name is NULL must encode, and a valid one must
// keep its name.
//
// EncodeArrowLocation's writer used to emit the hasFunction flag as 0x1 and the
// whole function block unconditionally, while serializedArrowLocationSize
// budgeted the block only under lineFunctionName.IsValid(i). A line with a null
// function name -- a line with a number but no function, exactly a pprof Line
// with FunctionId 0 -- was therefore written past the end of a buffer sized for
// the flag byte alone. That is a panic (index out of range) on the ingest path,
// which has no recovery interceptor, reachable from a WriteArrow record whose
// function_name column, which the schema permits to be nullable, carries a null.
//
// The buffer is parsed back with parseArrowLocation (a self-contained reader of
// the exact wire format, so the test does not depend on DecodeInto, which is a
// partial writer that leaves an Arrow record unbalanced). A clean parse that
// consumes the whole buffer is also the proof that writer and sizer agree: the
// encoder returns the entire sized buffer, so a size disagreement leaves either
// an overrun (panic in the writer) or trailing bytes (the parse would stop
// short of len).
func TestEncodeArrowLocationNullFunctionRoundTrips(t *testing.T) {
	mem := memory.DefaultAllocator

	// Two lines: [0] has a function, [1] has a null function.
	lineNumber := int64Array(mem, 1, 5)
	startLine := int64Array(mem, 2, 0)
	column := uint64Array(mem, 0, 0)
	name, nameDict := dictWith(mem, sp("main.run"), nil)
	sys, sysDict := dictWith(mem, sp("main.run"), nil)
	file, fileDict, fileVals := reeDictWith(mem, sp("main.go"), nil)

	buf := EncodeArrowLocation(
		0xdeadbeef, false, 0, 0, 0, nil, nil,
		0, 2, nil, nil,
		lineNumber, column,
		name, nameDict, sys, sysDict,
		file, fileDict, fileVals, startLine,
	)

	lines := parseArrowLocation(t, buf)
	require.Len(t, lines, 2)
	require.True(t, lines[0].hasFunction, "the named line lost its function")
	require.Equal(t, "main.run", lines[0].name)
	require.False(t, lines[1].hasFunction, "the null-function line must encode as absent, not an empty or wrong name")
}

// The distinctions the fix hinges on, each its own line, encoded together so a
// mixed record also proves the per-line flags stay aligned:
//   - a null function name encodes as absent (hasFunction 0x0);
//   - an empty-but-present function name encodes as PRESENT with an empty name,
//     not absent (the old code, had the buffer been large enough, would have
//     read dict.Value(0) for a null slot -- a wrong name -- so keeping empty and
//     null distinct matters);
//   - a valid function whose filename is null still encodes -- the inner
//     filename-null branch the round-trip test above never reached.
func TestEncodeArrowLocationFunctionShapes(t *testing.T) {
	mem := memory.DefaultAllocator

	lineNumber := int64Array(mem, 1, 2, 3, 4)
	startLine := int64Array(mem, 10, 20, 30, 0)
	column := uint64Array(mem, 0, 0, 0, 0)
	//                    named          empty   null-filename  null-func
	name, nameDict := dictWith(mem, sp("main.run"), sp(""), sp("f"), nil)
	sys, sysDict := dictWith(mem, sp("main.run"), sp(""), sp("f"), nil)
	file, fileDict, fileVals := reeDictWith(mem, sp("main.go"), sp(""), nil, nil)

	buf := EncodeArrowLocation(
		0xabc, false, 0, 0, 0, nil, nil,
		0, 4, nil, nil,
		lineNumber, column,
		name, nameDict, sys, sysDict,
		file, fileDict, fileVals, startLine,
	)

	lines := parseArrowLocation(t, buf)
	require.Len(t, lines, 4)

	// [0] named.
	require.True(t, lines[0].hasFunction)
	require.Equal(t, "main.run", lines[0].name)
	require.Equal(t, "main.go", lines[0].filename)

	// [1] empty-but-present: PRESENT with an empty name -- not absent.
	require.True(t, lines[1].hasFunction, "an empty function name must stay present, not become absent")
	require.Equal(t, "", lines[1].name)

	// [2] valid function, null filename: the function is kept, the filename is
	// an empty string, and nothing overran.
	require.True(t, lines[2].hasFunction)
	require.Equal(t, "f", lines[2].name)
	require.Equal(t, "", lines[2].filename)

	// [3] null function: absent.
	require.False(t, lines[3].hasFunction, "a null function name must encode as absent")
}

type parsedLine struct {
	lineNumber        int64
	hasFunction       bool
	name, systemName  string
	filename          string
	functionStartLine int64
}

// parseArrowLocation decodes the byte format EncodeArrowLocation produces, the
// same format profile.DecodeInto and the duckdb/clickhouse decoders read, and
// fails if the buffer does not parse cleanly and completely -- which catches a
// writer that over- or under-wrote relative to the sizer. It is self-contained
// on purpose: DecodeInto is a partial writer (it never appends the location
// wrappers), so building an Arrow record from it is unbalanced and unsafe to
// navigate.
func parseArrowLocation(t *testing.T, data []byte) []parsedLine {
	t.Helper()
	off := 0
	uvarint := func() uint64 {
		v, n := binary.Uvarint(data[off:])
		require.Positive(t, n, "truncated uvarint at offset %d", off)
		off += n
		return v
	}
	str := func() string {
		l := int(uvarint())
		require.LessOrEqual(t, off+l, len(data), "string length %d overruns the buffer at offset %d", l, off)
		s := string(data[off : off+l])
		off += l
		return s
	}
	flag := func() bool {
		require.Less(t, off, len(data), "flag byte past the buffer")
		b := data[off] == 0x1
		off++
		return b
	}

	_ = uvarint() // address
	numLines := int(uvarint())
	if flag() { // hasMapping
		_, _ = str(), str() // buildID, filename
		uvarint()           // memoryStart
		uvarint()           // memoryLength
		uvarint()           // mappingOffset
	}

	out := make([]parsedLine, 0, numLines)
	for i := 0; i < numLines; i++ {
		var l parsedLine
		l.lineNumber = int64(uvarint())
		uvarint() // column
		l.hasFunction = flag()
		if l.hasFunction {
			l.functionStartLine = int64(uvarint())
			l.name = str()
			l.systemName = str()
			l.filename = str()
		}
		out = append(out, l)
	}

	require.Equal(t, len(data), off,
		"the encoder wrote %d bytes into a buffer sized for %d: writer and sizer disagree", off, len(data))
	return out
}

func int64Array(mem memory.Allocator, vs ...int64) *array.Int64 {
	b := array.NewInt64Builder(mem)
	defer b.Release()
	b.AppendValues(vs, nil)
	return b.NewInt64Array()
}

func uint64Array(mem memory.Allocator, vs ...uint64) *array.Uint64 {
	b := array.NewUint64Builder(mem)
	defer b.Release()
	b.AppendValues(vs, nil)
	return b.NewUint64Array()
}

// dictWith builds a dictionary(binary) array; a nil entry appends null.
func dictWith(mem memory.Allocator, vs ...*string) (*array.Dictionary, *array.Binary) {
	b := array.NewDictionaryBuilder(mem, &arrow.DictionaryType{
		IndexType: arrow.PrimitiveTypes.Uint32, ValueType: arrow.BinaryTypes.Binary,
	}).(*array.BinaryDictionaryBuilder)
	defer b.Release()
	for _, v := range vs {
		if v == nil {
			b.AppendNull()
			continue
		}
		if err := b.AppendString(*v); err != nil {
			panic(err)
		}
	}
	d := b.NewDictionaryArray()
	return d, d.Dictionary().(*array.Binary)
}

// reeDictWith builds the RunEndEncoded(dictionary(binary)) shape the filename
// column uses.
func reeDictWith(mem memory.Allocator, vs ...*string) (*array.RunEndEncoded, *array.Dictionary, *array.Binary) {
	b := array.NewBuilder(mem, arrow.RunEndEncodedOf(arrow.PrimitiveTypes.Int32,
		&arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Uint32, ValueType: arrow.BinaryTypes.Binary})).(*array.RunEndEncodedBuilder)
	defer b.Release()
	vb := b.ValueBuilder().(*array.BinaryDictionaryBuilder)
	for _, v := range vs {
		b.Append(1)
		if v == nil {
			vb.AppendNull()
			continue
		}
		if err := vb.AppendString(*v); err != nil {
			panic(err)
		}
	}
	ree := b.NewRunEndEncodedArray()
	dict := ree.Values().(*array.Dictionary)
	return ree, dict, dict.Dictionary().(*array.Binary)
}

func sp(s string) *string { return &s }
