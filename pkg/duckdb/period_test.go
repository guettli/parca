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
	"time"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/parca-dev/parca/pkg/duckdb"
)

// A query's Meta must carry the sampling period from the stored data, and a
// single profile's duration with it. The selector names the period *type* ("cpu:nanoseconds") but never its
// value, so Meta was left at 0 and the pprof went out with Period: 0.
//
// That is not cosmetic. A sampled CPU profile's values are a count of stack
// samples; multiplying by the period is what turns them into CPU time. A
// consumer that finds no period cannot do that conversion and reports the
// profile as a bare count -- against a real server this showed up as every
// number being a sample count where it should have been cores.
func TestQueryCarriesPeriodAndDuration(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()
	logger := log.NewNopLogger()
	tracer := noop.NewTracerProvider().Tracer("")

	const tsMillis int64 = 1_700_000_000_000
	// buildSampleRecord writes period 10ms and duration 1s.
	const (
		wantPeriod   int64 = 10_000_000
		wantDuration int64 = int64(time.Second)
	)

	rec := buildSampleRecord(t, mem, tsMillis)
	defer rec.Release()
	require.NoError(t, duckdb.NewIngester(logger, client).Ingest(ctx, rec))

	q := duckdb.NewQuerier(client, logger, tracer, mem, nopSymbolizer{})
	queryStr := `process_cpu:cpu:nanoseconds:cpu:nanoseconds:delta{job="test"}`
	start := time.UnixMilli(tsMillis - 1_000)
	end := time.UnixMilli(tsMillis + 1_000)

	t.Run("merge", func(t *testing.T) {
		p, err := q.QueryMerge(ctx, queryStr, start, end, []string{"job"}, false, "")
		require.NoError(t, err)
		defer func() {
			for _, r := range p.Samples {
				r.Release()
			}
		}()

		require.Equal(t, wantPeriod, p.Meta.Period, "merged profile lost the sampling period")
		// A merge spans many profiles, so none of their durations describes
		// the result and Meta deliberately carries none. Asserted, not
		// ignored: filling it with the query window would look plausible and
		// silently rescale anything that divides by it.
		require.Zero(t, p.Meta.Duration, "a merge must not claim a single profile's duration")
	})

	t.Run("single", func(t *testing.T) {
		p, err := q.QuerySingle(ctx, queryStr, time.UnixMilli(tsMillis), false)
		require.NoError(t, err)
		defer func() {
			for _, r := range p.Samples {
				r.Release()
			}
		}()

		require.Equal(t, wantPeriod, p.Meta.Period, "single profile lost the sampling period")
		// Exact, not merely positive: every row of one profile repeats its
		// duration, so SUM over the stacktrace groups multiplies it by the
		// number of distinct stacks. "Positive" cannot see that.
		require.Equal(t, wantDuration, p.Meta.Duration, "single profile has the wrong duration")
	})
}
