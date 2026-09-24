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

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	pprof "github.com/parca-dev/parca/gen/proto/go/google/pprof"
	"github.com/parca-dev/parca/pkg/profile"
)

func chCounterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() == name {
			require.NotEmpty(t, mf.GetMetric())
			return mf.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("metric %q not registered", name)
	return 0
}

// The ClickHouse ingest path is the default backend, so its counting of dropped
// inlined frames (guettli/parca#109) needs its own coverage -- the duckdb test
// does not exercise this code. extractStacktraceData reads the stacktrace column
// without touching the ClickHouse connection, so it can be driven directly.
func TestExtractStacktraceCountsDroppedInlinedFrames(t *testing.T) {
	// One location with two lines: an inlined caller above the innermost frame.
	twoLine := profile.EncodePprofLocation(
		&pprof.Location{Address: 0x1, Line: []*pprof.Line{
			{Line: 1, FunctionId: 1},
			{Line: 2, FunctionId: 2},
		}},
		nil,
		[]*pprof.Function{
			{Id: 1, Name: 1, SystemName: 1, Filename: 2},
			{Id: 2, Name: 3, SystemName: 3, Filename: 2},
		},
		[]string{"", "inner", "x.go", "outer"},
	)

	// A stacktrace column: List(Dictionary(Binary)) with a single location.
	mem := memory.NewGoAllocator()
	dictType := &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Uint32, ValueType: arrow.BinaryTypes.Binary}
	lb := array.NewListBuilder(mem, dictType)
	defer lb.Release()
	vb := lb.ValueBuilder().(*array.BinaryDictionaryBuilder)
	lb.Append(true)
	require.NoError(t, vb.Append(twoLine))
	listArr := lb.NewListArray()
	defer listArr.Release()

	schema := arrow.NewSchema([]arrow.Field{{Name: profile.ColumnStacktrace, Type: arrow.ListOf(dictType)}}, nil)
	rec := array.NewRecord(schema, []arrow.Array{listArr}, 1)
	defer rec.Release()

	reg := prometheus.NewRegistry()
	ing := NewIngester(log.NewNopLogger(), nil, reg)

	data := ing.extractStacktraceData(rec, 0, 0)
	require.Equal(t, []string{"inner"}, data.FunctionNames, "the innermost line must still decode")

	require.Equal(t, 1.0, chCounterValue(t, reg, "parca_ingest_locations_total"),
		"one location reference was decoded")
	require.Equal(t, 1.0, chCounterValue(t, reg, "parca_ingest_locations_with_inlined_frames_total"),
		"the two-line location must be counted as having inlined frames")
	require.Equal(t, 1.0, chCounterValue(t, reg, "parca_ingest_inlined_frames_dropped_total"),
		"a two-line location drops numLines-1 = 1 inlined frame")
}
