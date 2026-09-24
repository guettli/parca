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

	"github.com/parca-dev/parca/pkg/duckdb"
)

func countRows(t *testing.T, c *duckdb.Client) int {
	t.Helper()
	var n int
	require.NoError(t, c.DB().QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+c.Table()).Scan(&n))
	return n
}

// TestDeleteOlderThan verifies that retention deletes rows strictly older than
// the cutoff, keeps rows at or after it (including exactly at the cutoff), and
// that a repeated run is a no-op.
func TestDeleteOlderThan(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()
	ing := duckdb.NewIngester(log.NewNopLogger(), client, nil)

	now := time.Now()
	cutoff := now.Add(-7 * 24 * time.Hour).UnixMilli()
	oldTs := now.Add(-10 * 24 * time.Hour).UnixMilli() // older than cutoff -> pruned
	boundaryTs := cutoff                               // exactly at cutoff  -> kept (strict <)
	recentTs := now.Add(-1 * time.Hour).UnixMilli()    // newer than cutoff  -> kept

	for _, ts := range []int64{oldTs, boundaryTs, recentTs} {
		rec := buildSampleRecord(t, mem, ts)
		require.NoError(t, ing.Ingest(ctx, rec))
		rec.Release()
	}
	require.Equal(t, 3, countRows(t, client))

	// Only the row strictly older than the cutoff is removed; the boundary row
	// (timestamp == cutoff) and the recent row stay.
	n, err := client.DeleteOlderThan(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	require.Equal(t, 2, countRows(t, client))

	// Running again deletes nothing.
	n, err = client.DeleteOlderThan(ctx, cutoff)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)
	require.Equal(t, 2, countRows(t, client))
}

// TestRunRetentionDisabled verifies RunRetention returns immediately when
// retention is disabled (<= 0) and touches nothing.
func TestRunRetentionDisabled(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()
	ing := duckdb.NewIngester(log.NewNopLogger(), client, nil)

	rec := buildSampleRecord(t, mem, time.Now().Add(-100*24*time.Hour).UnixMilli())
	require.NoError(t, ing.Ingest(ctx, rec))
	rec.Release()
	require.Equal(t, 1, countRows(t, client))

	// retention <= 0 must return at once and delete nothing, even though the
	// row is very old. A bounded context guards against a hang regression.
	done := make(chan struct{})
	go func() {
		defer close(done)
		duckdb.RunRetention(ctx, log.NewNopLogger(), client, 0, time.Hour)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunRetention did not return promptly when disabled")
	}
	require.Equal(t, 1, countRows(t, client))
}
