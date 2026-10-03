package serveendpoint

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestASettingsUpdateAnswersThenPublishesTheStateItLeft(t *testing.T) {
	registry, err := serve.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(serve.New(registry, serve.Options{}), Options{Adapter: "memory", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	policy := &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 9}
	input := strings.Join([]string{
		requestLine(t, protocol.TypeSessionOpenRequest, "open-1", protocol.SessionOpenRequest{SessionID: "live"}, "live"),
		requestLine(t, protocol.TypeSessionSettingsUpdateRequest, "update-policy", protocol.SessionSettingsUpdateRequest{SessionID: "live", CompactionPolicy: policy}, "live"),
		requestLine(t, protocol.TypeSessionSettingsUpdateRequest, "update-level", protocol.SessionSettingsUpdateRequest{SessionID: "live", ReasoningLevel: protocol.ReasoningHigh}, "live"),
	}, "\n") + "\n"
	out := &syncBuffer{}
	_ = server.Run(context.Background(), strings.NewReader(input), out)

	var answered, published bool
	for _, raw := range strings.Split(out.String(), "\n") {
		var envelope protocol.Envelope
		if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &envelope) != nil {
			continue
		}
		switch envelope.Type {
		case protocol.TypeSessionSettingsUpdateResponse:
			var response protocol.SessionSettingsUpdateResponse
			if err := envelope.DecodePayload(&response); err != nil {
				t.Fatal(err)
			}
			if envelope.InReplyTo != "update-policy" || response.CompactionPolicy == nil || *response.CompactionPolicy != *policy ||
				response.PreviousCompactionPolicy == nil || response.PreviousCompactionPolicy.Kind != protocol.CompactionAuto {
				t.Fatalf("update response = %s, want tokens 9 replacing auto", raw)
			}
			answered = true
		case protocol.TypeSessionStateUpdated:
			var state protocol.SessionState
			if err := envelope.DecodePayload(&state); err != nil {
				t.Fatal(err)
			}
			if !answered || state.CompactionPolicy == nil || *state.CompactionPolicy != *policy || envelope.Sequence == nil {
				t.Fatalf("state update = %s, want the new policy after the response", raw)
			}
			published = true
		}
	}
	if !answered || !published {
		t.Fatalf("answered=%v published=%v:\n%s", answered, published, out.String())
	}
	if code := refusalFor(t, out.String(), "update-level"); code != "unsupported_feature" {
		t.Fatalf("a reasoning level the memory adapter does not take live was refused %q, want unsupported_feature", code)
	}
}
