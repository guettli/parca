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

package clickhouse

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	pprofpb "github.com/parca-dev/parca/gen/proto/go/google/pprof"
	"github.com/parca-dev/parca/pkg/profile"
)

// A location encoded by the ingest path must decode to the same location.
//
// It did not, in this backend and in the DuckDB one, for the same reason: the
// encoder writes a column between the line number and the hasFunction flag --
// pprof carries none, so it is a uvarint zero, one 0x00 byte -- and the
// decoder read that byte as the flag, found it false, and discarded every
// function name.
//
// This is the default backend, so it is the copy that mattered most. The bug
// was found against DuckDB only because that is what one server happened to
// run: a Go service's goroutine profile returned 120 goroutines and 100%
// [unsymbolized] while the same endpoint scraped directly named all 120.
//
// Written against the real encoder rather than a hand-built blob, because a
// hand-built one would be built from the same misreading -- which is not
// hypothetical: the DuckDB package's own fixture omitted the column too,
// because it had been written to match the decoder instead of the format.
func TestLocationSurvivesTheRoundTripItIsEncodedFor(t *testing.T) {
	const (
		wantName  = "bufio.(*Reader).Peek"
		wantFile  = "/usr/local/go/src/bufio/bufio.go"
		wantLine  = 42
		wantStart = 10
	)
	strs := []string{"", wantName, wantName, wantFile, "build-id", "/bin/svc"}
	funcs := []*pprofpb.Function{{Id: 1, Name: 1, SystemName: 2, Filename: 3, StartLine: wantStart}}

	for _, tc := range []struct {
		name    string
		mapping *pprofpb.Mapping
	}{
		// The mapping block sits between the line count and the lines, so a
		// decoder wrong about one is easily right about the other. Non-zero
		// values, so the uvarints are more than one byte each.
		{"with a mapping", &pprofpb.Mapping{Id: 1, BuildId: 4, Filename: 5, MemoryStart: 0x1000, MemoryLimit: 0x2000, FileOffset: 8}},
		{"without a mapping", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc := &pprofpb.Location{
				Id: 1, Address: 0xdeadbeef,
				Line: []*pprofpb.Line{{FunctionId: 1, Line: wantLine}},
			}
			if tc.mapping != nil {
				loc.MappingId = tc.mapping.Id
			}
			got := decodeLineInfo(profile.EncodePprofLocation(loc, tc.mapping, funcs, strs))

			require.Equal(t, wantName, got.FunctionName, "the function name was dropped")
			require.Equal(t, wantName, got.FunctionSystemName)
			require.Equal(t, wantFile, got.FunctionFilename)
			require.EqualValues(t, wantLine, got.LineNumber)
			require.EqualValues(t, wantStart, got.FunctionStartLine)
		})
	}
}

// A short or malformed record must not take the process down. These bytes are
// server-produced and self-consistent in normal operation, so the realistic
// way to get a bad one is encoder/decoder drift -- which is exactly what this
// file exists because of. The ingest path has no recovery interceptor, so a
// panic here is a dead server, not a failed request: before this was
// bounds-checked, 43 of the 58 truncations below panicked.
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
	require.NotPanics(t, func() { decodeLineInfo(nil) })
	require.NotPanics(t, func() { decodeLineInfo([]byte{}) })

	// A partial record must not invent a confidently-wrong name: a half-read
	// length prefix must not yield a string.
	for n := 0; n < len(full); n++ {
		got := decodeLineInfo(full[:n])
		if got.FunctionName != "" && got.FunctionName != "pkg.Func" {
			t.Fatalf("prefix of %d bytes invented a name: %q", n, got.FunctionName)
		}
	}
}

// The innermost inlined frame is kept, the callers above it dropped -- the same
// behaviour as the DuckDB backend, since both store one line per location.
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

// The existence check must stay an existence check, not regress to the
// SELECT DISTINCT scan it replaced. Behaviour is identical either way, so only
// the SQL distinguishes them.
func TestHasProfileDataQueryDoesNotScan(t *testing.T) {
	q := hasProfileDataQuery("db.profiles")
	require.Contains(t, q, "LIMIT 1", "existence needs one row, not a scan")
	require.NotContains(t, q, "DISTINCT", "a DISTINCT over the table is the full scan this replaced")
	require.Contains(t, q, "db.profiles", "must query the given table")
}
