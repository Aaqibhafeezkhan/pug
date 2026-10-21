package activity

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/authn"
	"connectrpc.com/connect"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/pug-sh/pug/internal/app/server/rpc"
	activityv1 "github.com/pug-sh/pug/internal/gen/proto/shared/activity/v1"
	"github.com/pug-sh/pug/internal/gen/repo/dbread"
	"google.golang.org/protobuf/proto"
)

// failingConn fails every query with err or, with cancel set, cancels the
// request first: the client going away mid-read.
type failingConn struct {
	driver.Conn
	err    error
	cancel context.CancelFunc
}

func (c failingConn) Query(ctx context.Context, _ string, _ ...any) (driver.Rows, error) {
	if c.cancel != nil {
		c.cancel()
		return nil, ctx.Err()
	}
	return nil, c.err
}

// otelconnect reads the handler's code: a cancel coded INTERNAL would mark the
// span an error and count toward the server error rate. The code comes from the
// request's context, so an error that only wraps a deadline, as a ClickHouse dial
// timeout does, stays INTERNAL.
func TestReadFailuresKeepTheirCode(t *testing.T) {
	user := proto.String("u")
	calls := map[string]func(*server, context.Context) error{
		"GetActivityFeed": func(s *server, ctx context.Context) error {
			_, err := s.GetActivityFeed(ctx, connect.NewRequest(&activityv1.GetActivityFeedRequest{DistinctId: user}))
			return err
		},
		"GetEventExplorer": func(s *server, ctx context.Context) error {
			_, err := s.GetEventExplorer(ctx, connect.NewRequest(&activityv1.GetEventExplorerRequest{}))
			return err
		},
		"GetActivityHeatmap": func(s *server, ctx context.Context) error {
			_, err := s.GetActivityHeatmap(ctx, connect.NewRequest(&activityv1.GetActivityHeatmapRequest{DistinctId: user}))
			return err
		},
		"GetProfileSessions": func(s *server, ctx context.Context) error {
			_, err := s.GetProfileSessions(ctx, connect.NewRequest(&activityv1.GetProfileSessionsRequest{DistinctId: user}))
			return err
		},
		"GetProfileStats": func(s *server, ctx context.Context) error {
			_, err := s.GetProfileStats(ctx, connect.NewRequest(&activityv1.GetProfileStatsRequest{DistinctId: user}))
			return err
		},
	}
	for _, tc := range []struct {
		name   string
		cancel bool
		err    error
		want   connect.Code
	}{
		{"canceled", true, nil, connect.CodeCanceled},
		{"failed", false, errors.New("boom"), connect.CodeInternal},
		{"dial timeout", false, fmt.Errorf("dial tcp: %w", context.DeadlineExceeded), connect.CodeInternal},
		{"stray cancel", false, context.Canceled, connect.CodeInternal},
	} {
		for name, call := range calls {
			ctx, cancel := context.WithCancel(authn.SetInfo(context.Background(), &rpc.Principal{
				AuthType: rpc.AuthTypePrivateKey,
				Project:  &dbread.Project{ID: "p"},
			}))
			conn := failingConn{err: tc.err}
			if tc.cancel {
				conn.cancel = cancel
			}
			if got := connect.CodeOf(call(NewServer(conn, nil, nil), ctx)); got != tc.want {
				t.Errorf("%s %s: code = %v, want %v", name, tc.name, got, tc.want)
			}
			cancel()
		}
	}
}
