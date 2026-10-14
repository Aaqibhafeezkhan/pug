package profiles_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pug-sh/pug/internal/core/profiles"
	"github.com/pug-sh/pug/internal/testutil"
)

// TestProfilesList_PagesWithinOneSecond pins the seek at millisecond
// precision. Event times carry milliseconds, but a time.Time bound to a ?
// placeholder is formatted to whole seconds. A cursor compared as a time
// therefore seeks from the start of its second and skips everyone ordered
// after it within that second. Id order runs opposite to time order, so an
// ordering that ties the three on their shared second comes out reversed.
func TestProfilesList_PagesWithinOneSecond(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ch := testutil.SetupClickHouse(t)
	ctx := context.Background()
	projectID := "proj-ms"
	second := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	seedEvent(ctx, t, ch, projectID, "anon-a", second.Add(900*time.Millisecond))
	seedEvent(ctx, t, ch, projectID, "anon-b", second.Add(500*time.Millisecond))
	seedEvent(ctx, t, ch, projectID, "anon-c", second.Add(100*time.Millisecond))

	service := profiles.NewService(nil, ch.Conn, nil)
	want := []string{"anon-a", "anon-b", "anon-c"}
	for _, sort := range []profiles.ListSort{profiles.ListSortCreateTime, profiles.ListSortFirstSeen, profiles.ListSortLastSeen} {
		t.Run(string(sort), func(t *testing.T) {
			if got := walkList(ctx, t, service, projectID, sort, len(want)); !slices.Equal(got, want) {
				t.Fatalf("order = %v, want %v", got, want)
			}
		})
	}
}

func seedEvent(ctx context.Context, t *testing.T, ch *testutil.TestClickHouse, projectID, distinctID string, at time.Time) {
	t.Helper()
	testutil.InsertEvent(ctx, t, ch.Conn, uuid.NewString(), projectID, distinctID, "page_view", uuid.NewString(),
		map[string]string{}, map[string]string{}, at)
}

// walkList pages through the list one person at a time, seeking each page
// from the previous row's cursor value, and returns the ids in order. A walk
// longer than limit fails rather than loop on a seek that never advances.
func walkList(ctx context.Context, t *testing.T, service *profiles.Service, projectID string, sort profiles.ListSort, limit int) []string {
	t.Helper()
	var order []string
	params := profiles.ListParams{ProjectID: projectID, PageSize: 1, Sort: sort}
	for {
		if len(order) > limit {
			t.Fatalf("pagination did not terminate: %v", order)
		}
		got, err := service.List(ctx, params)
		if err != nil {
			t.Fatalf("List after %v: %v", order, err)
		}
		if len(got) == 0 {
			return order
		}
		order = append(order, got[0].ID)
		params.HasCursor = true
		params.CursorTime = profiles.ListSortValue(sort, got[0])
		params.CursorID = got[0].ID
	}
}
