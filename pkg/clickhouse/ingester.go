// Copyright 2024-2026 The Parca Authors
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

package clickhouse

import (
	"context"
	"fmt"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/dennwc/varint"
	"github.com/go-kit/log"
	"github.com/go-kit/log/level"

	"github.com/parca-dev/parca/pkg/profile"
)

// Ingester implements the ingester.Ingester interface for ClickHouse.
type Ingester struct {
	logger log.Logger
	client *Client
}

// NewIngester creates a new ClickHouse ingester.
func NewIngester(logger log.Logger, client *Client) *Ingester {
	return &Ingester{
		logger: logger,
		client: client,
	}
}

// Ingest implements the ingester.Ingester interface.
// It converts Arrow records to ClickHouse batch inserts.
func (i *Ingester) Ingest(ctx context.Context, record arrow.RecordBatch) error {
	if record.NumRows() == 0 {
		return nil
	}

	batch, err := i.client.PrepareBatch(ctx, InsertSQL(i.client.Database(), i.client.Table()))
	if err != nil {
		return fmt.Errorf("failed to prepare batch: %w", err)
	}

	schema := record.Schema()

	// Find column indices
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

	// Find label columns
	labelColumns := make(map[string]int)
	for idx, field := range schema.Fields() {
		if strings.HasPrefix(field.Name, profile.ColumnLabelsPrefix) {
			labelName := strings.TrimPrefix(field.Name, profile.ColumnLabelsPrefix)
			labelColumns[labelName] = idx
		}
	}

	for row := 0; row < int(record.NumRows()); row++ {
		// Extract profile metadata
		name := getStringValue(record, nameIdx, row)
		sampleType := getStringValue(record, sampleTypeIdx, row)
		sampleUnit := getStringValue(record, sampleUnitIdx, row)
		periodType := getStringValue(record, periodTypeIdx, row)
		periodUnit := getStringValue(record, periodUnitIdx, row)
		period := getInt64Value(record, periodIdx, row)
		duration := getInt64Value(record, durationIdx, row)
		timestamp := getInt64Value(record, timestampIdx, row)
		timeNanos := getInt64Value(record, timeNanosIdx, row)
		value := getInt64Value(record, valueIdx, row)

		// Extract labels as a map for JSON column
		labels := make(map[string]string)
		for labelName, colIdx := range labelColumns {
			if colIdx >= 0 {
				labelValue := getStringValue(record, colIdx, row)
				if labelValue != "" {
					labels[labelName] = labelValue
				}
			}
		}

		// Extract stacktrace data
		stacktraceData := extractStacktraceData(record, stacktraceIdx, row)

		// Append to batch
		err := batch.Append(
			name,
			sampleType,
			sampleUnit,
			periodType,
			periodUnit,
			period,
			duration,
			timestamp,
			timeNanos,
			value,
			labels,
			stacktraceData.Addresses,
			stacktraceData.MappingStarts,
			stacktraceData.MappingLimits,
			stacktraceData.MappingOffsets,
			stacktraceData.MappingFiles,
			stacktraceData.MappingBuildIDs,
			stacktraceData.LineNumbers,
			stacktraceData.FunctionNames,
			stacktraceData.FunctionSystemNames,
			stacktraceData.FunctionFilenames,
			stacktraceData.FunctionStartLines,
		)
		if err != nil {
			level.Error(i.logger).Log("msg", "failed to append row to batch", "err", err)
			return fmt.Errorf("failed to append row to batch: %w", err)
		}
	}

	if err := batch.Send(); err != nil {
		return fmt.Errorf("failed to send batch: %w", err)
	}

	return nil
}

// StacktraceData holds the extracted stacktrace information for a single sample.
type StacktraceData struct {
	Addresses           []uint64
	MappingStarts       []uint64
	MappingLimits       []uint64
	MappingOffsets      []uint64
	MappingFiles        []string
	MappingBuildIDs     []string
	LineNumbers         []int64
	FunctionNames       []string
	FunctionSystemNames []string
	FunctionFilenames   []string
	FunctionStartLines  []int64
}

