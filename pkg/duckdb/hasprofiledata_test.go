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
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/parca-dev/parca/pkg/duckdb"
)

// HasProfileData answers a yes/no the UI polls, and must answer it without
// scanning the table -- it used to run ProfileTypes' unbounded SELECT DISTINCT,
// which on a real server held the only connection and stalled ingestion.
//
// The empty case is the one that changed: an empty table now returns false via
// the LIMIT 1 finding no row, where before it returned false via a full scan
// finding no distinct types. Both cases are asserted so the answer is pinned,
// not just the absence of a scan (which a unit test cannot see directly).
func TestHasProfileData(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()
	q := duckdb.NewQuerier(client, log.NewNopLogger(), noop.NewTracerProvider().Tracer(""), mem, nopSymbolizer{})

	// Empty table.
	has, err := q.HasProfileData(ctx)
	require.NoError(t, err)
	require.False(t, has, "an empty table has no profile data")

	// After one ingest.
	const tsMillis int64 = 1_700_000_000_000
	rec := buildSampleRecord(t, mem, tsMillis)
	defer rec.Release()
	require.NoError(t, duckdb.NewIngester(log.NewNopLogger(), client).Ingest(ctx, rec))

	has, err = q.HasProfileData(ctx)
	require.NoError(t, err)
	require.True(t, has, "a table with one row has profile data")
}

// The existence check must stay an existence check. The behavioural test above
// passes whether HasProfileData runs LIMIT 1 or the full SELECT DISTINCT scan
// it replaced -- both answer the same yes/no -- so it cannot, on its own, stop
// a regression to the scan. This asserts the mechanism.
func TestHasProfileDataQueryDoesNotScan(t *testing.T) {
	q := duckdb.HasProfileDataQueryForTest("profiles")
	require.Contains(t, q, "LIMIT 1", "existence needs one row, not a scan")
	require.NotContains(t, q, "DISTINCT", "a DISTINCT over the table is the full scan this replaced")
	require.Contains(t, q, "profiles", "must query the given table")
}
