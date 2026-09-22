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

package duckdb

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	pprofpb "github.com/parca-dev/parca/gen/proto/go/google/pprof"
	"github.com/parca-dev/parca/pkg/profile"
)

// A location encoded by the ingest path must decode to the same location.
//
// It did not. The encoder writes a column between the line number and the
// hasFunction flag -- pprof carries no column info, so it is a uvarint zero,
// one 0x00 byte -- and this decoder did not read it. It read that byte as the
// flag instead, found it false, and discarded every function name.
//
// Nothing failed while it happened. The address, the mapping and the line
// number all decoded, the row was stored, and the profile came back with every
// frame nameless. Against a real server a Go service's goroutine profile
// reported 120 goroutines and 100% [unsymbolized], while the same endpoint
// scraped directly named every one of them.
//
// Only profiles that arrive ALREADY symbolized are affected -- anything from a
// Go /debug/pprof endpoint -- and those are exactly the ones the symbolizer
// cannot rescue later, because they carry no build ID to look up.
//
// Written against the real encoder rather than a hand-built blob, because a
// hand-built one would have been built from the same misreading.
func TestLocationSurvivesTheRoundTripItIsEncodedFor(t *testing.T) {
	const (
		wantName   = "bufio.(*Reader).Peek"
		wantSystem = "bufio.(*Reader).Peek"
		wantFile   = "/usr/local/go/src/bufio/bufio.go"
		wantLine   = 42
		wantStart  = 10
	)
	strs := []string{"", wantName, wantSystem, wantFile, "build-id", "/bin/svc"}
	funcs := []*pprofpb.Function{{Id: 1, Name: 1, SystemName: 2, Filename: 3, StartLine: wantStart}}

	for _, tc := range []struct {
		name    string
		mapping *pprofpb.Mapping
	}{
		// Both arms matter: the mapping block sits between the line count and
		// the lines, so a decoder that is wrong about one is easily right
		// about the other.
		{"with a mapping", &pprofpb.Mapping{Id: 1, BuildId: 4, Filename: 5, MemoryStart: 0x1000, MemoryLimit: 0x2000, FileOffset: 8}},
		{"without a mapping", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc := &pprofpb.Location{
				Id:      1,
				Address: 0xdeadbeef,
				Line:    []*pprofpb.Line{{FunctionId: 1, Line: wantLine}},
			}
			if tc.mapping != nil {
				loc.MappingId = tc.mapping.Id
			}

			got := decodeLineInfo(profile.EncodePprofLocation(loc, tc.mapping, funcs, strs))

			require.Equal(t, wantName, got.FunctionName, "the function name was dropped")
			require.Equal(t, wantSystem, got.FunctionSystemName)
			require.Equal(t, wantFile, got.FunctionFilename)
			require.EqualValues(t, wantLine, got.LineNumber)
			require.EqualValues(t, wantStart, got.FunctionStartLine)
		})
	}
}

// A location with no lines still has to decode -- that is every sample from
// parca-agent, which sends addresses and lets the server symbolize them.
func TestLocationWithNoLinesStillDecodes(t *testing.T) {
	strs := []string{"", "build-id", "/bin/svc"}
	m := &pprofpb.Mapping{Id: 1, BuildId: 1, Filename: 2}
	loc := &pprofpb.Location{Id: 1, Address: 0xcafe, MappingId: 1}

	got := decodeLineInfo(profile.EncodePprofLocation(loc, m, nil, strs))

	// No lines, so no name -- and, crucially, no panic or garbage read out of
	// the mapping block that precedes them.
	require.Empty(t, got.FunctionName)
	require.Zero(t, got.LineNumber)
}

