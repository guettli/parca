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

// Package builder vendors the optimized Arrow array builders from
// github.com/polarsignals/frostdb/pqarrow/builder. These builders expose
// random-access mutation methods (Set/Add/Value/AppendData) that the
// flamegraph and table query algorithms rely on, which the upstream
// arrow-go array.Builder API does not provide.
//
// AppendParquetValues methods and the parquet-go dependency from the
// upstream package have been dropped — parca uses these builders only on
// the query side and never to convert parquet values.
package builder
