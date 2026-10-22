package events_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/pug-sh/pug/internal/core/events"
)

// failingConn fails each query whose SQL contains match, at the start or
// mid-stream, and answers every other query with no rows.
type failingConn struct {
	driver.Conn
	err       error
	match     string
	midStream bool
}

func (c failingConn) String() string {
	return fmt.Sprintf("{%v match=%q midStream=%t}", c.err, c.match, c.midStream)
}

func (c failingConn) Query(_ context.Context, query string, _ ...any) (driver.Rows, error) {
	if !strings.Contains(query, c.match) {
		return emptyRows{}, nil
	}
	if c.midStream {
		return emptyRows{err: c.err}, nil
	}
	return nil, c.err
}

type emptyRows struct {
	driver.Rows
	err error
}

func (r emptyRows) Next() bool   { return false }
func (r emptyRows) Err() error   { return r.err }
func (r emptyRows) Close() error { return r.err }

var reads = []struct {
	name string
	read func(context.Context, *events.Reader) error
}{
	{"GetEventExplorer", func(ctx context.Context, r *events.Reader) error {
		_, _, err := r.GetEventExplorer(ctx, events.EventExplorerParams{ProjectID: "p"})
		return err
	}},
	{"GetActivityFeed", func(ctx context.Context, r *events.Reader) error {
		_, _, err := r.GetActivityFeed(ctx, events.ActivityFeedParams{ProjectID: "p", DistinctID: "u"})
		return err
	}},
	{"GetActivityHeatmap", func(ctx context.Context, r *events.Reader) error {
		_, err := r.GetActivityHeatmap(ctx, events.ActivityHeatmapParams{ProjectID: "p", DistinctID: "u"})
		return err
	}},
	{"GetProfileStats", func(ctx context.Context, r *events.Reader) error {
		_, _, err := r.GetProfileStats(ctx, events.ProfileStatsParams{ProjectID: "p", DistinctID: "u"})
		return err
	}},
	{"GetProfileSessions", func(ctx context.Context, r *events.Reader) error {
		_, _, err := r.GetProfileSessions(ctx, events.ProfileSessionsParams{ProjectID: "p", DistinctID: "u"})
		return err
	}},
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// A deadline means the query outran the request's budget, and a cancel on a live
// request came from something other than the caller, so both log like any failure.
func TestReadsLogAFailure(t *testing.T) {
	logs := captureLogs(t)
	for _, tc := range reads {
		for _, cause := range []error{errors.New("boom"), context.DeadlineExceeded, context.Canceled} {
			for _, conn := range []failingConn{
				{err: cause},
				{err: cause, midStream: true},
				{err: cause, match: "FROM events"},
				{err: cause, match: "FROM events", midStream: true},
			} {
				logs.Reset()
				if err := tc.read(context.Background(), events.NewReader(conn)); !errors.Is(err, cause) {
					t.Errorf("%s %v: err = %v, want %v", tc.name, conn, err, cause)
				}
				want := "query failed"
				if conn.midStream {
					want = "iteration failed"
				}
				if !strings.Contains(logs.String(), want) {
					t.Errorf("%s %v did not log %q:\n%s", tc.name, conn, want, logs)
				}
			}
		}
	}
}
