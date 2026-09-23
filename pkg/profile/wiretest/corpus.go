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

// Package wiretest holds a shared corpus of encoded location records and the
// values they must decode to.
//
// The location wire format has three encoders (profile.EncodePprofLocation,
// profile.EncodeArrowLocation, normalizer.encodeV2Location) and three decoders
// (profile.DecodeInto and the duckdb / clickhouse decodeLineInfo). They live in
// separate packages and were kept in agreement by discipline alone -- which is
// how the same class of bug shipped repeatedly: a column the encoder wrote and
// the decoders skipped (guettli/parca#103), a function block the writer emitted
// past what the sizer budgeted (#108), and decoders that panicked on short or
// oversized input (#107/#111).
//
// This corpus is the executable cross-check. It builds records with the
// canonical encoder (EncodePprofLocation) and states what each must decode to,
// so a decoder's own package can assert the same expectations against the same
// bytes. A decoder that drifts from the format -- or from the other decoders --
// then fails a test rather than a production ingest.
//
// It is consumed today by the two ingest-path decoders that turn a stored
// location back into fields, duckdb and clickhouse decodeLineInfo, whose tests
// live beside them. profile.DecodeInto is the third decoder; it is not wired up
// here yet because it is a partial Arrow writer (it repopulates a record's field
// builders rather than returning plain values) and, unlike the two ingest
// decoders, has no bounds checks -- feeding it the Adversarial cases would need
// its own guarding first. The corpus is written to serve it when that happens.
//
// It is a normal importable package rather than test-only code because the
// decoders it guards are unexported and in different packages; only _test files
// import it, so it is not compiled into the server.
package wiretest

import (
	"math"

	pprofpb "github.com/parca-dev/parca/gen/proto/go/google/pprof"
	"github.com/parca-dev/parca/pkg/profile"
)

// Line is the expected decode of one line of a location.
type Line struct {
	Number int64
	// HasFunction is false for a line with a number but no function -- a pprof
	// Line whose FunctionId is 0. A decoder that keeps only strings collapses
	// this with an empty-but-present name; DecodeInto keeps them distinct
	// (null vs empty), so a decoder that reconstructs an Arrow record should
	// check HasFunction, and one that yields plain strings should find Name
	// empty when HasFunction is false.
	HasFunction                bool
	Name, SystemName, Filename string
	StartLine                  int64
}

// Case is one encoded location and the values it must decode to.
type Case struct {
	Name    string
	Encoded []byte
	Lines   []Line

	// The mapping portion. The duckdb/clickhouse decodeLineInfo skip the
	// mapping but must parse it to reach the lines, so a corrupt mapping-skip
	// misaligns every line -- the cases with a mapping exercise that path.
	HasMapping                             bool
	BuildID, File                          string
	MemoryStart, MemoryLimit, MemoryOffset uint64
}

type caseSpec struct {
	name    string
	loc     *pprofpb.Location
	mapping *pprofpb.Mapping
	funcs   []*pprofpb.Function
	strs    []string
	want    []Line
}

