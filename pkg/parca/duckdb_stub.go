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

//go:build !duckdb

package parca

import (
	"context"
	"fmt"

	"github.com/go-kit/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/parca-dev/parca/pkg/profilestore"
	queryservice "github.com/parca-dev/parca/pkg/query"
	"github.com/parca-dev/parca/pkg/symbolizer"
)

// setupDuckDB is the stub used when the binary is built without the `duckdb`
// build tag. The DuckDB backend links a static libduckdb through cgo, so it is
// excluded from the default (CGO-free, multi-arch) release builds. Rebuild with
// -tags duckdb — and CGO_ENABLED=1 — to include it (see Dockerfile.duckdb).
func setupDuckDB(
	context.Context,
	log.Logger,
	FlagsDuckDB,
	trace.TracerProvider,
	*symbolizer.Symbolizer,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	return nil, nil, nil, fmt.Errorf("this build does not include the DuckDB storage backend; rebuild with -tags duckdb (see Dockerfile.duckdb) or use --storage-backend=clickhouse")
}
