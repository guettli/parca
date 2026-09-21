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

package duckdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"regexp"

	duckdb "github.com/marcboeker/go-duckdb/v2"
	"golang.org/x/sync/semaphore"
)

// Config holds DuckDB connection configuration.
//
// Path is the on-disk file path. An empty Path uses an in-memory database.
// Table is the name of the profile data table.
// MemoryLimit, when non-empty, caps DuckDB's own (C++) memory via
// `SET memory_limit` — e.g. "3GB", "3GiB", "2500MB". This keeps DuckDB inside a
// container's cgroup limit: by default DuckDB sizes its limit to a fraction of
// *detected host* RAM (not the cgroup), so it can blow past the pod limit and
// get OOMKilled. Beyond the cap DuckDB spills to its temp directory for most
// operators, but some heavy ones (e.g. a large ORDER BY) can instead fail with
// an out-of-memory error rather than spill — so size it with headroom below the
// cgroup limit and keep the temp directory writable. Empty leaves DuckDB's
// default.
type Config struct {
	Path        string
	Table       string
	MemoryLimit string
}

// memoryLimitRe matches the memory-limit forms DuckDB accepts: a number with an
// optional decimal and a byte unit — decimal (KB/MB/GB/TB) or binary
// (KiB/MiB/GiB/TiB), case-insensitive, with an optional space. Percentages are
// NOT accepted: DuckDB rejects "%" for memory_limit. Validated because the
// value is interpolated into a SET statement.
var memoryLimitRe = regexp.MustCompile(`(?i)^[0-9]+(\.[0-9]+)?\s*(K|M|G|T)i?B$`)

// maxOpenConns bounds the connection pool. It must be more than one.
//
// DuckDB runs readers concurrently with writers under MVCC, so a pool of one
// buys nothing the engine requires -- it only means any single long-running
// statement holds the process's only connection, and everything else waits.
// Reads are the ones that run long here: ProfileTypes, Labels, Values and
// GetProfileMetadataMappings all issue SELECTs with no time predicate, so each
// is a full scan of a table that grows without bound. While one of those holds
// the only connection, every agent's write fails at the pool with
// "acquire duckdb connection", which is what a production server did.
//
// A pool does not make writers immune to everything: a CHECKPOINT blocks
// writers on every connection for as long as it runs, whatever the pool size.
// It is measurably cheap when there is little write-ahead log to fold, which is
// the normal case under DuckDB's own 16 MB auto-checkpoint threshold -- but the
// pool is not what protects ingestion from a checkpoint, and this comment
// should not be read as claiming it is.
const maxOpenConns = 4

// Client wraps a DuckDB database/sql connection pool.
type Client struct {
	db  *sql.DB
	cfg Config

	// writeSem admits writers. See LockWrites and LockWritesExclusive.
	writeSem *semaphore.Weighted
}

// NewClient opens a DuckDB connection at cfg.Path (file) or in memory if
// the path is empty. The on-disk file is created if it doesn't exist.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.MemoryLimit != "" && !memoryLimitRe.MatchString(cfg.MemoryLimit) {
		return nil, fmt.Errorf("invalid duckdb memory limit %q (want e.g. 3GB, 3GiB, or 2500MB)", cfg.MemoryLimit)
	}

	// Apply memory_limit on every new connection via a connector init hook,
	// not once: DuckDB's limit is per-instance, and for the default in-memory
	// database each new connection is a fresh instance at the host-sized
	// default, so a set-once approach would leave a recycled connection
	// uncapped. connInitFn runs on every connection. It uses its own context
	// because it fires whenever database/sql opens a connection, not just at
	// startup.
	connector, err := duckdb.NewConnector(cfg.Path, func(execer driver.ExecerContext) error {
		if cfg.MemoryLimit == "" {
			return nil
		}
		_, err := execer.ExecContext(context.Background(),
			fmt.Sprintf("SET memory_limit = '%s'", cfg.MemoryLimit), nil)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("open duckdb: %w", err)
	}

	db := sql.OpenDB(connector)

	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)

	// Open a connection now so a bad memory_limit or DSN fails at startup
	// rather than on the first query.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open duckdb: %w", err)
	}

	return &Client{db: db, cfg: cfg, writeSem: semaphore.NewWeighted(maxOpenConns)}, nil
}

// LockWrites admits one writer, blocking only against an exclusive holder, and
// returns the function that releases the slot. It honours ctx, so a caller that
// has run out of time fails instead of queueing past its own deadline.
//
// Concurrent appends are deliberately allowed: DuckDB permits several write
// transactions at once and aborts only those whose row changes conflict, which
// inserts never do. Measured, eight concurrent appenders to one table land
// every row without error. So this is not a "one writer at a time" lock.
//
// What it exists for is CHECKPOINT. A non-forced checkpoint refuses to run
// while another connection holds an open write transaction, failing with
// "Cannot CHECKPOINT: there are other write transactions active". Writers take
// a slot, the checkpoint takes them all, and that conflict cannot happen.
func (c *Client) LockWrites(ctx context.Context) (func(), error) {
	if err := c.writeSem.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("acquire duckdb write slot: %w", err)
	}
	return func() { c.writeSem.Release(1) }, nil
}

// LockWritesExclusive waits for every in-flight writer to finish and keeps new
// ones out until the returned function is called. Used by CHECKPOINT; see
// LockWrites for why.
func (c *Client) LockWritesExclusive(ctx context.Context) (func(), error) {
	if err := c.writeSem.Acquire(ctx, maxOpenConns); err != nil {
		return nil, fmt.Errorf("acquire duckdb write lock: %w", err)
	}
	return func() { c.writeSem.Release(maxOpenConns) }, nil
}

// Close closes the underlying database/sql connections.
func (c *Client) Close() error { return c.db.Close() }

// DB returns the underlying *sql.DB.
func (c *Client) DB() *sql.DB { return c.db }

// Table returns the profile data table name.
func (c *Client) Table() string { return c.cfg.Table }

// EnsureSchema creates the profile data table if it doesn't already exist.
func (c *Client) EnsureSchema(ctx context.Context) error {
	if _, err := c.db.ExecContext(ctx, CreateTableSQL(c.cfg.Table)); err != nil {
		return fmt.Errorf("create table %q: %w", c.cfg.Table, err)
	}
	return nil
}
