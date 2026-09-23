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
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/parca-dev/parca/pkg/profile"
	"github.com/parca-dev/parca/pkg/symbolizer"
)

// A v2-ingested profile stores its symbol in function_system_name with
// function_name empty (normalizer.encodeV2Location). With no mapping build ID
// the querier never offers the frame to the symbolizer, so before the fallback
// the stored-data arm's `function_name != ""` guard was false and the frame was
// dropped -- the symbol sat in the row, one column over from the answer. The
// querier must fall back to the system name.
//
// The symbolizer here panics if it is ever called, which also proves this frame
// took the stored arm and was not rescued by symbolization.
func TestRowsToArrowFallsBackToSystemName(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	q := &Querier{
		logger:     log.NewNopLogger(),
		tracer:     noop.NewTracerProvider().Tracer(""),
		mem:        mem,
		symbolizer: unusedSymbolizer{t},
	}

	// One sample, one location: empty name, symbol in the system name, and no
	// mapping build ID -- exactly a v2 stored row.
	rows := &oneSampleRows{s: sampleData{
		addresses:           []uint64{0xf00d},
		mappingStarts:       []uint64{0},
		mappingLimits:       []uint64{0},
		mappingOffsets:      []uint64{0},
		mappingFiles:        []string{""},
		mappingBuildIDs:     []string{""},
		lineNumbers:         []int64{7},
		functionNames:       []string{""},
		functionSystemNames: []string{"v2.only.systemname"},
		functionFilenames:   []string{"main.go"},
		functionStartLines:  []int64{1},
		value:               42,
		labelsJSON:          "{}",
		period:              10_000_000,
	}}

	recs, err := q.rowsToArrowRecords(context.Background(), rows, false)
	require.NoError(t, err)
	require.NotEmpty(t, recs)

	var names []string
	for _, r := range recs {
		rr, err := profile.NewRecordReader(r)
		require.NoError(t, err)
		for i := 0; i < rr.LineFunctionNameIndices.Len(); i++ {
			if rr.LineFunctionNameIndices.IsNull(i) {
				continue
			}
			names = append(names, string(rr.LineFunctionNameDict.Value(int(rr.LineFunctionNameIndices.Value(i)))))
		}
		r.Release()
	}

	require.Contains(t, names, "v2.only.systemname",
		"a v2 profile's symbol, stored in the system name, never reached the record")
}

// oneSampleRows yields exactly one sampleData row.
type oneSampleRows struct {
	s    sampleData
	done bool
}

func (m *oneSampleRows) Next() bool {
	if m.done {
		return false
	}
	m.done = true
	return true
}

func (m *oneSampleRows) Err() error { return nil }

func (m *oneSampleRows) Scan(dest ...interface{}) error {
	*dest[0].(*[]uint64) = m.s.addresses
	*dest[1].(*[]uint64) = m.s.mappingStarts
	*dest[2].(*[]uint64) = m.s.mappingLimits
	*dest[3].(*[]uint64) = m.s.mappingOffsets
	*dest[4].(*[]string) = m.s.mappingFiles
	*dest[5].(*[]string) = m.s.mappingBuildIDs
	*dest[6].(*[]int64) = m.s.lineNumbers
	*dest[7].(*[]string) = m.s.functionNames
	*dest[8].(*[]string) = m.s.functionSystemNames
	*dest[9].(*[]string) = m.s.functionFilenames
	*dest[10].(*[]int64) = m.s.functionStartLines
	*dest[11].(*int64) = m.s.value
	*dest[12].(*string) = m.s.labelsJSON
	*dest[13].(*int64) = m.s.duration
	*dest[14].(*int64) = m.s.period
	return nil
}

// unusedSymbolizer fails the test if the querier ever tries to symbolize -- the
// v2 frame is already symbolised and must not take that path.
type unusedSymbolizer struct{ t *testing.T }

func (u unusedSymbolizer) Symbolize(context.Context, symbolizer.SymbolizationRequest) error {
	u.t.Fatal("symbolizer must not be called: the v2 frame is already symbolised in the system name")
	return nil
}
