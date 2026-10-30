package profiles

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/pug-sh/pug/internal/apperr"
	natsdeps "github.com/pug-sh/pug/internal/deps/nats"
	sdkprofilesv1 "github.com/pug-sh/pug/internal/gen/proto/sdk/profiles/v1"
	"github.com/pug-sh/pug/internal/geo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// publishedIdentifies decodes what the spy captured, checking each message went
// to the identify subject.
func publishedIdentifies(t *testing.T, spy *spyJetStream) []*sdkprofilesv1.ProfileIdentifyMessage {
	t.Helper()
	spy.mu.Lock()
	defer spy.mu.Unlock()
	out := make([]*sdkprofilesv1.ProfileIdentifyMessage, len(spy.published))
	for i, m := range spy.published {
		if m.Subject != natsdeps.ProfileIdentifySubject {
			t.Errorf("message %d subject = %q, want %q", i, m.Subject, natsdeps.ProfileIdentifySubject)
		}
		out[i] = &sdkprofilesv1.ProfileIdentifyMessage{}
		if err := proto.Unmarshal(m.Data, out[i]); err != nil {
			t.Fatalf("unmarshal message %d: %v", i, err)
		}
	}
	return out
}

func batch(items ...*sdkprofilesv1.IdentifyRequest) *connect.Request[sdkprofilesv1.BatchIdentifyRequest] {
	return connect.NewRequest(&sdkprofilesv1.BatchIdentifyRequest{Identifies: items})
}

func TestBatchIdentify_PublishesEachItem(t *testing.T) {
	spy := &spyJetStream{}
	srv := NewServer(spy)
	traits, _ := structpb.NewStruct(map[string]any{"plan": "pro"})

	res, err := srv.BatchIdentify(sdkContext("proj-test"), batch(
		&sdkprofilesv1.IdentifyRequest{
			ExternalId:  proto.String("user-1"),
			Traits:      traits,
			AnonymousId: proto.String("anon-1"),
			DeviceId:    proto.String("device-1"),
		},
		&sdkprofilesv1.IdentifyRequest{ExternalId: proto.String("user-2")}, // no traits
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res.Msg.GetAccepted(); got != 2 {
		t.Errorf("accepted = %d, want 2", got)
	}

	msgs := publishedIdentifies(t, spy)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 published messages, got %d", len(msgs))
	}
	first, second := msgs[0], msgs[1]
	if first.GetExternalId() != "user-1" || first.GetProjectId() != "proj-test" ||
		first.GetAnonymousId() != "anon-1" || first.GetDeviceId() != "device-1" ||
		first.GetTraits().GetFields()["plan"].GetStringValue() != "pro" {
		t.Errorf("first message = %v", first)
	}
	if second.GetExternalId() != "user-2" || second.GetProjectId() != "proj-test" || len(second.GetTraits().GetFields()) != 0 {
		t.Errorf("second message = %v", second)
	}
}

func TestBatchIdentify_StripsClientSuppliedIP(t *testing.T) {
	spy := &spyJetStream{}
	srv := NewServer(spy)
	traits, _ := structpb.NewStruct(map[string]any{geo.PropIP: "203.0.113.7", "plan": "pro"})

	if _, err := srv.BatchIdentify(sdkContext("proj-test"), batch(
		&sdkprofilesv1.IdentifyRequest{ExternalId: proto.String("user-1"), Traits: traits},
	)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msgs := publishedIdentifies(t, spy)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}
	fields := msgs[0].GetTraits().GetFields()
	if _, ok := fields[geo.PropIP]; ok {
		t.Errorf("%s must be stripped from traits, but it was published", geo.PropIP)
	}
	if fields["plan"].GetStringValue() != "pro" {
		t.Errorf("legitimate trait dropped: plan = %v, want %q", fields["plan"], "pro")
	}
}

func TestBatchIdentify_EmptyBatch(t *testing.T) {
	spy := &spyJetStream{}
	srv := NewServer(spy)

	res, err := srv.BatchIdentify(sdkContext("proj-test"), batch())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := res.Msg.GetAccepted(); got != 0 {
		t.Errorf("accepted = %d, want 0", got)
	}
	if msgs := publishedIdentifies(t, spy); len(msgs) != 0 {
		t.Errorf("published %d messages for an empty batch", len(msgs))
	}
}

func TestBatchIdentify_Unauthenticated(t *testing.T) {
	spy := &spyJetStream{}
	srv := NewServer(spy)

	// No principal in context.
	_, err := srv.BatchIdentify(context.Background(), batch(
		&sdkprofilesv1.IdentifyRequest{ExternalId: proto.String("user-1")},
	))
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Code() != connect.CodeUnauthenticated {
		t.Fatalf("want unauthenticated apperr, got %v (%T)", err, err)
	}
	if msgs := publishedIdentifies(t, spy); len(msgs) != 0 {
		t.Errorf("published %d messages without a principal", len(msgs))
	}
}

// A publish failure stops the batch at that item. The SDK retries the whole
// batch, so the items before it publish again; the identify worker tolerates
// that.
func TestBatchIdentify_PublishErrorStopsTheBatch(t *testing.T) {
	spy := &spyJetStream{failOn: 2}
	srv := NewServer(spy)

	_, err := srv.BatchIdentify(sdkContext("proj-test"), batch(
		&sdkprofilesv1.IdentifyRequest{ExternalId: proto.String("user-1")},
		&sdkprofilesv1.IdentifyRequest{ExternalId: proto.String("user-2")},
		&sdkprofilesv1.IdentifyRequest{ExternalId: proto.String("user-3")},
	))
	if code := connect.CodeOf(err); code != connect.CodeInternal {
		t.Fatalf("code = %v (err %v), want %v", code, err, connect.CodeInternal)
	}
	msgs := publishedIdentifies(t, spy)
	if len(msgs) != 1 || msgs[0].GetExternalId() != "user-1" {
		t.Errorf("published %v, want only user-1", msgs)
	}
}
