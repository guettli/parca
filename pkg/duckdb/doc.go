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

// Package duckdb implements the embedded DuckDB storage backend. It depends on
// github.com/marcboeker/go-duckdb, which links a static libduckdb via cgo, so
// the whole package is gated behind the "duckdb" build tag and requires
// CGO_ENABLED=1. The default (release) binary is built CGO-free without this
// tag; see Dockerfile.duckdb and docs/duckdb-image.md for the DuckDB build.
//
// This file carries no build tag so the package always has a Go file to
// declare it, even in builds that exclude the duckdb-tagged sources.
package duckdb
