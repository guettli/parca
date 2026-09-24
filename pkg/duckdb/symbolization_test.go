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

//go:build duckdb

package duckdb_test

import (
	"context"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/go-kit/log"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	metapb "github.com/parca-dev/parca/gen/proto/go/parca/metastore/v1alpha1"
	"github.com/parca-dev/parca/pkg/duckdb"
	"github.com/parca-dev/parca/pkg/profile"
	"github.com/parca-dev/parca/pkg/symbolizer"
)

// A query must emit function names, from either source.
//
// Nothing in the suite checked this before, which is how a server that
// answered [unsymbolized] for 100% of its samples passed every test: the rows
// scanned cleanly, the query returned records, the record count was right, and
// the only thing wrong was that every name inside was empty.
//
// Both arms are covered because they fail independently. The stored arm breaks
// when the row does not decode (the Composite scanner reads `mapstructure`
// tags, so a `db` tag leaves every field but Address zero). The symbolized arm
// breaks when the writer stops reading what the symbolizer produced.
func TestQueryEmitsFunctionNames(t *testing.T) {
	const tsMillis int64 = 1_700_000_000_000

	t.Run("names already in the table", func(t *testing.T) {
		names := queryFunctionNames(t, tsMillis, nopSymbolizer{}, func(mem memory.Allocator) arrow.RecordBatch {
			return buildSampleRecord(t, mem, tsMillis)
		})
		require.Contains(t, names, "main.run",
			"the name stored with the location never reached the record")
	})

	t.Run("names from the symbolizer", func(t *testing.T) {
		// A location with no lines: the querier offers it to the symbolizer,
		// which fills in the Lines slice it was handed.
		names := queryFunctionNames(t, tsMillis, &fakeSymbolizer{name: "main.symbolized"},
			func(mem memory.Allocator) arrow.RecordBatch {
				return buildUnsymbolizedRecord(t, mem, tsMillis)
			})
		require.Contains(t, names, "main.symbolized",
			"the symbolizer's output never reached the record")
	})

	// A v2-ingested profile stores its symbol in the system name with the
	// function name empty (normalizer.encodeV2Location), and carries no mapping
	// build ID, so the querier never offers it to the symbolizer. Before the
	// fallback the writer's `FunctionName != ""` arm was false and the frame was
	// dropped as [unsymbolized] -- the symbol sat one column over, in the row but
	// not in the answer. The symbolizer here would rename any location it were
	// handed, so a result of "v2.only.systemname" also proves this frame took the
	// stored arm, not symbolization.
	t.Run("name from the system name (v2 profiles)", func(t *testing.T) {
		names := queryFunctionNames(t, tsMillis, &fakeSymbolizer{name: "should.not.be.used"},
			func(mem memory.Allocator) arrow.RecordBatch {
				return buildRecordWithLocation(t, mem, tsMillis, encodeV2ShapedLocation(0xf00d, "", "v2.only.systemname"))
			})
		require.Contains(t, names, "v2.only.systemname",
			"a v2 profile's symbol, stored in the system name, never reached the record")
	})

	// The system-name fallback must not shadow debuginfo. A v2 frame that has a
	// build ID is still offered to the symbolizer, whose richer output (demangled
	// names, inlined frames) wins; the stored system name is used only when
	// symbolization returns nothing. So this frame -- system name set, empty name,
	// AND a build ID -- must render the symbolizer's name, not "v2.raw.symbol".
	t.Run("debuginfo still wins over the system name when a build ID is present", func(t *testing.T) {
		names := queryFunctionNames(t, tsMillis, &fakeSymbolizer{name: "main.fromDebuginfo"},
			func(mem memory.Allocator) arrow.RecordBatch {
				return buildRecordWithLocation(t, mem, tsMillis, encodeV2ShapedLocation(0xf00d, "build-id-xyz", "v2.raw.symbol"))
			})
		require.Contains(t, names, "main.fromDebuginfo",
			"a build-ID frame must still be symbolized by debuginfo")
		require.NotContains(t, names, "v2.raw.symbol",
			"the raw system name must not shadow the richer debuginfo symbol")
	})
}