// Corpus returns the shared cases. Every case is encoded here with the
// canonical encoder so the callers only have to decode and compare.
func Corpus() []Case {
	specs := []caseSpec{
		{
			name: "one line with a function",
			loc:  &pprofpb.Location{Address: 0x1000, Line: []*pprofpb.Line{{Line: 42, FunctionId: 1}}},
			funcs: []*pprofpb.Function{
				{Id: 1, Name: 1, SystemName: 2, Filename: 3, StartLine: 10},
			},
			strs: []string{"", "main.run", "main.run.sys", "main.go"},
			want: []Line{{Number: 42, HasFunction: true, Name: "main.run", SystemName: "main.run.sys", Filename: "main.go", StartLine: 10}},
		},
		{
			name: "one line, no function (FunctionId 0)",
			loc:  &pprofpb.Location{Address: 0x2000, Line: []*pprofpb.Line{{Line: 7}}},
			strs: []string{""},
			want: []Line{{Number: 7, HasFunction: false}},
		},
		{
			name: "function present but empty name",
			loc:  &pprofpb.Location{Address: 0x3000, Line: []*pprofpb.Line{{Line: 1, FunctionId: 1}}},
			funcs: []*pprofpb.Function{
				{Id: 1, Name: 0, SystemName: 0, Filename: 0, StartLine: 0},
			},
			strs: []string{""},
			want: []Line{{Number: 1, HasFunction: true, Name: "", SystemName: "", Filename: ""}},
		},
		{
			name: "with a mapping, non-zero fields",
			loc:  &pprofpb.Location{Address: 0x4000, MappingId: 1, Line: []*pprofpb.Line{{Line: 99, FunctionId: 1}}},
			mapping: &pprofpb.Mapping{
				Id: 1, BuildId: 4, Filename: 5,
				MemoryStart: 0x1000, MemoryLimit: 0x9000, FileOffset: 0x40,
			},
			funcs: []*pprofpb.Function{{Id: 1, Name: 1, SystemName: 2, Filename: 3, StartLine: 5}},
			strs:  []string{"", "svc.handle", "svc.handle", "svc.go", "the-build-id", "/bin/svc"},
			want:  []Line{{Number: 99, HasFunction: true, Name: "svc.handle", SystemName: "svc.handle", Filename: "svc.go", StartLine: 5}},
		},
		{
			name:  "no mapping",
			loc:   &pprofpb.Location{Address: 0x5000, Line: []*pprofpb.Line{{Line: 3, FunctionId: 1}}},
			funcs: []*pprofpb.Function{{Id: 1, Name: 1, SystemName: 1, Filename: 2}},
			strs:  []string{"", "f", "f.go"},
			want:  []Line{{Number: 3, HasFunction: true, Name: "f", SystemName: "f", Filename: "f.go"}},
		},
		{
			name: "multiple inlined lines, mixed function/none",
			loc: &pprofpb.Location{Address: 0x6000, Line: []*pprofpb.Line{
				{Line: 11, FunctionId: 1}, // innermost
				{Line: 22},                // inlined caller with no function
				{Line: 33, FunctionId: 2},
			}},
			funcs: []*pprofpb.Function{
				{Id: 1, Name: 1, SystemName: 1, Filename: 4, StartLine: 1},
				{Id: 2, Name: 2, SystemName: 2, Filename: 4, StartLine: 2},
			},
			strs: []string{"", "inner", "outer", "", "x.go"},
			want: []Line{
				{Number: 11, HasFunction: true, Name: "inner", SystemName: "inner", Filename: "x.go", StartLine: 1},
				{Number: 22, HasFunction: false},
				{Number: 33, HasFunction: true, Name: "outer", SystemName: "outer", Filename: "x.go", StartLine: 2},
			},
		},
		{
			name:  "unicode and a long name",
			loc:   &pprofpb.Location{Address: 0x7000, Line: []*pprofpb.Line{{Line: 500, FunctionId: 1}}},
			funcs: []*pprofpb.Function{{Id: 1, Name: 1, SystemName: 2, Filename: 3, StartLine: 1}},
			strs: []string{
				"",
				"pkg.(*Café).Ålgorithm[go.shape.int]",
				repeat("veryLongSystemName_", 40),
				"/a/deeply/nested/path/to/source/file/that/is/long.go",
			},
			want: []Line{{
				Number: 500, HasFunction: true,
				Name:       "pkg.(*Café).Ålgorithm[go.shape.int]",
				SystemName: repeat("veryLongSystemName_", 40),
				Filename:   "/a/deeply/nested/path/to/source/file/that/is/long.go",
				StartLine:  1,
			}},
		},
		{
			name: "no lines at all",
			loc:  &pprofpb.Location{Address: 0x8000},
			strs: []string{""},
			want: nil,
		},
	}

	out := make([]Case, 0, len(specs))
	for _, s := range specs {
		c := Case{
			Name:    s.name,
			Encoded: profile.EncodePprofLocation(s.loc, s.mapping, s.funcs, s.strs),
			Lines:   s.want,
		}
		if s.mapping != nil {
			c.HasMapping = true
			c.BuildID = s.strs[s.mapping.BuildId]
			c.File = s.strs[s.mapping.Filename]
			c.MemoryStart = s.mapping.MemoryStart
			c.MemoryLimit = s.mapping.MemoryLimit
			c.MemoryOffset = s.mapping.FileOffset
		}
		out = append(out, c)
	}
	return out
}

