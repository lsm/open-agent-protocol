package servestdio

import (
	"context"
	"encoding/json"
	"fmt"
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

func (s taskSession) TaskOutput(_ context.Context, id protocol.TaskID, from *uint64) (protocol.SessionTaskOutputResponse, error) {
	if id != "t" {
		return protocol.SessionTaskOutputResponse{}, base.ErrInvalidSubmission
	}
	if from == nil || *from != 2 {
		return protocol.SessionTaskOutputResponse{}, base.ErrInvalidRequest
	}
	return protocol.SessionTaskOutputResponse{Output: "é", Next: 4, Ended: true}, nil
}
func TestTaskOutputStdio(t *testing.T) {
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
	from := uint64(2)
	for _, tc := range []struct{ header, payload, code string }{{"s", "s", ""}, {"s", "other", "scope_mismatch"}, {"other", "s", "scope_mismatch"}} {
		env, _ := protocol.NewEnvelope(protocol.TypeSessionTaskOutputRequest, "out", protocol.SessionTaskOutputRequest{SessionID: protocol.SessionID(tc.payload), TaskID: "t", From: &from})
		env.SessionID = protocol.SessionID(tc.header)
		raw, _ := json.Marshal(env)
		answer, werr := server.dispatch(context.Background(), requestLine{Op: "task_output", SessionID: "s", Request: raw})
		if tc.code != "" {
			if werr == nil || werr.Code != tc.code {
				t.Fatalf("want %s got %+v", tc.code, werr)
			}
			continue
		}
		if werr != nil {
			t.Fatalf("output: %+v", werr)
		}
		var response protocol.Envelope
		if err := json.Unmarshal(answer, &response); err != nil {
			t.Fatal(err)
		}
		var got protocol.SessionTaskOutputResponse
		if response.Type != protocol.TypeSessionTaskOutputResponse || response.InReplyTo != "out" || response.DecodePayload(&got) != nil || got.Output != "é" || got.Next != 4 || !got.Ended {
			t.Fatalf("response %+v output %+v", response, got)
		}
	}
}

func TestTaskOutputStdioRefusals(t *testing.T) {
	registry := serve.NewRegistry()
	_ = registry.Register("tasks", taskAdapter{base.NewMemory(base.Config{})})
	_ = registry.Register("memory", base.NewMemory(base.Config{}))
	hub := serve.New(registry, serve.Options{})
	defer hub.CloseSessions(context.Background())
	for _, item := range []struct{ adapter, id, code string }{{"tasks", "s", "invalid_request"}, {"memory", "m", "unsupported_feature"}} {
		if _, _, err := hub.Open(context.Background(), item.adapter, base.OpenRequest{SessionID: protocol.SessionID(item.id)}); err != nil {
			t.Fatal(err)
		}
		server, err := New(hub, Options{})
		if err != nil {
			t.Fatal(err)
		}
		from := uint64(1)
		e, _ := protocol.NewEnvelope(protocol.TypeSessionTaskOutputRequest, "bad", protocol.SessionTaskOutputRequest{SessionID: protocol.SessionID(item.id), TaskID: "t", From: &from})
		e.SessionID = protocol.SessionID(item.id)
		raw, _ := json.Marshal(e)
		_, werr := server.dispatch(context.Background(), requestLine{Op: "task_output", SessionID: item.id, Request: raw})
		if werr == nil || werr.Code != item.code {
			t.Fatalf("%s: %+v", item.id, werr)
		}
	}
}

func TestTaskOutputStdioWire(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("tasks", taskAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	defer hub.CloseSessions(context.Background())
	if _, _, err := hub.Open(context.Background(), "tasks", base.OpenRequest{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	f := startFrontend(t, hub, Options{})
	from := uint64(2)
	env, _ := protocol.NewEnvelope(protocol.TypeSessionTaskOutputRequest, "out", protocol.SessionTaskOutputRequest{SessionID: "s", TaskID: "t", From: &from})
	env.SessionID = "s"
	raw, _ := json.Marshal(env)
	f.send(fmt.Sprintf(`{"id":1,"op":"task_output","session_id":"s","request":%s}`, raw))
	response := f.expectResponse(1)
	requireOK(t, response)
	var envelope protocol.Envelope
	if err := json.Unmarshal(response.Result, &envelope); err != nil {
		t.Fatal(err)
	}
	var result protocol.SessionTaskOutputResponse
	if envelope.Type != protocol.TypeSessionTaskOutputResponse || envelope.InReplyTo != "out" || envelope.DecodePayload(&result) != nil || result.Output != "é" || result.Next != 4 {
		t.Fatalf("envelope %+v result %+v", envelope, result)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}
