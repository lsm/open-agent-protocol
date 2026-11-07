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

func (s taskSession) TaskOutput(_ context.Context, id protocol.TaskID, from *uint64) (protocol.SessionTaskOutputResponse, error) {
	if id != "task-1" {
		return protocol.SessionTaskOutputResponse{}, base.ErrInvalidSubmission
	}
	if from == nil || *from != 2 {
		return protocol.SessionTaskOutputResponse{}, base.ErrInvalidRequest
	}
	return protocol.SessionTaskOutputResponse{Output: "é", Next: 4, Ended: true}, nil
}
func TestTaskOutputHTTP(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("tasks", taskAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	hub, server := newServer(t, registry, Options{})
	defer hub.CloseSessions(context.Background())
	if _, _, err := hub.Open(context.Background(), "tasks", base.OpenRequest{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	from := uint64(2)
	for _, tc := range []struct {
		header, payload string
		status          int
	}{{"s", "s", http.StatusOK}, {"s", "other", http.StatusBadRequest}, {"other", "s", http.StatusBadRequest}} {
		req := requestEnvelope(t, protocol.TypeSessionTaskOutputRequest, "out", protocol.SessionTaskOutputRequest{SessionID: protocol.SessionID(tc.payload), TaskID: "task-1", From: &from}, tc.header, "", "")
		status, res := postEnvelope(t, server, "/sessions/s/task-output", req)
		if status != tc.status {
			t.Fatalf("status %d response %+v", status, res)
		}
		if status == http.StatusOK {
			var got protocol.SessionTaskOutputResponse
			if res.Type != protocol.TypeSessionTaskOutputResponse || res.InReplyTo != "out" || res.DecodePayload(&got) != nil || got.Output != "é" || got.Next != 4 || !got.Ended {
				t.Fatalf("response %+v output %+v", res, got)
			}
		}
	}
}

func TestTaskOutputHTTPRefusals(t *testing.T) {
	registry := serve.NewRegistry()
	_ = registry.Register("tasks", taskAdapter{base.NewMemory(base.Config{})})
	_ = registry.Register("memory", base.NewMemory(base.Config{}))
	hub, server := newServer(t, registry, Options{})
	defer hub.CloseSessions(context.Background())
	for _, item := range []struct{ adapter, id, code string }{{"tasks", "s", "invalid_request"}, {"memory", "m", "unsupported_feature"}} {
		if _, _, err := hub.Open(context.Background(), item.adapter, base.OpenRequest{SessionID: protocol.SessionID(item.id)}); err != nil {
			t.Fatal(err)
		}
		from := uint64(1)
		e := requestEnvelope(t, protocol.TypeSessionTaskOutputRequest, "bad", protocol.SessionTaskOutputRequest{SessionID: protocol.SessionID(item.id), TaskID: "task-1", From: &from}, item.id, "", "")
		status, response := postEnvelope(t, server, "/sessions/"+item.id+"/task-output", e)
		var refusal protocol.ErrorResponse
		if status != http.StatusBadRequest || response.DecodePayload(&refusal) != nil || refusal.Error.Code != item.code {
			t.Fatalf("%s status %d refusal %+v", item.id, status, refusal)
		}
	}
}

func TestTaskControlsRequireEnvelopeSessionHTTP(t *testing.T) {
	hub, server := newServer(t, memoryRegistry(16), Options{})
	defer hub.CloseSessions(context.Background())
	if _, _, err := hub.Open(context.Background(), "memory", base.OpenRequest{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		typ     protocol.EnvelopeType
		payload any
	}{
		{"task-output", protocol.TypeSessionTaskOutputRequest, protocol.SessionTaskOutputRequest{SessionID: "s", TaskID: "t"}},
		{"cancel", protocol.TypeSessionTaskCancelRequest, protocol.SessionTaskCancelRequest{SessionID: "s", TaskID: "t"}},
	} {
		env, _ := protocol.NewEnvelope(tc.typ, "req", tc.payload)
		status, response := postEnvelope(t, server, "/sessions/s/"+tc.path, env)
		var refusal protocol.ErrorResponse
		if status != http.StatusBadRequest || response.DecodePayload(&refusal) != nil || refusal.Error.Code != "schema_invalid" {
			t.Fatalf("%s: status %d %+v", tc.path, status, refusal)
		}
	}
}
