package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// ConnectCtxErr wraps a context error in the appropriate Connect error code
// with an explicit client-facing message. The errorInterceptor in error.go maps
// any server fault on a done request with it, so a handler need not call it
// after a failed read. Returns nil for a nil err so call sites can keep the
// "if err := ctx.Err(); err != nil" idiom without re-checking.
func ConnectCtxErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return connect.NewError(connect.CodeDeadlineExceeded, errors.New("request timed out"))
	}
	if errors.Is(err, context.Canceled) {
		return connect.NewError(connect.CodeCanceled, errors.New("request canceled"))
	}
	// Defensive: only context errors should reach this path. Anything else is
	// a programmer error (passing a non-context error). Return Internal so the
	// mistake surfaces as a server bug rather than a misleading "canceled".
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}