// A short or malformed record must not take the process down.
//
// These bytes are server-produced and self-consistent in normal operation, so
// the realistic way to get a bad one is encoder/decoder drift -- which is
// exactly what this file exists because of. There is no recovery interceptor
// on the ingest path, so a panic here is not a failed request, it is a dead
// server: before this was bounds-checked, 67 of the 110 truncations below
// panicked.
func TestATruncatedRecordDoesNotPanic(t *testing.T) {
	strs := []string{"", "pkg.Func", "pkg.Func", "f.go", "build-id", "/bin/svc"}
	funcs := []*pprofpb.Function{{Id: 1, Name: 1, SystemName: 2, Filename: 3, StartLine: 7}}
	m := &pprofpb.Mapping{Id: 1, BuildId: 4, Filename: 5, MemoryStart: 0x1000, MemoryLimit: 0x2000, FileOffset: 8}
	full := profile.EncodePprofLocation(
		&pprofpb.Location{
			Id: 1, Address: 0xdeadbeef, MappingId: 1,
			Line: []*pprofpb.Line{{FunctionId: 1, Line: 42}},
		},
		m, funcs, strs)

	for n := 0; n <= len(full); n++ {
		require.NotPanics(t, func() { decodeLineInfo(full[:n]) },
			"decoding the first %d of %d bytes panicked", n, len(full))
	}
	// Nil and empty, which a column with no value would produce.
	require.NotPanics(t, func() { decodeLineInfo(nil) })
	require.NotPanics(t, func() { decodeLineInfo([]byte{}) })

	// Whatever it returns for a partial record, it must not be a confidently
	// wrong name: a half-read length prefix must not yield a string.
	for n := 0; n < len(full); n++ {
		got := decodeLineInfo(full[:n])
		if got.FunctionName != "" && got.FunctionName != "pkg.Func" {
			t.Fatalf("prefix of %d bytes invented a name: %q", n, got.FunctionName)
		}
	}
}

// A location whose line carries no function -- the encoder writes the flag as
// 0 and stops -- decodes to a line number and nothing else.
func TestLineWithoutAFunctionDecodes(t *testing.T) {
	loc := &pprofpb.Location{Id: 1, Address: 1, Line: []*pprofpb.Line{{Line: 99}}}
	got := decodeLineInfo(profile.EncodePprofLocation(loc, nil, nil, []string{""}))
	require.EqualValues(t, 99, got.LineNumber)
	require.Empty(t, got.FunctionName)
}

// Inlined frames: the encoder writes every line, the schema holds one. The
// innermost is kept, which is Location.line[0] per the pprof spec -- and the
// callers above it are dropped, which is worth knowing rather than
// discovering from a flamegraph whose parents look wrong.
func TestOnlyTheInnermostInlinedFrameIsKept(t *testing.T) {
	strs := []string{"", "inner", "inner", "i.go", "outer", "outer", "o.go"}
	funcs := []*pprofpb.Function{
		{Id: 1, Name: 1, SystemName: 2, Filename: 3},
		{Id: 2, Name: 4, SystemName: 5, Filename: 6},
	}
	loc := &pprofpb.Location{Id: 1, Address: 1, Line: []*pprofpb.Line{
		{FunctionId: 1, Line: 11}, // innermost
		{FunctionId: 2, Line: 22},
	}}
	got := decodeLineInfo(profile.EncodePprofLocation(loc, nil, funcs, strs))
	require.Equal(t, "inner", got.FunctionName)
	require.EqualValues(t, 11, got.LineNumber)
}

// A length prefix larger than the record must not panic. Cast to int, a huge
// uvarint length goes negative, so offset+int(length) lands below offset and a
// naive `> len(data)` check waves it through into a slice with low > high. The
// guard has to compare in unsigned space.
func TestAnOverflowingLengthPrefixDoesNotPanic(t *testing.T) {
	var b []byte
	put := func(v uint64) { b = binary.AppendUvarint(b, v) }
	put(0)             // address
	put(1)             // numLines
	b = append(b, 0x0) // hasMapping = false
	put(7)             // lineNumber
	put(0)             // column
	b = append(b, 0x1) // hasFunction = true
	put(3)             // startLine
	put(1<<64 - 1)     // functionName length: enormous, nothing behind it

	require.NotPanics(t, func() {
		got := decodeLineInfo(b)
		require.Empty(t, got.FunctionName, "a length with no bytes behind it must not yield a string")
	})
}
