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

// The DuckDB storage backend depends on github.com/marcboeker/go-duckdb, which
// links a static libduckdb through cgo. It therefore cannot be part of the
// default, CGO-free, cross-compiled release build. Build with `-tags duckdb`
// (and CGO_ENABLED=1) to include it; see Dockerfile.duckdb.

package parca

import (
	"context"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/parca-dev/parca/pkg/duckdb"
	"github.com/parca-dev/parca/pkg/profilestore"
	queryservice "github.com/parca-dev/parca/pkg/query"
	"github.com/parca-dev/parca/pkg/symbolizer"
)

func newDuckDBBackend(
	ctx context.Context,
	logger log.Logger,
	tracerProvider trace.TracerProvider,
	sharedSymbolizer *symbolizer.Symbolizer,
	flags FlagsDuckDB,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	ddClient, err := duckdb.NewClient(ctx, duckdb.Config{
		Path:  flags.Path,
		Table: flags.Table,
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

	return profileIngester, querier, ddClient.Close, nil
}
