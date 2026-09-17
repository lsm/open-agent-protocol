package serveendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// parkingWriter accepts a few writes and then blocks forever, which is what a
// pipe does once the host stops reading it and the buffer fills.
type parkingWriter struct {
	mu       sync.Mutex
	accepted int
	limit    int
	parked   chan struct{}
	once     sync.Once
}

func (w *parkingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.accepted++
	over := w.accepted > w.limit
	w.mu.Unlock()
	if !over {
		return len(p), nil
	}
	w.once.Do(func() { close(w.parked) })
	select {}
}

// TestTeardownStopsWhenAWriteParksForever is the hung-up host the binding
// describes: alive, stdin closed, no longer reading stdout.
//
// The run pump parks inside its write holding the writer lock, so a teardown
// that then took that lock to flush would wait on a pump it had just given up
// on — turning the bounded wait into the very hang the bound exists to
// prevent. Signals are captured by the caller, so nothing but SIGKILL would
// recover, and that skips the session sweep.
func TestTeardownStopsWhenAWriteParksForever(t *testing.T) {
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	server, err := New(hub, Options{Adapter: "memory", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	open := requestLine(t, protocol.TypeSessionOpenRequest, "open-1",
		protocol.SessionOpenRequest{SessionID: "parked"}, "parked")
	submit := requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-1",
		protocol.MessageSubmitRequest{
			SessionID: "parked", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}, "parked")

	// The two responses are let through; the run's first event is what parks.
	out := &parkingWriter{limit: 2, parked: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- server.Run(context.Background(), strings.NewReader(open+"\n"+submit+"\n"), out)
	}()

	select {
	case <-out.parked:
	case <-time.After(10 * time.Second):
		t.Fatal("the endpoint never reached a parked write, so this test proved nothing")
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownStalled) {
			t.Fatalf("Run returned %v, want ErrShutdownStalled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run never returned: the teardown blocked on the writer lock the abandoned pump still holds")
	}
}

func requestLine(t *testing.T, typ protocol.EnvelopeType, id string, payload any, session protocol.SessionID) string {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(id), payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = session
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%s", data)
}

// TestPipelinedRequestsStayCancellableWhileTheWriterIsParked is the case a
// mutex-guarded writer could not survive.
//
// Requests may be pipelined, so more of them arrive while a run pump is
// already parked writing to a pipe nobody is draining. Serialising writes
// behind a lock meant the next handler blocked on the lock the parked pump
// held, the read loop never came back round, and stdin EOF and SIGINT alike
// went unseen — leaving SIGKILL, which skips the session sweep. Handing lines
// to a writer goroutine instead means a producer waits on a channel it can
// select against, so the loop stays answerable to its context no matter how
// wedged the pipe is.
func TestPipelinedRequestsStayCancellableWhileTheWriterIsParked(t *testing.T) {
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	server, err := New(hub, Options{Adapter: "memory", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	var input strings.Builder
	input.WriteString(requestLine(t, protocol.TypeSessionOpenRequest, "open-1",
		protocol.SessionOpenRequest{SessionID: "wedged"}, "wedged") + "\n")
	input.WriteString(requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-1",
		protocol.MessageSubmitRequest{
			SessionID: "wedged", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}, "wedged") + "\n")
	// Comfortably more than the write queue holds, so the loop is forced to
	// wait on a send rather than slipping every answer into the buffer.
	for i := 0; i < writeQueue*4; i++ {
		input.WriteString(requestLine(t, protocol.TypeSessionStateRequest,
			fmt.Sprintf("state-%d", i),
			protocol.SessionStateRequest{SessionID: "wedged"}, "wedged") + "\n")
	}

	out := &parkingWriter{limit: 2, parked: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, strings.NewReader(input.String()), out) }()

	select {
	case <-out.parked:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("the endpoint never reached a parked write, so this test proved nothing")
	}

	// Stand in for the signal the operator sends when the host has wedged.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run ignored its cancelled context: a handler is blocked behind the parked writer")
	}
}
