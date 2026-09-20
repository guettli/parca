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
	"errors"

	"github.com/go-kit/log"
	"go.opentelemetry.io/otel/trace"

	"github.com/parca-dev/parca/pkg/profilestore"
	queryservice "github.com/parca-dev/parca/pkg/query"
	"github.com/parca-dev/parca/pkg/symbolizer"
)

// newDuckDBBackend is a stub for builds without the `duckdb` build tag. The
// DuckDB storage backend links libduckdb via cgo and is only available in
// binaries built with `-tags duckdb` (see Dockerfile.duckdb); the default
// release build is CGO-free, so selecting it here is a configuration error.
func newDuckDBBackend(
	_ context.Context,
	_ log.Logger,
	_ trace.TracerProvider,
	_ FlagsDuckDB,
	_ *symbolizer.Symbolizer,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	return nil, nil, nil, errors.New(`storage backend "duckdb" is not available in this build; rebuild with -tags duckdb (see Dockerfile.duckdb)`)
}
