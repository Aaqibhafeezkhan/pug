package profiles_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/pug-sh/pug/internal/core/profiles"
	"github.com/pug-sh/pug/internal/testutil"
)

// TestProfilesList_Sorts pins each ordering of the person list and the keyset
// walk over it. In the fixture, create time, first seen and last seen all
// disagree, so a sort that reads another column comes out in a different
// order. So does a cursor that seeks on one. Walking one row per page makes
// every row's cursor value seek. Under the activity sorts, u-3 → u-2 crosses
// the tie of people with no activity, which SQL and ListSortValue must both
// key on the epoch.
func TestProfilesList_Sorts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ch := testutil.SetupClickHouse(t)
	ctx := context.Background()
	projectID := "proj-sort"
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

	profile := func(id, externalID string, created time.Time) {
		if err := ch.Conn.Exec(ctx,
			`INSERT INTO profiles (id, project_id, external_id, properties, is_deleted, create_time, update_time) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, projectID, externalID, map[string]any{}, uint8(0), created, created,
		); err != nil {
			t.Fatalf("seed profile %s: %v", id, err)
		}
	}

	//          create  first  last
	// anon-a      0      0     50
	// anon-b     20     20     20
	// anon-c     60     60     60
	// u-1        30     10     40   identified after its first events
	// u-2        40      —      —   identified, no activity
	// u-3         5      —      —   identified, no activity
	seedEvent(ctx, t, ch, projectID, "anon-a", at(0))
	seedEvent(ctx, t, ch, projectID, "anon-a", at(50))
	seedEvent(ctx, t, ch, projectID, "anon-b", at(20))
	seedEvent(ctx, t, ch, projectID, "anon-c", at(60))
	profile("u-1", "ext-1", at(30))
	seedEvent(ctx, t, ch, projectID, "ext-1", at(10))
	seedEvent(ctx, t, ch, projectID, "ext-1", at(40))
	profile("u-2", "ext-2", at(40))
	profile("u-3", "ext-3", at(5))

	service := profiles.NewService(nil, ch.Conn, nil)
	for _, tc := range []struct {
		sort profiles.ListSort
		want []string
	}{
		{profiles.ListSortCreateTime, []string{"anon-c", "u-2", "u-1", "anon-b", "u-3", "anon-a"}},
		{profiles.ListSortFirstSeen, []string{"anon-c", "anon-b", "u-1", "anon-a", "u-3", "u-2"}},
		{profiles.ListSortLastSeen, []string{"anon-c", "anon-a", "u-1", "anon-b", "u-3", "u-2"}},
	} {
		t.Run(string(tc.sort), func(t *testing.T) {
			if got := walkList(ctx, t, service, projectID, tc.sort, len(tc.want)); !slices.Equal(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
		})
	}
}
