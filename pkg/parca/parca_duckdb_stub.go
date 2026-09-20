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

//go:build !duckdb

package parca

import (
	"context"
	"errors"

	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/parca-dev/parca/pkg/profilestore"
	queryservice "github.com/parca-dev/parca/pkg/query"
	"github.com/parca-dev/parca/pkg/symbolizer"
)

// initDuckDBBackend is the stub used when Parca is built without the "duckdb"
// build tag. The DuckDB backend depends on cgo (github.com/marcboeker/go-duckdb)
// and is therefore excluded from the default CGO-free, cross-compiled release
// build. Use the DuckDB image (Dockerfile.duckdb, built with -tags duckdb) or
// build with `-tags duckdb` to enable --storage-backend=duckdb.
func initDuckDBBackend(
	_ context.Context,
	_ log.Logger,
	_ FlagsDuckDB,
	_ trace.TracerProvider,
	_ memory.Allocator,
	_ symbolizer.SymbolizationClient,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	return nil, nil, nil, errors.New("this Parca binary was built without the DuckDB storage backend; build with -tags duckdb (or use the ghcr.io/<owner>/parca:duckdb image) to enable --storage-backend=duckdb")
}