// Adversarial returns malformed encodings that a decoder must survive without
// panicking. Truncating a valid record (as the fuzz seeds also do) rarely lands
// exactly on a string length-prefix or a flag byte, so these craft the specific
// shapes that crashed the decoders before: a length prefix that claims more
// bytes than follow, an oversized length with bit 63 set (the value that breaks
// a signed bound), a truncated varint, a flag byte past the end, and a line body
// that stops early (#107/#111). Each is a deterministic seed, so a decoder that
// drops or weakens a bounds check fails the fuzz's seed run -- no -fuzz flag
// needed.
func Adversarial() [][]byte {
	var out [][]byte

	// A well-formed prefix: address, one line, no mapping, lineNumber, column,
	// hasFunction=1, functionStartLine -- everything up to the first string.
	prefix := func() []byte {
		var b []byte
		b = appendUvarint(b, 0) // address
		b = appendUvarint(b, 1) // numLines
		b = append(b, 0x0)      // hasMapping = false
		b = appendUvarint(b, 7) // lineNumber
		b = appendUvarint(b, 0) // column
		b = append(b, 0x1)      // hasFunction = true
		b = appendUvarint(b, 3) // functionStartLine
		return b
	}

	// Name length prefix claims 50 bytes; none follow.
	out = append(out, append(prefix(), appendUvarint(nil, 50)...))

	// Name length prefix claims 50 bytes; only a few follow.
	out = append(out, append(append(prefix(), appendUvarint(nil, 50)...), []byte("short")...))

	// Oversized length: the length must have bit 63 set, because that is the
	// value that breaks a signed bound. length > len(data)-offset done in int
	// space casts such a length to a negative int, so offset+int(length) lands
	// below offset and slips past the check into a panicking slice -- the exact
	// #111 regression. 1<<63 is the smallest such value; MaxUint64 is the
	// largest. (A merely large-but-positive length like 1<<62 does NOT test
	// this: int(1<<62) is still positive and a signed check handles it.)
	out = append(out, append(prefix(), appendUvarint(nil, 1<<63)...))
	out = append(out, append(prefix(), appendUvarint(nil, math.MaxUint64)...))

	// Truncated varint: a length byte with the continuation bit set and no
	// continuation.
	out = append(out, append(prefix(), 0x80))

	// A line is promised (numLines=1) but the bytes end right where the
	// hasFunction flag byte should be -- the flag read must bounds-check, not
	// index past the end.
	{
		var b []byte
		b = appendUvarint(b, 0) // address
		b = appendUvarint(b, 1) // numLines
		b = append(b, 0x0)      // hasMapping = false
		b = appendUvarint(b, 7) // lineNumber
		b = appendUvarint(b, 0) // column -- and then nothing: hasFunction is missing
		out = append(out, b)
	}

	// numLines is promised but nothing follows at all -- the very first line
	// read (lineNumber uvarint) is already past the end.
	{
		var b []byte
		b = appendUvarint(b, 0) // address
		b = appendUvarint(b, 1) // numLines -- but nothing follows
		out = append(out, b)
	}

	// hasMapping = true, but the buildID length prefix claims bytes that are not
	// there -- the mapping-skip path, which every line read depends on.
	{
		var b []byte
		b = appendUvarint(b, 0)  // address
		b = appendUvarint(b, 1)  // numLines
		b = append(b, 0x1)       // hasMapping = true
		b = appendUvarint(b, 20) // buildID length = 20, none follow
		out = append(out, b)
	}

	return out
}

func appendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func repeat(s string, n int) string {
	b := make([]byte, 0, len(s)*n)
	for range n {
		b = append(b, s...)
	}
	return string(b)
}
