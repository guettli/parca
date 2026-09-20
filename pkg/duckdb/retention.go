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

package duckdb

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
)

// DeleteOlderThan removes every profile row whose timestamp is strictly older
// than cutoffUnixMilli (the same millisecond-since-epoch unit Parca writes into
// the timestamp column), returning the number of rows deleted. It does not
// checkpoint; callers that want the freed pages folded back into the file
// should call Checkpoint afterwards.
func (c *Client) DeleteOlderThan(ctx context.Context, cutoffUnixMilli int64) (int64, error) {
	// quotedTable quotes the (trusted, config-supplied) table identifier, the
	// same way every other statement in this package builds it.
	res, err := c.db.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE %s < ?", quotedTable(c), ColTimestamp),
		cutoffUnixMilli,
	)
	if err != nil {
		return 0, fmt.Errorf("delete rows older than %d: %w", cutoffUnixMilli, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return n, nil
}

// Checkpoint folds the write-ahead log back into the database file so pages
// freed by deletes become reusable by later inserts. DuckDB reuses that space
// in place, which bounds the file's growth rather than shrinking it on disk.
func (c *Client) Checkpoint(ctx context.Context) error {
	if _, err := c.db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	return nil
}

// RunRetention deletes rows older than retention every interval, until ctx is
// cancelled. It runs one pass immediately so a restart enforces the policy
// without waiting a full interval. retention must be > 0; interval is clamped
// to a sane minimum. This blocks, so run it in its own goroutine.
func RunRetention(ctx context.Context, logger log.Logger, c *Client, retention, interval time.Duration) {
	if retention <= 0 {
		return
	}
	if interval < time.Minute {
		level.Warn(logger).Log("msg", "duckdb retention interval clamped to minimum", "requested", interval.String(), "using", time.Minute.String())
		interval = time.Minute
	}

	run := func() {
		cutoff := time.Now().Add(-retention)
		n, err := c.DeleteOlderThan(ctx, cutoff.UnixMilli())
		if err != nil {
			level.Error(logger).Log("msg", "duckdb retention delete failed", "retention", retention.String(), "err", err)
			return
		}
		level.Info(logger).Log("msg", "duckdb retention delete", "retention", retention.String(), "cutoff", cutoff.UTC().Format(time.RFC3339), "rows_deleted", n)
		// A checkpoint failure is not fatal: the rows are already gone, only
		// the space reclaim is deferred. Report it, but don't treat the pass
		// as failed.
		if err := c.Checkpoint(ctx); err != nil {
			level.Warn(logger).Log("msg", "duckdb retention checkpoint failed", "err", err)
		}
	}

	run()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
