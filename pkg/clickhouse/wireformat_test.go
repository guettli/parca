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

package clickhouse

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parca-dev/parca/pkg/profile/wiretest"
)

// This decoder must agree with the canonical encoder, and with the ClickHouse
// decoder, on the shared corpus. The clickhouse package runs the identical
// assertions against its own decodeLineInfo (wireformat_test.go there), so a
// drift in either -- a field read in the wrong order, a skipped column, a
// mishandled null -- fails a test here rather than a production ingest. This is
// the executable version of "keep the two in step" that used to be discipline
// alone.
func TestDecodeLineInfoMatchesTheCorpus(t *testing.T) {
	for _, c := range wiretest.Corpus() {
		t.Run(c.Name, func(t *testing.T) {
			got := decodeLineInfo(c.Encoded)

			if len(c.Lines) == 0 {
				// No lines: the decoder returns a zero lineInfo.
				require.Empty(t, got.FunctionName)
				require.Zero(t, got.LineNumber)
				return
			}

			// decodeLineInfo keeps only the innermost line (c.Lines[0]) and
			// represents "no function" as empty strings.
			want := c.Lines[0]
			require.Equal(t, want.Number, got.LineNumber, "line number")
			if want.HasFunction {
				require.Equal(t, want.Name, got.FunctionName, "function name")
				require.Equal(t, want.SystemName, got.FunctionSystemName, "system name")
				require.Equal(t, want.Filename, got.FunctionFilename, "filename")
				require.Equal(t, want.StartLine, got.FunctionStartLine, "start line")
			} else {
				require.Empty(t, got.FunctionName, "a line with no function must decode to an empty name")
			}
		})
	}
}

// FuzzDecodeLineInfo asserts the decoder never panics, whatever bytes it is
// handed. The records come from an agent, the ingest path has no recovery
// interceptor, and encoder/decoder drift produced exactly the short/oversized
// inputs that crashed it before (#107/#111). Seeded with the real corpus and
// every truncation of it so the fuzzer starts from valid shapes.
func FuzzDecodeLineInfo(f *testing.F) {
	for _, c := range wiretest.Corpus() {
		for n := 0; n <= len(c.Encoded); n++ {
			f.Add(c.Encoded[:n])
		}
	}
	for _, b := range wiretest.Adversarial() {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = decodeLineInfo(data) // must not panic
	})
}
