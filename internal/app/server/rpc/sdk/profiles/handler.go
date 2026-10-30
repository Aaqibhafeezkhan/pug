package profiles

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/pug-sh/pug/internal/app/server/rpc"
	"github.com/pug-sh/pug/internal/deps/nats"
	"github.com/pug-sh/pug/internal/deps/telemetry"
	sdkprofilesv1 "github.com/pug-sh/pug/internal/gen/proto/sdk/profiles/v1"
	"github.com/pug-sh/pug/internal/gen/proto/sdk/profiles/v1/sdkprofilesv1connect"
	"github.com/pug-sh/pug/internal/geo"
	"github.com/pug-sh/pug/internal/slogx"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"
)

var ipStrippedCounter metric.Int64Counter

func init() {
	meter := otel.Meter("github.com/pug-sh/pug/internal/app/server/rpc/sdk/profiles")
	ipStrippedCounter, _ = meter.Int64Counter(
		"profiles.identify_ip_stripped_total",
		metric.WithDescription("A $ip trait was stripped during Identify or BatchIdentify because the visitor IP must never be persisted. Our SDKs never send it, so a non-zero count means a hand-crafted client supplied an IP in traits."),
	)
}

type Server struct {
	sdkprofilesv1connect.UnimplementedProfilesSDKServiceHandler
	producer jetstream.JetStream
}

func NewServer(js jetstream.JetStream) *Server {
	return &Server{
		producer: js,
	}
}

func (s *Server) Identify(
	ctx context.Context,
	req *connect.Request[sdkprofilesv1.IdentifyRequest],
) (*connect.Response[sdkprofilesv1.IdentifyResponse], error) {
	principal, err := rpc.MustGetPrincipalWithProject(ctx)
	if err != nil {
		return nil, err
	}

	if err := s.publishIdentify(ctx, principal.Project.ID, req.Msg); err != nil {
		return nil, err
	}

	return connect.NewResponse(&sdkprofilesv1.IdentifyResponse{}), nil
}

// BatchIdentify publishes each item exactly as Identify publishes one. It stops
// at the first failure: the SDK retries the whole batch, and the identify worker
// tolerates the items before the failure arriving twice.
func (s *Server) BatchIdentify(
	ctx context.Context,
	req *connect.Request[sdkprofilesv1.BatchIdentifyRequest],
) (*connect.Response[sdkprofilesv1.BatchIdentifyResponse], error) {
	principal, err := rpc.MustGetPrincipalWithProject(ctx)
	if err != nil {
		return nil, err
	}

	items := req.Msg.GetIdentifies()
	for _, item := range items {
		if err := s.publishIdentify(ctx, principal.Project.ID, item); err != nil {
			return nil, err
		}
	}

	return connect.NewResponse(&sdkprofilesv1.BatchIdentifyResponse{
		Accepted: proto.Uint32(uint32(len(items))),
	}), nil
}

// publishIdentify strips a client-supplied $ip trait (counting it) and publishes
// one ProfileIdentifyMessage for the identify worker. Its error is a client-safe
// connect error.
func (s *Server) publishIdentify(ctx context.Context, projectID string, req *sdkprofilesv1.IdentifyRequest) error {
	// The visitor IP is personal data and must never be persisted: strip the
	// canonical $ip key from traits so an untrusted SDK caller cannot inject it
	// into a profile's stored properties (mirrors the events enrichGeo strip),
	// and count any occurrence. Scope is the canonical key our SDKs produce;
	// arbitrary client data under other keys is never interpreted as an IP.
	// delete on a nil Fields map (traits unset) is a safe no-op.
	traits := req.GetTraits().GetFields()
	if _, ok := traits[geo.PropIP]; ok {
		ipStrippedCounter.Add(ctx, 1)
	}
	delete(traits, geo.PropIP)

	msg := &sdkprofilesv1.ProfileIdentifyMessage{
		ExternalId:  proto.String(req.GetExternalId()),
		Traits:      req.GetTraits(),
		ProjectId:   proto.String(projectID),
		AnonymousId: proto.String(req.GetAnonymousId()),
		DeviceId:    proto.String(req.GetDeviceId()),
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		slog.ErrorContext(ctx, "failed to marshal identify message", slogx.Error(err),
			slog.String("project_id", projectID))
		telemetry.RecordError(ctx, err)
		return connect.NewError(connect.CodeInternal, errors.New("failed to process request"))
	}

	if _, err = s.producer.Publish(ctx, nats.ProfileIdentifySubject, data); err != nil {
		slog.ErrorContext(ctx, "failed to publish identify message", slogx.Error(err),
			slog.String("project_id", projectID))
		telemetry.RecordError(ctx, err)
		return connect.NewError(connect.CodeInternal, errors.New("failed to process request"))
	}

	return nil
}
