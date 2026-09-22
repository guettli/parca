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
