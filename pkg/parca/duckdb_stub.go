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

	"github.com/go-kit/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/parca-dev/parca/pkg/profilestore"
	queryservice "github.com/parca-dev/parca/pkg/query"
	"github.com/parca-dev/parca/pkg/symbolizer"
)

// setupDuckDBBackend is the stub compiled into CGO-free release binaries, which
// are built without the "duckdb" build tag. The DuckDB backend links a static
// libduckdb via cgo and is only available in binaries built with `-tags duckdb`
// and CGO_ENABLED=1 (see Dockerfile.duckdb and docs/duckdb-image.md).
func setupDuckDBBackend(
	_ context.Context,
	_ log.Logger,
	_ trace.TracerProvider,
	_ *symbolizer.Symbolizer,
	_ *Flags,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	return nil, nil, nil, errors.New("this build of Parca was compiled without the DuckDB storage backend; rebuild with `-tags duckdb` and CGO_ENABLED=1, or use the ghcr.io/<owner>/parca:duckdb image")
}
