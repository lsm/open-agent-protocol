package servestdio

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestEventsSignalAClosedSessionByName(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	session := openSessionEntry(t, hub, "closed-under-stream")
	f := startFrontend(t, hub, Options{})
	f.send(fmt.Sprintf(`{"id":7,"op":"events","session_id":%q}`, "closed-under-stream"))
	if response := f.expectResponse(7); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	signal := f.expectSignal(7, signalSubscribed)
	if signal.SessionID != "closed-under-stream" {
		t.Fatalf("the subscribed signal names %q", signal.SessionID)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var closed sessionClosedLine
	if err := json.Unmarshal([]byte(f.line()), &closed); err != nil {
		t.Fatal(err)
	}
	if closed.Event != signalSessionClosed {
		t.Fatalf("the line after the close is %q, want %q", closed.Event, signalSessionClosed)
	}
	if closed.SessionID != "closed-under-stream" {
		t.Fatalf("the closing signal names %q, want the session it ended", closed.SessionID)
	}
	if closed.Message == "" {
		t.Fatal("the closing signal carries no message")
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestEventsSignalAnOverflowWithItsRunAndCursor(t *testing.T) {
	hub := newTestHub(t, 64, 1)
	session := openSessionEntry(t, hub, "flooded")
	f := startFrontend(t, hub, Options{WriteQueue: 1})
	f.send(fmt.Sprintf(`{"id":9,"op":"events","session_id":%q}`, "flooded"))
	if response := f.expectResponse(9); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	f.expectSignal(9, signalSubscribed)

	var overflow *overflowLine
	for i := 0; i < 64 && overflow == nil; i++ {
		_, _ = runToCompletion(t, hub, session)
		var line struct {
			Event string `json:"event"`
		}
		raw := f.line()
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("signal line %q: %v", raw, err)
		}
		switch line.Event {
		case signalEnvelope:
		case signalOverflow:
			overflow = &overflowLine{}
			if err := json.Unmarshal([]byte(raw), overflow); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("line %d is %q, want envelopes until the overflow", i, line.Event)
		}
	}
	if overflow == nil {
		t.Fatal("the stream never overflowed, so the signal was never driven")
	}
	if overflow.SessionID != "flooded" {
		t.Fatalf("the overflow names %q, want the session it ended", overflow.SessionID)
	}
	if overflow.RunID == "" {
		t.Fatal("the overflow names no run, so a client could not resume against it")
	}
	if overflow.Message == "" {
		t.Fatal("the overflow carries no message")
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestTheFrameLimitSignalCarriesItsRunAndSequence(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	session := openSessionEntry(t, hub, "wide-join")
	runID, lastSequence := runToCompletion(t, hub, session)

	f := startFrontend(t, hub, Options{FrameLimit: 256})
	f.send(fmt.Sprintf(`{"id":11,"op":"events","session_id":%q}`, "wide-join"))
	if response := f.expectResponse(11); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	f.expectSignal(11, signalSubscribed)
	_, _ = runToCompletion(t, hub, session)
	raw := f.line()
	var limited frameLimitLine
	if err := json.Unmarshal([]byte(raw), &limited); err != nil {
		t.Fatal(err)
	}
	if limited.Event != signalFrameLimit {
		t.Fatalf("the line is %q, want %q", limited.Event, signalFrameLimit)
	}
	if limited.RunID == "" {
		t.Fatalf("the frame-limit signal names no run: %s", raw)
	}
	if protocol.RunID(limited.RunID) != runID {
		t.Fatalf("the frame-limit signal names run %q, want the run it stopped in (%q)", limited.RunID, runID)
	}
	if limited.Sequence != lastSequence {
		t.Fatalf("the frame-limit signal names sequence %d, want %d", limited.Sequence, lastSequence)
	}
	if limited.Message == "" {
		t.Fatal("the frame-limit signal carries no message")
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}