// fakeSymbolizer answers every request by naming each location it was given.
// It writes into the Locations slice in place, which is the contract the real
// symbolizer follows.
type fakeSymbolizer struct{ name string }

func (f *fakeSymbolizer) Symbolize(_ context.Context, req symbolizer.SymbolizationRequest) error {
	for _, m := range req.Mappings {
		for _, loc := range m.Locations {
			loc.Lines = []profile.LocationLine{{
				Line:     42,
				Function: &metapb.Function{Name: f.name},
			}}
		}
	}
	return nil
}

// queryFunctionNames ingests one record, runs a merge over it, and returns
// every function name in the result.
func queryFunctionNames(t *testing.T, tsMillis int64, sym symbolizer.SymbolizationClient, build func(memory.Allocator) arrow.RecordBatch) []string {
	t.Helper()

	mem := memory.NewCheckedAllocator(memory.DefaultAllocator)
	defer mem.AssertSize(t, 0)

	client := newTestClient(t)
	ctx := context.Background()
	q := duckdb.NewQuerier(client, log.NewNopLogger(), noop.NewTracerProvider().Tracer(""), mem, sym)

	rec := build(mem)
	defer rec.Release()
	require.NoError(t, duckdb.NewIngester(log.NewNopLogger(), client, nil).Ingest(ctx, rec))

	p, err := q.QueryMerge(ctx,
		`process_cpu:cpu:nanoseconds:cpu:nanoseconds:delta{job="test"}`,
		time.UnixMilli(tsMillis-1_000), time.UnixMilli(tsMillis+1_000),
		[]string{"job"}, false, "")
	require.NoError(t, err)
	defer func() {
		for _, r := range p.Samples {
			r.Release()
		}
	}()

	var names []string
	for _, r := range p.Samples {
		rr, err := profile.NewRecordReader(r)
		require.NoError(t, err)
		for i := 0; i < rr.LineFunctionNameIndices.Len(); i++ {
			if rr.LineFunctionNameIndices.IsNull(i) {
				continue
			}
			names = append(names, string(rr.LineFunctionNameDict.Value(int(rr.LineFunctionNameIndices.Value(i)))))
		}
	}
	return names
}

// buildUnsymbolizedRecord is buildSampleRecord with a location that carries a
// mapping but no lines -- the shape that reaches the symbolizer.
func buildUnsymbolizedRecord(t *testing.T, mem memory.Allocator, ts int64) arrow.RecordBatch {
	t.Helper()

	schema := profile.BuildArrowSchema([]string{"job"})
	b := array.NewRecordBuilder(mem, schema)
	defer b.Release()

	for i, field := range schema.Fields() {
		switch field.Name {
		case profile.ColumnDuration:
			b.Field(i).(*array.Int64Builder).Append(int64(time.Second))
		case profile.ColumnName:
			require.NoError(t, b.Field(i).(*array.BinaryDictionaryBuilder).AppendString("process_cpu"))
		case profile.ColumnPeriod:
			b.Field(i).(*array.Int64Builder).Append(10_000_000)
		case profile.ColumnPeriodType, profile.ColumnSampleType:
			require.NoError(t, b.Field(i).(*array.BinaryDictionaryBuilder).AppendString("cpu"))
		case profile.ColumnPeriodUnit, profile.ColumnSampleUnit:
			require.NoError(t, b.Field(i).(*array.BinaryDictionaryBuilder).AppendString("nanoseconds"))
		case profile.ColumnStacktrace:
			lb := b.Field(i).(*array.ListBuilder)
			vb := lb.ValueBuilder().(*array.BinaryDictionaryBuilder)
			lb.Append(true)
			require.NoError(t, vb.Append(encodeBareLocation(0xcafe, "test-build-id", "/lib/test")))
		case profile.ColumnTimestamp:
			b.Field(i).(*array.Int64Builder).Append(ts)
		case profile.ColumnTimeNanos:
			b.Field(i).(*array.Int64Builder).Append(ts * int64(time.Millisecond))
		case profile.ColumnValue:
			b.Field(i).(*array.Int64Builder).Append(42)
		case profile.ColumnLabelsPrefix + "job":
			require.NoError(t, b.Field(i).(*array.BinaryDictionaryBuilder).AppendString("test"))
		}
	}
	return b.NewRecordBatch()
}

