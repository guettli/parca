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

// The DuckDB storage backend depends on github.com/marcboeker/go-duckdb, which
// links a static libduckdb through cgo. It therefore MUST be built with
// CGO_ENABLED=1 and the "duckdb" build tag and cannot be part of the CGO-free,
// cross-platform goreleaser release binaries. Binaries built without the tag
// use storage_noduckdb.go, which returns an explanatory error at runtime.
// See Dockerfile.duckdb and docs/duckdb-image.md.

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

// newDuckDBBackend opens the embedded DuckDB storage backend and returns its
// ingester, querier and a close function that also tears down any background
// retention loop.
func newDuckDBBackend(
	ctx context.Context,
	logger log.Logger,
	tracerProvider trace.TracerProvider,
	sharedSymbolizer *symbolizer.Symbolizer,
	flags FlagsDuckDB,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	level.Info(logger).Log("msg", "initializing DuckDB storage backend", "path", duckdbPathDescription(flags.Path))

	ddClient, err := duckdb.NewClient(ctx, duckdb.Config{
		Path:        flags.Path,
		Table:       flags.Table,
		MemoryLimit: flags.MemoryLimit,
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open DuckDB: %w", err)
	}
	if err := ddClient.EnsureSchema(ctx); err != nil {
		ddClient.Close()
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
	if flags.Retention > 0 {
		level.Info(logger).Log("msg", "enabling DuckDB retention", "retention", flags.Retention.String(), "interval", flags.RetentionInterval.String())
		retentionCtx, cancelRetention := context.WithCancel(ctx)
		retentionDone := make(chan struct{})
		go func() {
			defer close(retentionDone)
			duckdb.RunRetention(retentionCtx, logger, ddClient, flags.Retention, flags.RetentionInterval)
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
