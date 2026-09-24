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

package query

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/parca-dev/parca/gen/proto/go/parca/query/v1alpha1"
)

// recordingQuerier captures the time range ProfileTypes was called with. It
// embeds nopQuerier so it satisfies the whole Querier interface.
type recordingQuerier struct {
	nopQuerier
	gotStart, gotEnd time.Time
	called           bool
}

func (r *recordingQuerier) ProfileTypes(_ context.Context, start, end time.Time) ([]*pb.ProfileType, error) {
	r.gotStart, r.gotEnd, r.called = start, end, true
	return nil, nil
}

// A ProfileTypesRequest with no time range must not reach the querier as a zero
// range: both backends treat that as "all of history" and run an unbounded
// SELECT DISTINCT over the whole table (guettli/parca#119). The UI sends a
// range-less request on some paths, so the handler bounds it to a lookback
// instead of scanning everything (or rejecting).
func TestProfileTypesBoundsUnsetRange(t *testing.T) {
	t.Run("unset range is bounded to a lookback, not all of history", func(t *testing.T) {
		rec := &recordingQuerier{}
		api := &ColumnQueryAPI{querier: rec}

		before := time.Now()
		_, err := api.ProfileTypes(context.Background(), &pb.ProfileTypesRequest{})
		require.NoError(t, err)
		require.True(t, rec.called)

		// The querier must not receive the zero range that triggers a full scan.
		require.NotZero(t, rec.gotStart.Unix(), "start still zero -> querier full-scans the table")
		require.NotZero(t, rec.gotEnd.Unix(), "end still zero -> querier full-scans the table")

		// End is ~now and the window is exactly one lookback wide.
		require.WithinDuration(t, before, rec.gotEnd, 5*time.Second)
		require.Equal(t, defaultProfileTypesLookback, rec.gotEnd.Sub(rec.gotStart))
	})

	// A one-sided range (only Start, or only End) is the trap: the backends skip
	// their filter whenever EITHER bound is zero, so a request with a single bound
	// -- or an epoch bound -- still full-scans if the handler only guards the
	// both-zero case. Both must be bounded.
	t.Run("only start set is still bounded", func(t *testing.T) {
		rec := &recordingQuerier{}
		api := &ColumnQueryAPI{querier: rec}

		start := time.Unix(1_700_000_000, 0).UTC()
		before := time.Now()
		_, err := api.ProfileTypes(context.Background(), &pb.ProfileTypesRequest{
			Start: timestamppb.New(start),
			// End unset -> End.Unix() == 0 -> backend would skip the filter.
		})
		require.NoError(t, err)
		require.NotZero(t, rec.gotStart.Unix(), "start still zero -> querier full-scans")
		require.NotZero(t, rec.gotEnd.Unix(), "end still zero -> querier full-scans")
		require.WithinDuration(t, before, rec.gotEnd, 5*time.Second)
		require.Equal(t, defaultProfileTypesLookback, rec.gotEnd.Sub(rec.gotStart))
	})

	t.Run("only end set is still bounded", func(t *testing.T) {
		rec := &recordingQuerier{}
		api := &ColumnQueryAPI{querier: rec}

		end := time.Unix(1_700_003_600, 0).UTC()
		before := time.Now()
		_, err := api.ProfileTypes(context.Background(), &pb.ProfileTypesRequest{
			End: timestamppb.New(end),
			// Start unset -> Start.Unix() == 0 -> backend would skip the filter.
		})
		require.NoError(t, err)
		require.NotZero(t, rec.gotStart.Unix(), "start still zero -> querier full-scans")
		require.NotZero(t, rec.gotEnd.Unix(), "end still zero -> querier full-scans")
		require.WithinDuration(t, before, rec.gotEnd, 5*time.Second)
		require.Equal(t, defaultProfileTypesLookback, rec.gotEnd.Sub(rec.gotStart))
	})

	t.Run("an explicit range is passed through unchanged", func(t *testing.T) {
		rec := &recordingQuerier{}
		api := &ColumnQueryAPI{querier: rec}

		start := time.Unix(1_700_000_000, 0).UTC()
		end := start.Add(time.Hour)
		_, err := api.ProfileTypes(context.Background(), &pb.ProfileTypesRequest{
			Start: timestamppb.New(start),
			End:   timestamppb.New(end),
		})
		require.NoError(t, err)
		require.Equal(t, start.Unix(), rec.gotStart.Unix(), "explicit start must pass through")
		require.Equal(t, end.Unix(), rec.gotEnd.Unix(), "explicit end must pass through")
	})
}
