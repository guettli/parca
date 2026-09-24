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

package normalizer

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	pprofpb "github.com/parca-dev/parca/gen/proto/go/google/pprof"
	"github.com/parca-dev/parca/pkg/profile"
)

func dedupTestFixture() (locations []*pprofpb.Location, functions []*pprofpb.Function, stringTable []string) {
	stringTable = []string{"", "main.run", "main.go", "svc.handle", "svc.go"}
	functions = []*pprofpb.Function{
		{Id: 1, Name: 1, SystemName: 1, Filename: 2},
		{Id: 2, Name: 3, SystemName: 3, Filename: 4},
	}
	locations = []*pprofpb.Location{
		{Id: 1, Line: []*pprofpb.Line{{Line: 10, FunctionId: 1}}},
		{Id: 2, Line: []*pprofpb.Line{{Line: 20, FunctionId: 2}}},
	}
	return locations, functions, stringTable
}

// A location is encoded once per distinct id and the slice is shared across every
// stacktrace that references it, so the live [][]byte holds one slice per distinct
// location rather than one per occurrence (guettli/parca#110). The bytes must
// still be exactly what EncodePprofLocation produces.
func TestSerializePprofStacktraceMemoizesLocations(t *testing.T) {
	locations, functions, stringTable := dedupTestFixture()
	cache := map[uint64][]byte{}

	// Two stacktraces that both reference location 1.
	st1 := serializePprofStacktrace([]uint64{1, 2}, locations, functions, nil, stringTable, cache)
	st2 := serializePprofStacktrace([]uint64{2, 1}, locations, functions, nil, stringTable, cache)

	require.Len(t, cache, 2, "each distinct location must be encoded exactly once")

	// Location 1 appears in both stacktraces and must be the very same backing
	// slice, not a fresh allocation.
	require.Equal(t, fmt.Sprintf("%p", st1[0]), fmt.Sprintf("%p", st2[1]),
		"a repeated location must reuse the cached encoded slice")
	require.Equal(t, fmt.Sprintf("%p", st1[1]), fmt.Sprintf("%p", st2[0]),
		"a repeated location must reuse the cached encoded slice")

	// The cached bytes are exactly the canonical encoding.
	want := profile.EncodePprofLocation(locations[0], nil, functions, stringTable)
	require.Equal(t, want, st1[0], "memoized encoding must match EncodePprofLocation")
}

// BenchmarkSerializePprofStacktrace models a profile whose samples repeatedly
// reference a small pool of locations (a hot frame recurs across stacktraces).
// With the per-profile cache, allocations are proportional to the number of
// distinct locations rather than the number of occurrences -- run with -benchmem
// to see allocs/op stay flat as the sample count grows.
func BenchmarkSerializePprofStacktrace(b *testing.B) {
	const distinct = 64
	stringTable := []string{""}
	functions := make([]*pprofpb.Function, distinct)
	locations := make([]*pprofpb.Location, distinct)
	for i := 0; i < distinct; i++ {
		name := len(stringTable)
		stringTable = append(stringTable, fmt.Sprintf("func_%d", i))
		functions[i] = &pprofpb.Function{Id: uint64(i + 1), Name: int64(name), SystemName: int64(name), Filename: 0}
		locations[i] = &pprofpb.Location{Id: uint64(i + 1), Line: []*pprofpb.Line{{Line: int64(i), FunctionId: uint64(i + 1)}}}
	}
	// 200 stacktraces, each 20 frames deep, drawn from the pool -> heavy reuse.
	stacks := make([][]uint64, 200)
	for s := range stacks {
		ids := make([]uint64, 20)
		for k := range ids {
			ids[k] = uint64((s+k)%distinct) + 1
		}
		stacks[s] = ids
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache := make(map[uint64][]byte, distinct)
		for _, ids := range stacks {
			_ = serializePprofStacktrace(ids, locations, functions, nil, stringTable, cache)
		}
	}
}
