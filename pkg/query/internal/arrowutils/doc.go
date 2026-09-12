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

// Package arrowutils vendors the Arrow record sort/take/merge utilities
// from github.com/polarsignals/frostdb/pqarrow/arrowutils. These power
// the sort and merge step in the columnar query path.
//
// The merge_test.go and schema_test.go suites from upstream have been
// omitted because they depend on github.com/polarsignals/frostdb/internal/records,
// which is not importable from outside the frostdb module. sort_test.go
// is preserved.
package arrowutils
