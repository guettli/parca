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

package duckdb_test

import (
	"context"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"

	"github.com/parca-dev/parca/pkg/duckdb"
)

func countRows(t *testing.T, c *duckdb.Client) int {
	t.Helper()
	var n int
	require.NoError(t, c.DB().QueryRowContext(context.Background(),
		"SELECT count(*) FROM stacktraces").Scan(&n))
	return n
}

// TestDeleteOlderThan verifies that retention deletes rows strictly older than
// the cutoff and leaves newer rows in place, and that a repeated run is a no-op.
func TestDeleteOlderThan(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()
	ing := duckdb.NewIngester(log.NewNopLogger(), client)

	now := time.Now()
	oldTs := now.Add(-10 * 24 * time.Hour).UnixMilli() // 10 days old -> pruned
	recentTs := now.Add(-1 * time.Hour).UnixMilli()    // 1 hour old  -> kept

	for _, ts := range []int64{oldTs, recentTs} {
		rec := buildSampleRecord(t, mem, ts)
		require.NoError(t, ing.Ingest(ctx, rec))
		rec.Release()
	}
	require.Equal(t, 2, countRows(t, client))

	// Keep 7 days: the 10-day-old row is removed, the 1-hour-old row stays.
	cutoff := now.Add(-7 * 24 * time.Hour).UnixMilli()
	n, err := client.DeleteOlderThan(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	require.Equal(t, 1, countRows(t, client))

	// Running again deletes nothing.
	n, err = client.DeleteOlderThan(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
	require.Equal(t, 1, countRows(t, client))
}
