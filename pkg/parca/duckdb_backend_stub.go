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

// This is the stub used by the default, CGO-free, cross-compiled release build.
// The DuckDB backend requires cgo (github.com/marcboeker/go-duckdb) and is only
// compiled in with `-tags duckdb`; see duckdb_backend.go and Dockerfile.duckdb.

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

func newDuckDBBackend(
	_ context.Context,
	_ log.Logger,
	_ trace.TracerProvider,
	_ *symbolizer.Symbolizer,
	_ FlagsDuckDB,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	return nil, nil, nil, errors.New("this binary was built without DuckDB support; rebuild with -tags duckdb (requires CGO). See Dockerfile.duckdb")
}
