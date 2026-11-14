package serveendpoint

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lsm/oxap/go/protocol"
)

type replayLine struct {
	Control  string          `json:"control"`
	Type     string          `json:"type"`
	RunID    protocol.RunID  `json:"run_id"`
	Sequence uint64          `json:"sequence"`
	Payload  json.RawMessage `json:"payload"`
}

func (l *liveEndpoint) until(found func(replayLine) bool) replayLine {
	l.t.Helper()
	for {
		read := make(chan string, 1)
		go func() {
			text, _ := l.answer.ReadString('\n')
			read <- text
		}()
		select {
		case text := <-read:
			var line replayLine
			if err := json.Unmarshal([]byte(text), &line); err != nil {
				l.t.Fatalf("line %q: %v", text, err)
			}
			if found(line) {
				return line
			}
		case <-time.After(10 * time.Second):
			l.t.Fatal("the line never came")
		}
	}
}

func (l *liveEndpoint) settledRun(session protocol.SessionID, id string) (protocol.RunID, uint64) {
	l.t.Helper()
	admitted := l.ask(requestLine(l.t, protocol.TypeSessionMessageSubmitRequest, "submit-"+id, protocol.MessageSubmitRequest{
		SessionID: session, Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}, session), protocol.EnvelopeID("submit-"+id))
	run := admitted.RunID
	if _, err := l.stdin.Write([]byte(requestLine(l.t, protocol.TypeRunCancelRequest, "cancel-"+id, protocol.RunCancelRequest{SessionID: session, RunID: run}, session) + "\n")); err != nil {
		l.t.Fatal(err)
	}
	terminal := l.until(func(line replayLine) bool {
		return line.Type == string(protocol.TypeRunCancelled) && line.RunID == run
	})
	return run, terminal.Sequence
}

func TestAReplayAtItsRunsTerminalEndsItsStream(t *testing.T) {
	live := startLiveEndpoint(t)
	live.ask(requestLine(t, protocol.TypeSessionOpenRequest, "open-1", protocol.SessionOpenRequest{SessionID: "settled"}, "settled"), "open-1")
	first, reached := live.settledRun("settled", "1")
	live.settledRun("settled", "2")

	replay := fmt.Sprintf(`{"control":"replay","id":"r1","session_id":"settled","run_id":%q,"after":%d}`, first, reached)
	if _, err := live.stdin.Write([]byte(replay + "\n")); err != nil {
		t.Fatal(err)
	}
	live.until(func(line replayLine) bool { return line.Control == controlReplayAccepted })
	time.Sleep(50 * time.Millisecond)

	ended := make(chan struct{})
	go func() {
		live.server.pumps.Wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("a replay with nothing left to send kept its stream open")
	}
}
