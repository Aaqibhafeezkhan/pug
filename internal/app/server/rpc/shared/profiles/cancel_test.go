package profiles

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/authn"
	"connectrpc.com/connect"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/pug-sh/pug/internal/app/server/rpc"
	coreprofiles "github.com/pug-sh/pug/internal/core/profiles"
	natsdeps "github.com/pug-sh/pug/internal/deps/nats"
	profilesv1 "github.com/pug-sh/pug/internal/gen/proto/shared/profiles/v1"
	profilesv1connect "github.com/pug-sh/pug/internal/gen/proto/shared/profiles/v1/profilesv1connect"
	"github.com/pug-sh/pug/internal/gen/repo/dbread"
	"google.golang.org/protobuf/proto"
)

// cancelingConn is the client going away mid-read: Query cancels the request
// and hands back rows that fail with the cancel. With err set, the rows fail
// with err instead and the request stays alive.
type cancelingConn struct {
	driver.Conn
	cancel context.CancelFunc
	err    error
}

func (c *cancelingConn) Query(ctx context.Context, _ string, _ ...any) (driver.Rows, error) {
	if c.err != nil {
		return failedRows{err: c.err}, nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	return failedRows{err: ctx.Err()}, nil
}

type failedRows struct {
	driver.Rows
	err error
}

func (r failedRows) Next() bool   { return false }
func (r failedRows) Err() error   { return r.err }
func (r failedRows) Close() error { return r.err }

// handlerErrs receives what the handler returned, before ErrorInterceptor maps it.
type handlerErrs chan error

func (c handlerErrs) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc { return next }

func (c handlerErrs) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (c handlerErrs) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		err := next(ctx, conn)
		c <- err
		return err
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

// runList streams one List through a real handler and returns what the handler
// returned. Unless conn.err is set, conn cancels the request mid-read.
func runList(t *testing.T, conn *cancelingConn) error {
	t.Helper()
	errs := make(handlerErrs, 1)
	path, handler := profilesv1connect.NewProfilesServiceHandler(
		NewServer(coreprofiles.NewService(nil, conn, &natsdeps.NATSClient{})),
		connect.WithInterceptors(errs),
	)
	authMW := authn.NewMiddleware(func(context.Context, *http.Request) (any, error) {
		return &rpc.Principal{Project: &dbread.Project{ID: "p"}}, nil
	})
	mux := http.NewServeMux()
	mux.Handle(path, authMW.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		conn.cancel = cancel
		handler.ServeHTTP(w, r.WithContext(ctx))
	})))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	stream, err := profilesv1connect.NewProfilesServiceClient(http.DefaultClient, ts.URL).
		List(context.Background(), connect.NewRequest(&profilesv1.ListRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for stream.Receive() {
	}
	_ = stream.Close()

	// The interceptor sends before Connect ends the stream, so the error is
	// already here; nothing would arrive if the handler panicked.
	select {
	case err := <-errs:
		return err
	default:
		t.Fatal("the handler returned no error")
		return nil
	}
}

func TestListCanceledMidReadIsCanceled(t *testing.T) {
	logs := captureLogs(t)
	if got := connect.CodeOf(runList(t, &cancelingConn{})); got != connect.CodeCanceled {
		t.Errorf("handler code = %v, want canceled", got)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a canceled List logged an error:\n%s", logs)
	}
}

func TestListFailureIsInternal(t *testing.T) {
	logs := captureLogs(t)
	for _, cause := range []error{errors.New("boom"), context.Canceled} {
		logs.Reset()
		if got := connect.CodeOf(runList(t, &cancelingConn{err: cause})); got != connect.CodeInternal {
			t.Errorf("%v: handler code = %v, want internal", cause, got)
		}
		if !strings.Contains(logs.String(), "failed listing profiles") {
			t.Errorf("%v did not log the failure:\n%s", cause, logs)
		}
	}
}

var gets = map[string]func(*Server, context.Context) error{
	"Get": func(s *Server, ctx context.Context) error {
		_, err := s.Get(ctx, connect.NewRequest(&profilesv1.GetRequest{Id: proto.String("u")}))
		return err
	},
	"GetByExternalId": func(s *Server, ctx context.Context) error {
		_, err := s.GetByExternalId(ctx, connect.NewRequest(&profilesv1.GetByExternalIdRequest{ExternalId: proto.String("u")}))
		return err
	},
}

func TestGetCanceledIsCanceled(t *testing.T) {
	logs := captureLogs(t)
	for name, get := range gets {
		ctx, cancel := context.WithCancel(authCtx("p"))
		s := NewServer(coreprofiles.NewService(nil, &cancelingConn{cancel: cancel}, &natsdeps.NATSClient{}))
		if got := connect.CodeOf(get(s, ctx)); got != connect.CodeCanceled {
			t.Errorf("%s code = %v, want canceled", name, got)
		}
		cancel()
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("a canceled read logged an error:\n%s", logs)
	}
}

// A failure on a live request still logs and stays INTERNAL, even one that only
// wraps a deadline, as a ClickHouse dial timeout does, or a cancel the caller
// did not make.
func TestGetFailureIsInternal(t *testing.T) {
	logs := captureLogs(t)
	for name, get := range gets {
		for _, cause := range []error{errors.New("boom"), context.DeadlineExceeded, context.Canceled} {
			logs.Reset()
			s := NewServer(coreprofiles.NewService(nil, &cancelingConn{err: cause}, &natsdeps.NATSClient{}))
			if got := connect.CodeOf(get(s, authCtx("p"))); got != connect.CodeInternal {
				t.Errorf("%s on %v: code = %v, want internal", name, cause, got)
			}
			if !strings.Contains(logs.String(), "failed reading profile") {
				t.Errorf("%s on %v did not log the failure:\n%s", name, cause, logs)
			}
		}
	}
}
