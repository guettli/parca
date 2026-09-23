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

// newDuckDBBackend is the stub used by the default, CGO-free cross-platform
// release binary. The DuckDB backend depends on go-duckdb, which links a static
// libduckdb through cgo and therefore cannot be part of the CGO-free build. Use
// the image built from Dockerfile.duckdb (compiled with `-tags duckdb`) to run
// with `--storage-backend=duckdb`.
func newDuckDBBackend(
	_ context.Context,
	_ log.Logger,
	_ trace.TracerProvider,
	_ memory.Allocator,
	_ *symbolizer.Symbolizer,
	_ FlagsDuckDB,
) (profilestore.Ingester, queryservice.Querier, func() error, error) {
	return nil, nil, nil, errors.New("this build of parca was compiled without the DuckDB storage backend; use the DuckDB image (built with -tags duckdb) to use --storage-backend=duckdb")
}
