package rpc

import (
	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/pug-sh/pug/internal/core/authz"
)

// HandlerOptions is the interceptor chain and limits every RPC handler is built
// with. Order is load-bearing. Correlation first so the id is in context
// downstream; otel outside ErrorInterceptor so a span exists for trace_id;
// Principal inside otel and outside validate, so a rejected request still names
// its tenant; ErrorInterceptor outside validate, which short-circuits a bad request
// without calling the inner chain; Logging outside that, so it observes the final
// resolved code. Authz is innermost and gates every unary RPC; handlers carry none
// of their own. It skips streaming RPCs, so TestStreamingRPCsNeedOnlyMembership
// holds each to a read every member may make.
//
// WithRecover is required, not cosmetic: on the /mcp loopback the handler runs on
// a jsonrpc2 goroutine that no net/http recover reaches, so an escaping panic
// would kill the process for every tenant.
func HandlerOptions(otel connect.Interceptor, authorizer *authz.Authorizer, lookup memberRoleLookup) connect.HandlerOption {
	return connect.WithHandlerOptions(
		connect.WithInterceptors(
			CorrelationInterceptor(),
			otel,
			PrincipalInterceptor(),
			LoggingInterceptor(),
			ErrorInterceptor(),
			validate.NewInterceptor(validate.WithoutErrorDetails()),
			AuthzInterceptor(authorizer, lookup),
		),
		connect.WithRecover(RecoverHandlerPanic),
		// The decompressed message: WithRequestLimits only sees the gzipped wire bytes.
		connect.WithReadMaxBytes(MaxRequestBytes),
	)
}