// buildRecordWithLocation is buildSampleRecord with a caller-supplied encoded
// location, so a test can pin the exact stored shape it cares about.
func buildRecordWithLocation(t *testing.T, mem memory.Allocator, ts int64, loc []byte) arrow.RecordBatch {
	t.Helper()

	schema := profile.BuildArrowSchema([]string{"job"})
	b := array.NewRecordBuilder(mem, schema)
	defer b.Release()

	for i, field := range schema.Fields() {
		switch field.Name {
		case profile.ColumnDuration:
			b.Field(i).(*array.Int64Builder).Append(int64(time.Second))
		case profile.ColumnName:
			require.NoError(t, b.Field(i).(*array.BinaryDictionaryBuilder).AppendString("process_cpu"))
		case profile.ColumnPeriod:
			b.Field(i).(*array.Int64Builder).Append(10_000_000)
		case profile.ColumnPeriodType, profile.ColumnSampleType:
			require.NoError(t, b.Field(i).(*array.BinaryDictionaryBuilder).AppendString("cpu"))
		case profile.ColumnPeriodUnit, profile.ColumnSampleUnit:
			require.NoError(t, b.Field(i).(*array.BinaryDictionaryBuilder).AppendString("nanoseconds"))
		case profile.ColumnStacktrace:
			lb := b.Field(i).(*array.ListBuilder)
			vb := lb.ValueBuilder().(*array.BinaryDictionaryBuilder)
			lb.Append(true)
			require.NoError(t, vb.Append(loc))
		case profile.ColumnTimestamp:
			b.Field(i).(*array.Int64Builder).Append(ts)
		case profile.ColumnTimeNanos:
			b.Field(i).(*array.Int64Builder).Append(ts * int64(time.Millisecond))
		case profile.ColumnValue:
			b.Field(i).(*array.Int64Builder).Append(42)
		case profile.ColumnLabelsPrefix + "job":
			require.NoError(t, b.Field(i).(*array.BinaryDictionaryBuilder).AppendString("test"))
		}
	}
	return b.NewRecordBatch()
}

// encodeV2ShapedLocation encodes one line whose function has an empty name and a
// non-empty system name -- the shape normalizer.encodeV2Location produces (the
// symbol is written as the system name; the name is ""). An empty buildID emits
// no mapping (the frame is never offered to the symbolizer); a non-empty buildID
// emits a mapping (the frame is offered, and debuginfo is preferred when found).
func encodeV2ShapedLocation(addr uint64, buildID, systemName string) []byte {
	var out []byte
	out = appendUvarint(out, addr)
	out = appendUvarint(out, 1) // 1 line
	if buildID == "" {
		out = append(out, 0x00) // no mapping
	} else {
		out = append(out, 0x01) // hasMapping
		out = appendBytes(out, []byte(buildID))
		out = appendBytes(out, []byte("/lib/test"))
		out = appendUvarint(out, 0) // memoryStart
		out = appendUvarint(out, 0) // memoryLength
		out = appendUvarint(out, 0) // mappingOffset
	}
	out = appendUvarint(out, 7)                // line number
	out = appendUvarint(out, 0)                // column
	out = append(out, 0x01)                    // hasFunction
	out = appendUvarint(out, 1)                // startLine
	out = appendBytes(out, []byte(""))         // name: empty, as v2 stores it
	out = appendBytes(out, []byte(systemName)) // system name: the real symbol
	out = appendBytes(out, []byte("main.go"))  // filename
	return out
}

// encodeBareLocation is encodeLocation with zero lines: an address inside a
// known mapping and nothing else, which is what an unsymbolized sample is.
func encodeBareLocation(addr uint64, buildID, mappingFile string) []byte {
	var out []byte
	out = appendUvarint(out, addr)
	out = appendUvarint(out, 0) // no lines
	out = append(out, 0x01)     // hasMapping
	out = appendBytes(out, []byte(buildID))
	out = appendBytes(out, []byte(mappingFile))
	out = appendUvarint(out, 0) // memoryStart
	out = appendUvarint(out, 0) // memoryLength
	out = appendUvarint(out, 0) // mappingOffset
	return out
}
