package serveendpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type taskCancelAdapter struct {
	*base.Memory
	events chan base.Result
}
type taskCancelSession struct {
	base.Session
	events chan base.Result
}

func (a *taskCancelAdapter) Open(ctx context.Context, req base.OpenRequest) (base.Session, error) {
	s, err := a.Memory.Open(ctx, req)
	if err != nil {
		return nil, err
	}
	return &taskCancelSession{s, a.events}, nil
}
func (s *taskCancelSession) SessionEvents() base.EventStream { return s.events }
func (s *taskCancelSession) CancelTask(_ context.Context, id protocol.TaskID) (protocol.SessionTaskCancelResponse, error) {
	e, _ := protocol.NewEnvelope(protocol.TypeSessionTaskEnded, "ended", protocol.SessionTaskEnded{TaskID: id, Status: protocol.SessionTaskEndCancelled, Cause: protocol.SessionTaskCancelledByHost})
	e.SessionID = "s"
	s.events <- base.Result{Envelope: e}
	next, _ := protocol.NewEnvelope(protocol.TypeSessionTaskUpdated, "later", protocol.SessionTaskUpdated{TaskID: "other"})
	next.SessionID = "s"
	s.events <- base.Result{Envelope: next}
	time.Sleep(100 * time.Millisecond)
	return protocol.SessionTaskCancelResponse{Outcome: protocol.SessionTaskCancelRequested}, nil
}
func TestTaskCancelAcknowledgesBeforeEnding(t *testing.T) {
	a := &taskCancelAdapter{Memory: base.NewMemory(base.Config{}), events: make(chan base.Result, 2)}
	registry := serve.NewRegistry()
	if err := registry.Register("tasks", a); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	defer hub.CloseSessions(context.Background())
	server, err := New(hub, Options{Adapter: "tasks", Shutdown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), input, output); output.Close() }()
	scan := bufio.NewScanner(reader)
	next := func() protocol.Envelope {
		t.Helper()
		if !scan.Scan() {
			t.Fatalf("missing output: %v", scan.Err())
		}
		var e protocol.Envelope
		if err := json.Unmarshal(scan.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	if _, err = io.WriteString(writer, requestLine(t, protocol.TypeSessionOpenRequest, "open", protocol.SessionOpenRequest{SessionID: "s"}, "s")+"\n"); err != nil {
		t.Fatal(err)
	}
	if e := next(); e.Type != protocol.TypeSessionOpenResponse {
		t.Fatalf("open: %+v", e)
	}
	if _, err = io.WriteString(writer, requestLine(t, protocol.TypeSessionTaskCancelRequest, "cancel", protocol.SessionTaskCancelRequest{SessionID: "s", TaskID: "t"}, "s")+"\n"); err != nil {
		t.Fatal(err)
	}
	response := next()
	if response.Type != protocol.TypeSessionTaskCancelResponse || response.InReplyTo != "cancel" {
		t.Fatalf("cancel response: %+v", response)
	}
	var ack protocol.SessionTaskCancelResponse
	if err := response.DecodePayload(&ack); err != nil || ack.Outcome != protocol.SessionTaskCancelRequested {
		t.Fatalf("ack: %+v %v", ack, err)
	}
	ended := next()
	if ended.Type != protocol.TypeSessionTaskEnded {
		t.Fatalf("ended: %+v", ended)
	}
	later := next()
	if later.Type != protocol.TypeSessionTaskUpdated || ended.Sequence == nil || later.Sequence == nil || *later.Sequence != *ended.Sequence+1 {
		t.Fatalf("session event order: ended=%+v later=%+v", ended, later)
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
