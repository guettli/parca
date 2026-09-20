// Copyright 2022-2026 The Parca Authors
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

package parca

import (
	"context"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"go.opentelemetry.io/otel/trace"

	"github.com/parca-dev/parca/pkg/duckdb"
	"github.com/parca-dev/parca/pkg/profilestore"
	queryservice "github.com/parca-dev/parca/pkg/query"
	"github.com/parca-dev/parca/pkg/symbolizer"
)

// setupDuckDBBackend initializes the embedded DuckDB storage backend. It is
// only compiled into binaries built with the "duckdb" build tag (which also
// requires CGO_ENABLED=1 because go-duckdb links a static libduckdb via cgo);
// the CGO-free variant in duckdb_stub.go returns an error instead.
func setupDuckDBBackend(
	ctx context.Context,
	logger log.Logger,
	tracerProvider trace.TracerProvider,
	sharedSymbolizer *symbolizer.Symbolizer,
	flags *Flags,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	level.Info(logger).Log("msg", "initializing DuckDB storage backend", "path", duckdbPathDescription(flags.DuckDB.Path))

	ddClient, err := duckdb.NewClient(ctx, duckdb.Config{
		Path:        flags.DuckDB.Path,
		Table:       flags.DuckDB.Table,
		MemoryLimit: flags.DuckDB.MemoryLimit,
	})
	if err != nil {
		level.Error(logger).Log("msg", "failed to open DuckDB", "err", err)
		return nil, nil, nil, fmt.Errorf("failed to open DuckDB: %w", err)
	}
	if err := ddClient.EnsureSchema(ctx); err != nil {
		ddClient.Close()
		level.Error(logger).Log("msg", "failed to ensure DuckDB schema", "err", err)
		return nil, nil, nil, fmt.Errorf("failed to ensure DuckDB schema: %w", err)
	}

	profileIngester := duckdb.NewIngester(logger, ddClient)
	querier := duckdb.NewQuerier(
		ddClient,
		logger,
		tracerProvider.Tracer("duckdb-querier"),
		memory.DefaultAllocator,
		sharedSymbolizer,
	)
	closeBackend := ddClient.Close

	// Time-based retention: periodically delete rows older than the configured
	// age so the DuckDB file's growth is bounded. Cancelled via closeBackend on
	// shutdown.
	if flags.DuckDB.Retention > 0 {
		level.Info(logger).Log("msg", "enabling DuckDB retention", "retention", flags.DuckDB.Retention.String(), "interval", flags.DuckDB.RetentionInterval.String())
		retentionCtx, cancelRetention := context.WithCancel(ctx)
		retentionDone := make(chan struct{})
		go func() {
			defer close(retentionDone)
			duckdb.RunRetention(retentionCtx, logger, ddClient, flags.DuckDB.Retention, flags.DuckDB.RetentionInterval)
		}()
		closeBackend = func() error {
			cancelRetention()
			<-retentionDone // wait for an in-flight pass before closing the DB
			return ddClient.Close()
		}
	}

	return profileIngester, querier, closeBackend, nil
}

func duckdbPathDescription(path string) string {
	if path == "" {
		return "in-memory (volatile)"
	}
	return path
}
