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

package duckdb

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/dennwc/varint"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	duckdb "github.com/marcboeker/go-duckdb/v2"

	"github.com/parca-dev/parca/pkg/profile"
)

// Ingester writes Arrow profile records to DuckDB via the Appender API.
type Ingester struct {
	logger log.Logger
	client *Client
}

// NewIngester returns an Ingester bound to client.
func NewIngester(logger log.Logger, client *Client) *Ingester {
	return &Ingester{logger: logger, client: client}
}

// Ingest writes record into the configured DuckDB table.
//
// Arrow → DuckDB row mapping:
//   - flat columns (name, sample_type, period, ...) → scalar Appender values
//   - labels.<name> Arrow columns → MAP(VARCHAR, VARCHAR) keyed by <name>
//   - stacktrace LIST<binary> (encoded location bytes) → LIST<STRUCT> by
//     decoding each binary blob into a location struct
func (i *Ingester) Ingest(ctx context.Context, record arrow.RecordBatch) error {
	if record.NumRows() == 0 {
		return nil
	}

	// Held for the whole append, so a CHECKPOINT cannot start underneath us
	// and fail on an open write transaction. Several appends may run at once;
	// see Client.LockWrites.
	release, err := i.client.LockWrites(ctx)
	if err != nil {
		return err
	}
	defer release()

	conn, err := i.client.DB().Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire duckdb connection: %w", err)
	}
	defer conn.Close()

	var appender *duckdb.Appender
	if rawErr := conn.Raw(func(driverConn any) error {
		dc, ok := driverConn.(driver.Conn)
		if !ok {
			return fmt.Errorf("duckdb raw connection is not a driver.Conn (got %T)", driverConn)
		}
		var aerr error
		appender, aerr = duckdb.NewAppenderFromConn(dc, "", i.client.Table())
		return aerr
	}); rawErr != nil {
		return fmt.Errorf("create appender: %w", rawErr)
	}
	defer appender.Close()

	schema := record.Schema()

	nameIdx := findColumnIndex(schema, profile.ColumnName)
	sampleTypeIdx := findColumnIndex(schema, profile.ColumnSampleType)
	sampleUnitIdx := findColumnIndex(schema, profile.ColumnSampleUnit)
	periodTypeIdx := findColumnIndex(schema, profile.ColumnPeriodType)
	periodUnitIdx := findColumnIndex(schema, profile.ColumnPeriodUnit)
	periodIdx := findColumnIndex(schema, profile.ColumnPeriod)
	durationIdx := findColumnIndex(schema, profile.ColumnDuration)
	timestampIdx := findColumnIndex(schema, profile.ColumnTimestamp)
	timeNanosIdx := findColumnIndex(schema, profile.ColumnTimeNanos)
	valueIdx := findColumnIndex(schema, profile.ColumnValue)
	stacktraceIdx := findColumnIndex(schema, profile.ColumnStacktrace)

	labelColumns := make(map[string]int)
	for idx, field := range schema.Fields() {
		if strings.HasPrefix(field.Name, profile.ColumnLabelsPrefix) {
			labelColumns[strings.TrimPrefix(field.Name, profile.ColumnLabelsPrefix)] = idx
		}
	}

	for row := 0; row < int(record.NumRows()); row++ {
		labels := make(duckdb.Map, len(labelColumns))
		for name, colIdx := range labelColumns {
			if v := getStringValue(record, colIdx, row); v != "" {
				labels[name] = v
			}
		}

		st := buildStacktraceList(record, stacktraceIdx, row)

		err := appender.AppendRow(
			getStringValue(record, nameIdx, row),
			getStringValue(record, sampleTypeIdx, row),
			getStringValue(record, sampleUnitIdx, row),
			getStringValue(record, periodTypeIdx, row),
			getStringValue(record, periodUnitIdx, row),
			getInt64Value(record, periodIdx, row),
			getInt64Value(record, durationIdx, row),
			getInt64Value(record, timestampIdx, row),
			getInt64Value(record, timeNanosIdx, row),
			getInt64Value(record, valueIdx, row),
			labels,
			st,
		)
		if err != nil {
			level.Error(i.logger).Log("msg", "duckdb appender row failed", "row", row, "err", err)
			return fmt.Errorf("append row %d: %w", row, err)
		}
	}

	if err := appender.Flush(); err != nil {
		return fmt.Errorf("flush appender: %w", err)
	}
	return nil
}

// buildStacktraceList decodes the encoded location blobs in record's
// stacktrace LIST column at row and produces the slice form the duckdb
// Appender expects for a LIST(STRUCT(...)) column.
func buildStacktraceList(record arrow.RecordBatch, colIdx, row int) []map[string]any {
	if colIdx < 0 {
		return nil
	}
	col := record.Column(colIdx)
	listCol, ok := col.(*array.List)
	if !ok || listCol.IsNull(row) {
		return nil
	}
	start, end := listCol.ValueOffsets(row)
	values := listCol.ListValues()
	dictCol, ok := values.(*array.Dictionary)
	if !ok {
		return nil
	}
	bin, ok := dictCol.Dictionary().(*array.Binary)
	if !ok {
		return nil
	}

	out := make([]map[string]any, 0, end-start)
	for idx := int(start); idx < int(end); idx++ {
		if dictCol.IsNull(idx) {
			continue
		}
		raw := bin.Value(dictCol.GetValueIndex(idx))
		sym, _ := profile.DecodeSymbolizationInfo(raw)
		line := decodeLineInfo(raw)
		out = append(out, map[string]any{
			StFieldAddress:            sym.Addr,
			StFieldMappingStart:       sym.Mapping.StartAddr,
			StFieldMappingLimit:       sym.Mapping.EndAddr,
			StFieldMappingOffset:      sym.Mapping.Offset,
			StFieldMappingFile:        sym.Mapping.File,
			StFieldMappingBuildID:     string(sym.BuildID),
			StFieldLineNumber:         line.LineNumber,
			StFieldFunctionName:       line.FunctionName,
			StFieldFunctionSystemName: line.FunctionSystemName,
			StFieldFunctionFilename:   line.FunctionFilename,
			StFieldFunctionStartLine:  line.FunctionStartLine,
		})
	}
	return out
}

