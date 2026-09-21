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
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/stretchr/testify/require"

	"github.com/parca-dev/parca/pkg/duckdb"
)

// A long-running read must not shut out writers.
//
// This is the shape of a real outage. With a pool of one connection, a single
// long statement held it and every agent's write failed at the pool with
// "acquire duckdb connection" for as long as that statement ran. The reads in
// this package invite it: ProfileTypes, Labels and friends issue SELECTs with
// no time predicate, so each is a full scan of a table that only grows.
//
// The reader here is genuinely executing, not an idle checked-out connection:
// an idle holder would demonstrate database/sql pool accounting rather than
// anything about DuckDB, and would pass at any pool size above one.
func TestSlowReadDoesNotBlockWrites(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()

	readDone := make(chan struct{})
	readStarted := make(chan struct{})
	go func() {
		defer close(readDone)
		var n sql.NullInt64
		close(readStarted)
		// Sized from measurement: this takes about five seconds, where
		// count(*) over the same range takes 190ms and would let the read
		// finish before the write even started -- a test that passes at a pool
		// of one and proves nothing. The overlap is asserted below rather than
		// assumed.
		_ = client.DB().QueryRowContext(ctx,
			"SELECT sum(i) FROM range(3000000000) t(i)").Scan(&n)
	}()
	<-readStarted

	rec := buildSampleRecord(t, mem, time.Now().UnixMilli())
	defer rec.Release()

	writeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	start := time.Now()
	err := duckdb.NewIngester(log.NewNopLogger(), client).Ingest(writeCtx, rec)
	elapsed := time.Since(start)

	require.NoError(t, err, "a write could not proceed while a read was running")

	// Without this the test is vacuous: if the read has already finished, the
	// write never contended with anything and would pass at a pool of one.
	select {
	case <-readDone:
		t.Fatal("the read finished before the write did; this proved nothing")
	default:
	}

	require.Equal(t, 1, countRows(t, client))
	t.Logf("write completed in %s while the scan was still running", elapsed)
	<-readDone
}

// Several appends at once must all land.
//
// DuckDB permits concurrent write transactions and aborts only those whose row
// changes conflict, which inserts never do -- so this passes with or without
// the write semaphore, and is here to keep that true rather than to prove the
// semaphore does something. What it would catch is a future change that
// serialized writers so hard they timed out, or one that made concurrent
// appends conflict.
func TestConcurrentWritesAllLand(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()
	ing := duckdb.NewIngester(log.NewNopLogger(), client)

	const writers = 8
	recs := make([]interface{ Release() }, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		rec := buildSampleRecord(t, mem, time.Now().Add(time.Duration(i)*time.Millisecond).UnixMilli())
		recs[i] = rec
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = ing.Ingest(ctx, rec)
		}()
	}
	wg.Wait()
	for _, r := range recs {
		r.Release()
	}
	for i, err := range errs {
		require.NoError(t, err, "writer %d failed", i)
	}
	require.Equal(t, writers, countRows(t, client))
}

// A checkpoint concurrent with appends must not fail as "busy".
//
// This is what the write semaphore exists for. A non-forced CHECKPOINT refuses
// to run while another connection holds an open write transaction:
//
//	TransactionContext Error: Cannot CHECKPOINT: there are other write
//	transactions active. Try using FORCE CHECKPOINT
//
// With nothing shared between writers and the checkpoint, they overlap and the
// checkpoint reports a failure that only means "busy" -- silently deferring
// space reclaim on exactly the busy server that needs it.
func TestCheckpointDoesNotRaceAppends(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()
	ing := duckdb.NewIngester(log.NewNopLogger(), client)

	const writers = 6
	recs := make([]interface{ Release() }, writers)
	werrs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		rec := buildSampleRecord(t, mem, time.Now().Add(time.Duration(i)*time.Millisecond).UnixMilli())
		recs[i] = rec
		wg.Add(1)
		go func() {
			defer wg.Done()
			werrs[i] = ing.Ingest(ctx, rec)
		}()
	}

	var ckptErr error
	var ckptWG sync.WaitGroup
	ckptWG.Add(1)
	go func() {
		defer ckptWG.Done()
		for range 5 {
			if err := client.Checkpoint(ctx); err != nil {
				ckptErr = err
				return
			}
		}
	}()

	wg.Wait()
	ckptWG.Wait()
	for _, r := range recs {
		r.Release()
	}

	for i, err := range werrs {
		require.NoError(t, err, "writer %d failed", i)
	}
	require.NoError(t, ckptErr, "checkpoint collided with an in-flight append")
	require.Equal(t, writers, countRows(t, client))
}

// A writer that has run out of time must fail, not queue.
//
// The write slot is taken with the caller's context on purpose. A plain mutex
// would ignore it: a burst of agents would sit in Lock() long past their own
// deadlines, each pinning its Arrow record alive, turning prompt failures into
// unbounded goroutine and heap growth.
func TestWriteHonoursItsDeadline(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)

	// Every write slot taken, so the ingest below cannot get one.
	release, err := client.LockWritesExclusive(context.Background())
	require.NoError(t, err)
	defer release()

	rec := buildSampleRecord(t, mem, time.Now().UnixMilli())
	defer rec.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = duckdb.NewIngester(log.NewNopLogger(), client).Ingest(ctx, rec)
	require.Error(t, err, "a write with no slot and no time left must fail")
	require.Less(t, time.Since(start), 5*time.Second, "the write ignored its deadline")
}

// Retention must checkpoint after it deletes, and say how long it took.
//
// The duration is the point. A checkpoint blocks writers on every connection
// for as long as it runs, and it was previously logged neither on success nor
// with a time, so an incident left nothing to point at: the logs showed the
// delete, then silence, then writes failing for reasons that named only the
// connection.
func TestRetentionLogsCheckpointDuration(t *testing.T) {
	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()

	old := time.Now().Add(-100 * 24 * time.Hour).UnixMilli()
	rec := buildSampleRecord(t, mem, old)
	require.NoError(t, duckdb.NewIngester(log.NewNopLogger(), client).Ingest(ctx, rec))
	rec.Release()
	require.Equal(t, 1, countRows(t, client))

	buf := &safeBuf{}
	logger := level.NewFilter(log.NewLogfmtLogger(buf), level.AllowDebug())

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		duckdb.RunRetention(runCtx, logger, client, time.Hour, time.Hour)
	}()
	require.Eventually(t, func() bool {
		return strings.Contains(buf.String(), "retention checkpoint")
	}, 10*time.Second, 20*time.Millisecond)
	cancel()
	<-done

	out := buf.String()
	require.Contains(t, out, "rows_deleted=1")
	require.Contains(t, out, "duration=")
	require.NotContains(t, out, "checkpoint failed")
	require.Equal(t, 0, countRows(t, client))
}

// safeBuf is a strings.Builder usable from two goroutines: RunRetention writes
// to it while the test reads. log.NewSyncWriter would not be enough -- it locks
// writes against each other, not against a concurrent String().
type safeBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
