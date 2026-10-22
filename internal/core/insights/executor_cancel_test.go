package insights

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// streamConn fails every query with err, at the start or mid-stream.
type streamConn struct {
	driver.Conn
	err       error
	midStream bool
}

func (c streamConn) Query(context.Context, string, ...any) (driver.Rows, error) {
	if c.midStream {
		return failedRows{err: c.err}, nil
	}
	return nil, c.err
}

type failedRows struct {
	driver.Rows
	err error
}

func (r failedRows) Columns() []string { return nil }
func (r failedRows) Next() bool        { return false }
func (r failedRows) Err() error        { return r.err }
func (r failedRows) Close() error      { return r.err }

func executorQueries(e *Executor) map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"QueryTrends": func(ctx context.Context) error { _, err := e.QueryTrends(ctx, "p", TrendsQuery{}); return err },
		"QueryScalar": func(ctx context.Context) error { _, err := e.QueryScalar(ctx, "p", ScalarQuery{}); return err },
		"QueryAggregateKeys": func(ctx context.Context) error {
			_, err := e.QueryAggregateKeys(ctx, "p", "SELECT 1", nil)
			return err
		},
		"QueryStringColumn": func(ctx context.Context) error {
			_, err := e.QueryStringColumn(ctx, "p", "SELECT 1", nil)
			return err
		},
		"QueryFunnel":   func(ctx context.Context) error { _, err := e.QueryFunnel(ctx, "p", FunnelQuery{}); return err },
		"QueryUserFlow": func(ctx context.Context) error { _, err := e.QueryUserFlow(ctx, "p", UserFlowQuery{}); return err },
		"QueryTopK": func(ctx context.Context) error {
			_, err := e.QueryTopK(ctx, "p", TopKQuery{sql: "SELECT 1"})
			return err
		},
		"QueryTopKProfiles": func(ctx context.Context) error {
			_, err := e.QueryTopKProfiles(ctx, "p", "SELECT 1", nil)
			return err
		},
		"QueryFunnelUserEvents": func(ctx context.Context) error {
			_, err := e.QueryFunnelUserEvents(ctx, "p", FunnelTimingQuery{})
			return err
		},
		"QueryRetention": func(ctx context.Context) error { _, err := e.QueryRetention(ctx, "p", RetentionQuery{}); return err },
	}
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
func TestExecutorLogsAFailedStream(t *testing.T) {
	logs := captureLogs(t)
	for _, cause := range []error{errors.New("boom"), context.DeadlineExceeded, context.Canceled} {
		for name, query := range executorQueries(NewExecutor(streamConn{err: cause, midStream: true})) {
			logs.Reset()
			if err := query(context.Background()); !errors.Is(err, cause) {
				t.Errorf("%s (%v): err = %v", name, cause, err)
			}
			if !strings.Contains(logs.String(), "iteration failed") {
				t.Errorf("%s (%v) did not log the failed stream:\n%s", name, cause, logs)
			}
		}
	}
}