// lineInfo mirrors the bits of the encoded location format we care about.
type lineInfo struct {
	LineNumber         int64
	FunctionStartLine  int64
	FunctionName       string
	FunctionSystemName string
	FunctionFilename   string
}

// decodeLineInfo decodes the line/function portion of a varint-encoded
// location record produced by the symbolizer.
// decodeLineInfo decodes the line/function portion of a varint-encoded
// location record produced by the symbolizer.
//
// Every read is bounds-checked and a short or malformed record returns what
// was decoded so far rather than panicking. The bytes are server-produced and
// therefore self-consistent in normal operation, so the realistic way to get a
// malformed one is encoder/decoder drift -- which is exactly what happened
// here, and which surfaced as a panic. There is no recovery interceptor on the
// ingest path, so that panic takes the process down, not the request.
func decodeLineInfo(data []byte) lineInfo {
	info := lineInfo{}
	offset := 0

	// uvarint reads one varint, reporting whether there was one to read.
	uvarint := func() (uint64, bool) {
		if offset >= len(data) {
			return 0, false
		}
		v, n := varint.Uvarint(data[offset:])
		if n <= 0 {
			return 0, false
		}
		offset += n
		return v, true
	}
	// str reads a length-prefixed string.
	str := func() (string, bool) {
		length, ok := uvarint()
		// Unsigned: a length larger than the record casts to a negative int,
		// so offset+int(length) can land BELOW offset and slip past a signed
		// check straight into a panicking slice. Compare against the bytes
		// that remain, in the same space the length was read in.
		if !ok || length > uint64(len(data)-offset) {
			return "", false
		}
		v := string(data[offset : offset+int(length)])
		offset += int(length)
		return v, true
	}
	// flag reads one byte.
	flag := func() (bool, bool) {
		if offset >= len(data) {
			return false, false
		}
		v := data[offset] == 0x1
		offset++
		return v, true
	}

	if _, ok := uvarint(); !ok { // address
		return info
	}
	numLines, ok := uvarint()
	if !ok {
		return info
	}
	hasMapping, ok := flag()
	if !ok {
		return info
	}
	if hasMapping {
		if _, ok := str(); !ok { // buildID
			return info
		}
		if _, ok := str(); !ok { // filename
			return info
		}
		// memoryStart, memoryLength, mappingOffset
		for range 3 {
			if _, ok := uvarint(); !ok {
				return info
			}
		}
	}

	if numLines == 0 {
		return info
	}
	// Only the first line. Location.line[0] is the innermost inlined function,
	// which is the right one to keep given a schema with a single function per
	// location -- but every inlined caller above it is dropped here, silently.
	ln, ok := uvarint()
	if !ok {
		return info
	}
	info.LineNumber = int64(ln)

	// The column. pprof carries none, so the encoder writes a uvarint zero --
	// a single 0x00 byte -- and this decoder used not to read it, taking that
	// byte for the hasFunction flag instead. It is false, so every function
	// name was discarded, and nothing failed while it happened: the address,
	// the mapping and the line number all decoded and the row was stored with
	// a nameless frame.
	//
	// Only profiles that arrive already symbolized are affected, which is
	// anything scraped from a Go /debug/pprof endpoint -- and those are the
	// ones the symbolizer cannot rescue afterwards, so the result read like
	// missing debuginfo rather than a decoder that could not parse what it had
	// been handed.
	if _, ok := uvarint(); !ok {
		return info
	}
	hasFunction, ok := flag()
	if !ok || !hasFunction {
		return info
	}
	startLine, ok := uvarint()
	if !ok {
		return info
	}
	info.FunctionStartLine = int64(startLine)
	if info.FunctionName, ok = str(); !ok {
		return info
	}
	if info.FunctionSystemName, ok = str(); !ok {
		return info
	}
	info.FunctionFilename, _ = str()
	return info
}

func findColumnIndex(schema *arrow.Schema, name string) int {
	idx := schema.FieldIndices(name)
	if len(idx) == 0 {
		return -1
	}
	return idx[0]
}

func getStringValue(record arrow.RecordBatch, colIdx, row int) string {
	if colIdx < 0 {
		return ""
	}
	col := record.Column(colIdx)
	if col.IsNull(row) {
		return ""
	}
	switch c := col.(type) {
	case *array.Dictionary:
		switch d := c.Dictionary().(type) {
		case *array.Binary:
			return string(d.Value(c.GetValueIndex(row)))
		case *array.String:
			return d.Value(c.GetValueIndex(row))
		}
	case *array.String:
		return c.Value(row)
	case *array.Binary:
		return string(c.Value(row))
	}
	return ""
}

func getInt64Value(record arrow.RecordBatch, colIdx, row int) int64 {
	if colIdx < 0 {
		return 0
	}
	col := record.Column(colIdx)
	if col.IsNull(row) {
		return 0
	}
	switch c := col.(type) {
	case *array.Int64:
		return c.Value(row)
	case *array.Dictionary:
		if d, ok := c.Dictionary().(*array.Int64); ok {
			return d.Value(c.GetValueIndex(row))
		}
	}
	return 0
}
