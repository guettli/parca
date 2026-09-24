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

//go:build duckdb

package duckdb_test

import (
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	pprofpb "github.com/parca-dev/parca/gen/proto/go/google/pprof"
	"github.com/parca-dev/parca/pkg/duckdb"
	"github.com/parca-dev/parca/pkg/profile"
)

func counterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
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

// A location that carries more than one line drops its inlined callers at ingest
// (only line[0] is stored). That loss was silent; it is now counted
// (guettli/parca#109). One two-line location must bump both the decoded counter
// and the inlined-dropped counter by one.
func TestIngestCountsDroppedInlinedFrames(t *testing.T) {
	const ts int64 = 1_700_000_000_000

	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	reg := prometheus.NewRegistry()
	ing := duckdb.NewIngester(log.NewNopLogger(), client, reg)

	// One location with two lines: an inlined caller above the innermost frame.
	twoLine := profile.EncodePprofLocation(
		&pprofpb.Location{Address: 0x1, Line: []*pprofpb.Line{
			{Line: 1, FunctionId: 1},
			{Line: 2, FunctionId: 2},
		}},
		nil,
		[]*pprofpb.Function{
			{Id: 1, Name: 1, SystemName: 1, Filename: 2},
			{Id: 2, Name: 3, SystemName: 3, Filename: 2},
		},
		[]string{"", "inner", "x.go", "outer"},
	)

	rec := buildRecordWithLocation(t, mem, ts, twoLine)
	defer rec.Release()
	require.NoError(t, ing.Ingest(context.Background(), rec))

	require.Equal(t, 1.0, counterValue(t, reg, "parca_ingest_locations_decoded_total"),
		"one location was decoded")
	require.Equal(t, 1.0, counterValue(t, reg, "parca_ingest_locations_inlined_dropped_total"),
		"the two-line location's inlined caller must be counted as dropped")
}
