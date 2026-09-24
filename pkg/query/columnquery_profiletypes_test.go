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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// ProfileTypes must never reach the querier with a zero bound: the backends drop
// their time filter unless both bounds are non-zero and then run an unbounded
// SELECT DISTINCT over the whole table (guettli/parca#119). No caller needs a
// range-less query, so every request missing a bound -- both unset or one-sided --
// is rejected with InvalidArgument and never reaches the querier; an explicit
// range passes through.
func TestProfileTypesRequiresRange(t *testing.T) {
	reject := func(t *testing.T, req *pb.ProfileTypesRequest) {
		t.Helper()
		rec := &recordingQuerier{}
		api := &ColumnQueryAPI{querier: rec}
		_, err := api.ProfileTypes(context.Background(), req)
		require.Equal(t, codes.InvalidArgument, status.Code(err), "a missing bound must be rejected")
		require.False(t, rec.called, "querier must not be called for a rejected request")
	}

	t.Run("both bounds unset is rejected", func(t *testing.T) {
		reject(t, &pb.ProfileTypesRequest{})
	})

	t.Run("only start set is rejected", func(t *testing.T) {
		reject(t, &pb.ProfileTypesRequest{Start: timestamppb.New(time.Unix(1_700_000_000, 0).UTC())})
	})

	t.Run("only end set is rejected", func(t *testing.T) {
		reject(t, &pb.ProfileTypesRequest{End: timestamppb.New(time.Unix(1_700_003_600, 0).UTC())})
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
