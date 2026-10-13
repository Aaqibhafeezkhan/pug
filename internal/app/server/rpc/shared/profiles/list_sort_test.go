package profiles

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/pug-sh/pug/internal/apperr"
	coreprofiles "github.com/pug-sh/pug/internal/core/profiles"
	natsdeps "github.com/pug-sh/pug/internal/deps/nats"
	profilesv1 "github.com/pug-sh/pug/internal/gen/proto/shared/profiles/v1"
	profilesv1connect "github.com/pug-sh/pug/internal/gen/proto/shared/profiles/v1/profilesv1connect"
	"github.com/pug-sh/pug/internal/testutil"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
)

// profileListSort is where the requested sort becomes the query's ordering
// column. A wrong arm serves another column's order under the requested header.
func TestProfileListSort(t *testing.T) {
	for _, tc := range []struct {
		in   profilesv1.ProfileListSort
		want coreprofiles.ListSort
	}{
		{profilesv1.ProfileListSort_PROFILE_LIST_SORT_UNSPECIFIED, coreprofiles.ListSortCreateTime},
		{profilesv1.ProfileListSort_PROFILE_LIST_SORT_CREATE_TIME, coreprofiles.ListSortCreateTime},
		{profilesv1.ProfileListSort_PROFILE_LIST_SORT_FIRST_SEEN, coreprofiles.ListSortFirstSeen},
		{profilesv1.ProfileListSort_PROFILE_LIST_SORT_LAST_SEEN, coreprofiles.ListSortLastSeen},
	} {
		if got := profileListSort(tc.in); got != tc.want {
			t.Errorf("profileListSort(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A token's seek value is a time in the ordering column it was issued under.
// Seeking another column's order on it would skip or repeat people, so the
// handler must refuse the token before it reads anything.
func TestList_RejectsTokenFromAnotherSort(t *testing.T) {
	token, err := (&profileListCursor{
		Sort: coreprofiles.ListSortLastSeen,
		Time: time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC),
		ID:   "anon-1",
	}).encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	s := NewServer(coreprofiles.NewService(nil, nil, &natsdeps.NATSClient{}))
	err = s.List(authCtx("proj_1"), connect.NewRequest(&profilesv1.ListRequest{
		PageToken: proto.String(token),
		Sort:      profilesv1.ProfileListSort_PROFILE_LIST_SORT_FIRST_SEEN.Enum(),
	}), nil)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != connect.CodeInvalidArgument {
		t.Fatalf("want apperr CodeInvalidArgument, got %v (%T)", err, err)
	}
	if ae.Reason() != apperr.ReasonInvalidPageToken {
		t.Fatalf("want INVALID_PAGE_TOKEN reason, got %q", ae.Reason())
	}
}

// TestList_SortedPagesResumeFromToken pins the sort through the handler. Both
// the stream's own second page and a client resuming from page one's token
// must continue in the requested order. Each person's last seen runs opposite
// to its first seen, so a dropped sort streams the list reversed. A cursor
// carrying create time instead of last seen seeks past the remaining person.
// Resuming under the default sort checks that a sort-less request accepts the
// token a sort-less request was issued.
func TestList_SortedPagesResumeFromToken(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	ch := testutil.SetupClickHouse(t)
	projectID := xid.New().String()

	// anon-000 … anon-100: person i is first seen at base+i s and last seen at
	// base+1000-i s.
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	ids := make([]string, pageSize+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("anon-%03d", i)
		for _, sec := range []int{i, 1000 - i} {
			testutil.InsertEvent(ctx, t, ch.Conn, uuid.NewString(), projectID, ids[i], "page_view", uuid.NewString(),
				map[string]string{}, map[string]string{}, base.Add(time.Duration(sec)*time.Second))
		}
	}
	newestFirst := slices.Clone(ids)
	slices.Reverse(newestFirst)

	client := newProfilesTestClient(t, NewServer(coreprofiles.NewService(nil, ch.Conn, &natsdeps.NATSClient{})), projectID)
	for _, tc := range []struct {
		name string
		sort *profilesv1.ProfileListSort
		want []string
	}{
		{"default", nil, newestFirst},
		{"last seen", profilesv1.ProfileListSort_PROFILE_LIST_SORT_LAST_SEEN.Enum(), ids},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages := receiveListPages(t, ctx, client, &profilesv1.ListRequest{Sort: tc.sort})
			if len(pages) != 2 {
				t.Fatalf("pages = %d, want 2", len(pages))
			}
			if got := append(pageIDs(pages[0]), pageIDs(pages[1])...); !slices.Equal(got, tc.want) {
				t.Fatalf("streamed order = %v, want %v", got, tc.want)
			}

			resumed := receiveListPages(t, ctx, client, &profilesv1.ListRequest{
				Sort:      tc.sort,
				PageToken: proto.String(pages[0].GetNextPageToken()),
			})
			if len(resumed) != 1 || !slices.Equal(pageIDs(resumed[0]), tc.want[pageSize:]) {
				t.Fatalf("resumed pages = %v, want one page of %v", resumed, tc.want[pageSize:])
			}
		})
	}
}

func receiveListPages(
	t *testing.T, ctx context.Context, client profilesv1connect.ProfilesServiceClient, req *profilesv1.ListRequest,
) []*profilesv1.ListResponse {
	t.Helper()
	stream, err := client.List(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var pages []*profilesv1.ListResponse
	for stream.Receive() {
		pages = append(pages, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}
	return pages
}

func pageIDs(page *profilesv1.ListResponse) []string {
	ids := make([]string, len(page.GetProfiles()))
	for i, p := range page.GetProfiles() {
		ids[i] = p.GetId()
	}
	return ids
}
