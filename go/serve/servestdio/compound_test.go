package servestdio

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func compoundOpenLine(t *testing.T, id int64, sessionID string, request protocol.SessionOpenRequest) string {
	t.Helper()
	request.SessionID = protocol.SessionID(sessionID)
	envelope := requestEnvelope(t, "req-open", protocol.TypeSessionOpenRequest, request, "", "")
	return fmt.Sprintf(`{"id":%d,"op":"open","adapter":"memory","request":%s}`, id, envelope)
}

func scriptedOpenMessage() *protocol.OpenMessage {
	return &protocol.OpenMessage{
		Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("drive the scripted run")}},
	}
}

func openedState(t *testing.T, result json.RawMessage) protocol.SessionOpenResponse {
	t.Helper()
	var envelope protocol.Envelope
	if err := json.Unmarshal(result, &envelope); err != nil {
		t.Fatal(err)
	}
	var state protocol.SessionOpenResponse
	if err := envelope.DecodePayload(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestOpenOpCarriesItsMessage(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})

	f.send(compoundOpenLine(t, 1, "stdio-carry", protocol.SessionOpenRequest{Message: scriptedOpenMessage()}))
	response := f.expectResponse(1)
	if !response.OK {
		t.Fatalf("compound open failed: %+v", response.Error)
	}
	state := openedState(t, response.Result)
	if len(state.ActiveRuns) != 1 {
		t.Fatalf("the open reports %d active runs, want the one its message admitted: %+v", len(state.ActiveRuns), state.ActiveRuns)
	}
	admitted := state.ActiveRuns[0]
	if admitted.RunID == "" {
		t.Fatalf("the admitted run is unnamed: %+v", admitted)
	}
	if len(admitted.AdmittedSubmitRequests) != 1 || admitted.AdmittedSubmitRequests[0] != "req-open" {
		t.Fatalf("the admitted run cites %v, want the open request", admitted.AdmittedSubmitRequests)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func drainSubscription(t *testing.T, f *frontend, id int64, session string) (uint64, protocol.EnvelopeType) {
	t.Helper()
	var first, lastSequence uint64
	var terminal protocol.EnvelopeType
	nextID := id + 1
	sent, answered := 0, 0
	for terminal == "" || answered < sent {
		line := f.line()
		if strings.HasPrefix(line, `{"id":`) {
			response := f.decodeResponse(line)
			if !response.OK {
				t.Fatalf("op %d failed: %+v", response.ID, response.Error)
			}
			answered++
			continue
		}
		var signal signalLine
		if err := json.Unmarshal([]byte(line), &signal); err != nil {
			t.Fatal(err)
		}
		if signal.Event != signalEnvelope {
			t.Fatalf("unexpected signal %q on a healthy stream: %s", signal.Event, line)
		}
		if signal.ID != id {
			t.Fatalf("envelope line correlated to %d, want the open request %d", signal.ID, id)
		}
		if signal.SessionID != session {
			t.Fatalf("envelope line names session %q, want %q", signal.SessionID, session)
		}
		if signal.Sequence == nil || *signal.Sequence <= lastSequence {
			t.Fatalf("sequence %v does not advance past %d", signal.Sequence, lastSequence)
		}
		if first == 0 {
			first = *signal.Sequence
		}
		lastSequence = *signal.Sequence
		var envelope protocol.Envelope
		if err := json.Unmarshal(signal.Envelope, &envelope); err != nil {
			t.Fatal(err)
		}
		switch envelope.Type {
		case protocol.TypeActionPermissionRequested, protocol.TypeUserInputRequested:
			f.send(fmt.Sprintf(`{"id":%d,"op":"resolve","session_id":%q,"request":%s}`,
				nextID, session, resolveEnvelope(t, fmt.Sprintf("req-resolve-%d", nextID), envelope, session)))
			nextID++
			sent++
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			terminal = envelope.Type
		}
	}
	return first, terminal
}

func TestOpenOpSubscribesBeforeItsMessageRuns(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})

	f.send(compoundOpenLine(t, 1, "stdio-joined", protocol.SessionOpenRequest{
		Subscribe: true,
		Message:   scriptedOpenMessage(),
	}))
	response := f.expectResponse(1)
	if !response.OK {
		t.Fatalf("compound open failed: %+v", response.Error)
	}
	state := openedState(t, response.Result)
	if len(state.ActiveRuns) != 1 {
		t.Fatalf("the open reports %d active runs: %+v", len(state.ActiveRuns), state.ActiveRuns)
	}

	first, terminal := drainSubscription(t, f, 1, "stdio-joined")
	if first != 1 {
		t.Fatalf("the compound open's stream starts at sequence %d, want the run's first envelope", first)
	}
	if terminal != protocol.TypeRunCompleted {
		t.Fatalf("run settled %s, want %s", terminal, protocol.TypeRunCompleted)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenOpWithoutSubscribeStreamsNothing(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})

	f.send(compoundOpenLine(t, 1, "stdio-quiet", protocol.SessionOpenRequest{Message: scriptedOpenMessage()}))
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("compound open failed: %+v", response.Error)
	}
	f.send(fmt.Sprintf(`{"id":2,"op":"state","session_id":%q}`, "stdio-quiet"))
	if response := f.expectResponse(2); !response.OK {
		t.Fatalf("state failed: %+v", response.Error)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}
