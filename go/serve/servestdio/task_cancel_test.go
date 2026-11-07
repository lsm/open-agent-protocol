package servestdio

import (
	"context"
	"encoding/json"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type taskAdapter struct{ *base.Memory }
type taskSession struct{ base.Session }

func (a taskAdapter) Open(ctx context.Context, req base.OpenRequest) (base.Session, error) {
	s, e := a.Memory.Open(ctx, req)
	if e != nil {
		return nil, e
	}
	return taskSession{s}, nil
}
func (s taskSession) CancelTask(context.Context, protocol.TaskID) (protocol.SessionTaskCancelResponse, error) {
	return protocol.SessionTaskCancelResponse{Outcome: protocol.SessionTaskCancelRequested}, nil
}
func TestTaskCancelStdio(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("tasks", taskAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	defer hub.CloseSessions(context.Background())
	if _, _, err := hub.Open(context.Background(), "tasks", base.OpenRequest{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ header, payload, code string }{{"s", "s", ""}, {"s", "other", "scope_mismatch"}, {"other", "s", "scope_mismatch"}} {
		env, err := protocol.NewEnvelope(protocol.TypeSessionTaskCancelRequest, "cancel", protocol.SessionTaskCancelRequest{SessionID: protocol.SessionID(tc.payload), TaskID: "t"})
		if err != nil {
			t.Fatal(err)
		}
		env.SessionID = protocol.SessionID(tc.header)
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		answer, werr := server.cancelOp(context.Background(), requestLine{SessionID: "s", Request: raw})
		if tc.code != "" {
			if werr == nil || werr.Code != tc.code {
				t.Fatalf("want %s got %+v", tc.code, werr)
			}
			continue
		}
		if werr != nil {
			t.Fatal(werr)
		}
		var response protocol.Envelope
		if err := json.Unmarshal(answer, &response); err != nil {
			t.Fatal(err)
		}
		var ack protocol.SessionTaskCancelResponse
		if response.Type != protocol.TypeSessionTaskCancelResponse || response.InReplyTo != "cancel" || response.DecodePayload(&ack) != nil || ack.Outcome != protocol.SessionTaskCancelRequested {
			t.Fatalf("response %+v ack %+v", response, ack)
		}
	}
}

func TestTaskCancelStdioUnsupported(t *testing.T) {
	hub := newTestHub(t, 16, 16)
	defer hub.CloseSessions(context.Background())
	if _, _, err := hub.Open(context.Background(), "memory", base.OpenRequest{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := protocol.NewEnvelope(protocol.TypeSessionTaskCancelRequest, "cancel", protocol.SessionTaskCancelRequest{SessionID: "s", TaskID: "t"})
	env.SessionID = "s"
	raw, _ := json.Marshal(env)
	_, werr := server.cancelOp(context.Background(), requestLine{SessionID: "s", Request: raw})
	if werr == nil || werr.Code != "unsupported_feature" {
		t.Fatalf("got %+v", werr)
	}
}
