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

// Package duckdb implements the embedded, single-node DuckDB storage backend.
//
// The backend links a static libduckdb through cgo
// (github.com/marcboeker/go-duckdb) and is therefore only compiled into
// binaries built with the `duckdb` build tag (see Dockerfile.duckdb). Without
// that tag every other file in this package is excluded by build constraints,
// leaving only this doc so the package still resolves in the default CGO-free
// build.
package duckdb
