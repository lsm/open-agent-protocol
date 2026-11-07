package servehttp

import (
	"context"
	"net/http"
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
func (s taskSession) CancelTask(_ context.Context, id protocol.TaskID) (protocol.SessionTaskCancelResponse, error) {
	if id != "task-1" {
		return protocol.SessionTaskCancelResponse{Outcome: protocol.SessionTaskCancelAlreadyEnded}, nil
	}
	return protocol.SessionTaskCancelResponse{Outcome: protocol.SessionTaskCancelRequested}, nil
}
func TestTaskCancelHTTP(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("tasks", taskAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	hub, server := newServer(t, registry, Options{})
	defer hub.CloseSessions(context.Background())
	if _, _, err := hub.Open(context.Background(), "tasks", base.OpenRequest{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, header, payload string
		status                int
		outcome               protocol.SessionTaskCancelOutcome
	}{
		{"requested", "s", "s", http.StatusOK, protocol.SessionTaskCancelRequested},
		{"bad payload", "s", "elsewhere", http.StatusBadRequest, ""},
		{"bad header", "elsewhere", "s", http.StatusBadRequest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := requestEnvelope(t, protocol.TypeSessionTaskCancelRequest, "cancel", protocol.SessionTaskCancelRequest{SessionID: protocol.SessionID(tc.payload), TaskID: "task-1"}, tc.header, "", "")
			status, res := postEnvelope(t, server, "/sessions/s/cancel", req)
			if status != tc.status {
				t.Fatalf("status %d response %+v", status, res)
			}
			if tc.status == http.StatusOK {
				var ack protocol.SessionTaskCancelResponse
				if res.Type != protocol.TypeSessionTaskCancelResponse || res.InReplyTo != "cancel" || res.DecodePayload(&ack) != nil || ack.Outcome != tc.outcome {
					t.Fatalf("response %+v ack %+v", res, ack)
				}
			}
		})
	}
}

func TestTaskCancelHTTPUnsupported(t *testing.T) {
	hub, server := newServer(t, memoryRegistry(16), Options{})
	defer hub.CloseSessions(context.Background())
	if _, _, err := hub.Open(context.Background(), "memory", base.OpenRequest{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	req := requestEnvelope(t, protocol.TypeSessionTaskCancelRequest, "cancel", protocol.SessionTaskCancelRequest{SessionID: "s", TaskID: "t"}, "s", "", "")
	status, response := postEnvelope(t, server, "/sessions/s/cancel", req)
	var payload protocol.ErrorResponse
	if status != http.StatusBadRequest || response.Type != protocol.TypeErrorResponse || response.DecodePayload(&payload) != nil || payload.Error.Code != "unsupported_feature" {
		t.Fatalf("status %d response %+v error %+v", status, response, payload)
	}
}
