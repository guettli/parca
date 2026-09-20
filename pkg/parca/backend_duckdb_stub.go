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

//go:build !cgo

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

// setupDuckDBBackend is the CGO-free fallback. The DuckDB backend links
// libduckdb through cgo (see backend_duckdb.go), so it is unavailable in
// binaries built with CGO_ENABLED=0 (for example the cross-platform goreleaser
// release artifacts). Use the ClickHouse backend or a cgo-enabled build.
func setupDuckDBBackend(
	_ context.Context,
	_ log.Logger,
	_ *Flags,
	_ trace.TracerProvider,
	_ symbolizer.SymbolizationClient,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	return nil, nil, nil, errors.New("the duckdb storage backend is not available in this build: rebuild with CGO_ENABLED=1 to enable it")
}