// LineInfo holds decoded line/function information from an encoded location.
type LineInfo struct {
	LineNumber         int64
	FunctionStartLine  int64
	FunctionName       string
	FunctionSystemName string
	FunctionFilename   string
}

// decodeLineInfo decodes line and function information from the encoded location data.
// It returns the first line's info (most profiles have one line per location).
func decodeLineInfo(data []byte) LineInfo {
	info := LineInfo{}
	offset := 0

	// Every read is bounds-checked and a short or malformed record returns
	// what was decoded so far rather than panicking. The bytes are
	// server-produced and therefore self-consistent in normal operation, so
	// the realistic way to get a malformed one is encoder/decoder drift -- the
	// same thing that discarded every function name until the column read
	// below was added. There is no recovery interceptor on the ingest path, so
	// an unchecked panic here takes the process down, not the request. This
	// mirrors pkg/duckdb's decoder; keep the two in step.
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
	// a single 0x00 byte -- which a decoder that skips it reads as the
	// hasFunction flag, finds false, and so discards every function name.
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

// extractStacktraceData extracts stacktrace information from the encoded binary column.
// The stacktrace column contains encoded location data that needs to be decoded.
func extractStacktraceData(record arrow.RecordBatch, colIdx, row int) StacktraceData {
	data := StacktraceData{
		Addresses:           []uint64{},
		MappingStarts:       []uint64{},
		MappingLimits:       []uint64{},
		MappingOffsets:      []uint64{},
		MappingFiles:        []string{},
		MappingBuildIDs:     []string{},
		LineNumbers:         []int64{},
		FunctionNames:       []string{},
		FunctionSystemNames: []string{},
		FunctionFilenames:   []string{},
		FunctionStartLines:  []int64{},
	}

	if colIdx < 0 {
		return data
	}

	col := record.Column(colIdx)
	listCol, ok := col.(*array.List)
	if !ok {
		return data
	}

	if listCol.IsNull(row) {
		return data
	}

	start, end := listCol.ValueOffsets(row)
	values := listCol.ListValues()

	dictCol, ok := values.(*array.Dictionary)
	if !ok {
		return data
	}

	binaryDict, ok := dictCol.Dictionary().(*array.Binary)
	if !ok {
		return data
	}

	for idx := int(start); idx < int(end); idx++ {
		if dictCol.IsNull(idx) {
			continue
		}

		dictIdx := dictCol.GetValueIndex(idx)
		encodedLocation := binaryDict.Value(dictIdx)

		// Decode the mapping info
		symInfo, _ := profile.DecodeSymbolizationInfo(encodedLocation)

		data.Addresses = append(data.Addresses, symInfo.Addr)
		data.MappingStarts = append(data.MappingStarts, symInfo.Mapping.StartAddr)
		data.MappingLimits = append(data.MappingLimits, symInfo.Mapping.EndAddr)
		data.MappingOffsets = append(data.MappingOffsets, symInfo.Mapping.Offset)
		data.MappingFiles = append(data.MappingFiles, symInfo.Mapping.File)
		data.MappingBuildIDs = append(data.MappingBuildIDs, string(symInfo.BuildID))

		// Decode line/function info
		lineInfo := decodeLineInfo(encodedLocation)
		data.LineNumbers = append(data.LineNumbers, lineInfo.LineNumber)
		data.FunctionNames = append(data.FunctionNames, lineInfo.FunctionName)
		data.FunctionSystemNames = append(data.FunctionSystemNames, lineInfo.FunctionSystemName)
		data.FunctionFilenames = append(data.FunctionFilenames, lineInfo.FunctionFilename)
		data.FunctionStartLines = append(data.FunctionStartLines, lineInfo.FunctionStartLine)
	}

	return data
}

func findColumnIndex(schema *arrow.Schema, name string) int {
	indices := schema.FieldIndices(name)
	if len(indices) == 0 {
		return -1
	}
	return indices[0]
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
		switch dict := c.Dictionary().(type) {
		case *array.Binary:
			return string(dict.Value(c.GetValueIndex(row)))
		case *array.String:
			return dict.Value(c.GetValueIndex(row))
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
		switch dict := c.Dictionary().(type) {
		case *array.Int64:
			return dict.Value(c.GetValueIndex(row))
		}
	}

	return 0
}
