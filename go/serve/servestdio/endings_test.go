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
	var overflow *overflowLine
	var delivered uint64
	for i := 0; i < 64 && overflow == nil; i++ {
		_, _ = runToCompletion(t, hub, session)
		raw := f.line()
		var line struct {
			Event    string  `json:"event"`
			Sequence *uint64 `json:"sequence"`
		}
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("signal line %q: %v", raw, err)
		}
		switch line.Event {
		case signalEnvelope:
			if line.Sequence != nil {
				delivered = *line.Sequence
			}
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
	if overflow.LastSequence < delivered {
		t.Fatalf("the overflow resumes after %d, want at or past the %d the stream delivered: a cursor behind what the client already has would replay events it saw", overflow.LastSequence, delivered)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestTheFrameLimitSignalCarriesItsRunAndSequence(t *testing.T) {
	const wide = "joined-0d8f1b2c-3e4a-4b5c-8d9e-0f1a2b3c4d5e"
	hub := newTestHub(t, 64, 64)
	session := openSessionEntry(t, hub, wide)
	runToCompletion(t, hub, session)

	f := startFrontend(t, hub, Options{FrameLimit: 256})
	f.send(fmt.Sprintf(`{"id":11,"op":"events","session_id":%q}`, wide))
	if response := f.expectResponse(11); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	f.expectSignal(11, signalSubscribed)

	// the session id is wide enough that the frames the live pump writes exceed
	// the limit, so the second run's first event is the one that cannot be
	// carried; the signal names the run it stopped in and the sequence a fresh
	// cursor resumes at, which is the event it could not deliver.
	framed, _ := runToCompletion(t, hub, session)
	var delivered uint64
	var limited frameLimitLine
	for i := 0; i < 64; i++ {
		raw := f.line()
		var line struct {
			Event    string  `json:"event"`
			Sequence *uint64 `json:"sequence"`
		}
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("signal line %q: %v", raw, err)
		}
		if line.Event == signalEnvelope {
			if line.Sequence != nil {
				delivered = *line.Sequence
			}
			continue
		}
		if err := json.Unmarshal([]byte(raw), &limited); err != nil {
			t.Fatal(err)
		}
		break
	}
	if limited.Event != signalFrameLimit {
		t.Fatalf("the stream ended with %q, want %q", limited.Event, signalFrameLimit)
	}
	if limited.SessionID != wide {
		t.Fatalf("the frame-limit signal names %q, want the session it ended", limited.SessionID)
	}
	if protocol.RunID(limited.RunID) != framed {
		t.Fatalf("the frame-limit signal names run %q, want the run it stopped in (%q)", limited.RunID, framed)
	}
	if limited.Sequence != delivered+1 {
		t.Fatalf("the frame-limit signal resumes at %d after %d was delivered, want the event it could not carry", limited.Sequence, delivered)
	}
	if limited.Message == "" {
		t.Fatal("the frame-limit signal carries no message")
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}
