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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parca-dev/parca/pkg/duckdb"
)

// TestMemoryLimitApplied verifies a configured MemoryLimit is pushed into DuckDB
// via SET memory_limit. It parses the reported value and asserts it is well
// below the requested cap's neighbourhood — a 256MB cap reports ~244 MiB — so
// the test can't false-pass on a small runner where DuckDB's default limit
// would also be reported in MiB.
func TestMemoryLimitApplied(t *testing.T) {
	c, err := duckdb.NewClient(context.Background(), duckdb.Config{
		Path: "", Table: "stacktraces", MemoryLimit: "256MB",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	var v string
	require.NoError(t, c.DB().QueryRowContext(context.Background(),
		"SELECT current_setting('memory_limit')").Scan(&v))

	// e.g. "244.1 MiB"
	fields := strings.Fields(v)
	require.Len(t, fields, 2, "unexpected memory_limit format %q", v)
	require.Equal(t, "MiB", fields[1], "expected a small (MiB) limit, got %q", v)
	n, err := strconv.ParseFloat(fields[0], 64)
	require.NoError(t, err)
	require.Lessf(t, n, 512.0, "memory_limit %q not capped near 256MB", v)
}

// TestMemoryLimitBinaryUnit verifies a binary unit (what operators get from a
// k8s "4Gi"-style limit) is accepted and applied.
func TestMemoryLimitBinaryUnit(t *testing.T) {
	c, err := duckdb.NewClient(context.Background(), duckdb.Config{
		Path: "", Table: "stacktraces", MemoryLimit: "300MiB",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	var v string
	require.NoError(t, c.DB().QueryRowContext(context.Background(),
		"SELECT current_setting('memory_limit')").Scan(&v))
	require.Contains(t, v, "MiB")
}

// TestMemoryLimitInvalid verifies malformed limits are rejected up front rather
// than interpolated into a SET statement. "80%" is included on purpose: DuckDB
// rejects percentages for memory_limit, so we must too.
func TestMemoryLimitInvalid(t *testing.T) {
	for _, bad := range []string{"banana", "80%", "1024", "3 GB extra", "'; DROP"} {
		_, err := duckdb.NewClient(context.Background(), duckdb.Config{
			Path: "", Table: "stacktraces", MemoryLimit: bad,
		})
		require.Errorf(t, err, "expected %q to be rejected", bad)
	}
}
