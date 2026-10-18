package serveendpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type liveEndpoint struct {
	t      *testing.T
	stdin  *io.PipeWriter
	answer *bufio.Reader
	done   chan error
}

func startLiveEndpoint(t *testing.T) *liveEndpoint {
	t.Helper()
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(serve.New(registry, serve.Options{}), Options{Adapter: "memory", Shutdown: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), inReader, outWriter) }()
	t.Cleanup(func() { _ = inWriter.Close(); _ = outReader.Close() })
	return &liveEndpoint{t: t, stdin: inWriter, answer: bufio.NewReader(outReader), done: done}
}

func (l *liveEndpoint) ask(line string, inReplyTo protocol.EnvelopeID) protocol.Envelope {
	l.t.Helper()
	if _, err := l.stdin.Write([]byte(line + "\n")); err != nil {
		l.t.Fatal(err)
	}
	for {
		read := make(chan string, 1)
		go func() {
			text, _ := l.answer.ReadString('\n')
			read <- text
		}()
		var text string
		select {
		case text = <-read:
		case <-time.After(10 * time.Second):
			l.t.Fatalf("no answer to %s", inReplyTo)
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal([]byte(text), &envelope); err != nil {
			l.t.Fatalf("line %q: %v", text, err)
		}
		if envelope.InReplyTo == inReplyTo {
			return envelope
		}
	}
}

func (l *liveEndpoint) stops() {
	l.t.Helper()
	select {
	case err := <-l.done:
		if err != nil {
			l.t.Fatalf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		l.t.Fatal("the endpoint kept serving after the shutdown completed")
	}
}

func shutdownOutcome(t *testing.T, envelope protocol.Envelope) protocol.EndpointShutdownResponse {
	t.Helper()
	if envelope.Type != protocol.TypeEndpointShutdownResponse {
		t.Fatalf("answered %s, want endpoint.shutdown.response", envelope.Type)
	}
	var answer protocol.EndpointShutdownResponse
	if err := envelope.DecodePayload(&answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func refusedCode(t *testing.T, envelope protocol.Envelope) string {
	t.Helper()
	var refused protocol.ErrorResponse
	if err := envelope.DecodePayload(&refused); err != nil {
		t.Fatal(err)
	}
	return refused.Error.Code
}

func TestAnEndpointShutdownWithNothingRunningStopsAtOnce(t *testing.T) {
	live := startLiveEndpoint(t)
	answer := shutdownOutcome(t, live.ask(requestLine(t, protocol.TypeEndpointShutdownRequest, "sd-1", protocol.EndpointShutdownRequest{}, ""), "sd-1"))
	if answer.Outcome != protocol.ShutdownStopping {
		t.Fatalf("idle shutdown answered %s, want stopping", answer.Outcome)
	}
	live.stops()
}

func TestAnEndpointShutdownAsksWhileARunIsActiveThenGracefulRefusesNewWorkAndForceStops(t *testing.T) {
	live := startLiveEndpoint(t)
	live.ask(requestLine(t, protocol.TypeSessionOpenRequest, "open-1", protocol.SessionOpenRequest{SessionID: "busy"}, "busy"), "open-1")
	live.ask(requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-1", protocol.MessageSubmitRequest{
		SessionID: "busy", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}, "busy"), "submit-1")

	asked := shutdownOutcome(t, live.ask(requestLine(t, protocol.TypeEndpointShutdownRequest, "sd-1", protocol.EndpointShutdownRequest{}, ""), "sd-1"))
	if asked.Outcome != protocol.ShutdownChoose || len(asked.ActiveRuns) != 1 {
		t.Fatalf("shutdown with a run answered %+v, want choose listing it", asked)
	}
	graceful := shutdownOutcome(t, live.ask(requestLine(t, protocol.TypeEndpointShutdownRequest, "sd-2", protocol.EndpointShutdownRequest{Mode: protocol.ShutdownGraceful}, ""), "sd-2"))
	if graceful.Outcome != protocol.ShutdownDraining {
		t.Fatalf("graceful answered %s, want draining", graceful.Outcome)
	}
	refused := live.ask(requestLine(t, protocol.TypeSessionOpenRequest, "open-2", protocol.SessionOpenRequest{SessionID: "late"}, "late"), "open-2")
	if code := refusedCode(t, refused); code != "shutting_down" {
		t.Fatalf("an open after the shutdown was answered %s, want shutting_down", code)
	}
	forced := shutdownOutcome(t, live.ask(requestLine(t, protocol.TypeEndpointShutdownRequest, "sd-3", protocol.EndpointShutdownRequest{Mode: protocol.ShutdownForce}, ""), "sd-3"))
	if forced.Outcome != protocol.ShutdownStopping {
		t.Fatalf("force answered %s, want stopping", forced.Outcome)
	}
	live.stops()
}

func TestAnEndpointShutdownRefusesAModeOtherThanGracefulOrForce(t *testing.T) {
	live := startLiveEndpoint(t)
	refused := live.ask(requestLine(t, protocol.TypeEndpointShutdownRequest, "sd-1", map[string]string{"mode": "later"}, ""), "sd-1")
	if code := refusedCode(t, refused); code != "invalid_payload" {
		t.Fatalf("an unknown mode was answered %s, want invalid_payload", code)
	}
	if live.ask(requestLine(t, protocol.TypeCapabilitiesRequest, "cap-1", map[string]any{}, ""), "cap-1").Type != protocol.TypeCapabilitiesResponse {
		t.Fatal("the endpoint stopped serving after a refused shutdown")
	}
}
