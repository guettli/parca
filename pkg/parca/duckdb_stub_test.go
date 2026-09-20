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
	"testing"
)

// TestNewDuckDBBackendStub asserts that the default, CGO-free build refuses the
// DuckDB storage backend instead of pulling in go-duckdb (which cannot be
// cross-compiled with CGO disabled). The DuckDB backend is only available in
// builds tagged `duckdb` (see Dockerfile.duckdb).
func TestNewDuckDBBackendStub(t *testing.T) {
	ingester, querier, closeBackend, err := newDuckDBBackend(
		context.Background(), nil, nil, nil, nil, FlagsDuckDB{},
	)
	if err == nil {
		t.Fatal("expected an error from the DuckDB stub, got nil")
	}
	if ingester != nil || querier != nil || closeBackend != nil {
		t.Fatalf("expected nil backend components, got ingester=%v querier=%v closeBackend=%v", ingester, querier, closeBackend)
	}
}
