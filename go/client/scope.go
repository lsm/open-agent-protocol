package client

import (
	"encoding/json"
	"fmt"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type payloadScope struct {
	SessionID  json.RawMessage `json:"session_id"`
	RunID      json.RawMessage `json:"run_id"`
	ToolCallID json.RawMessage `json:"tool_call_id"`
}

func scopeMember(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", true
	}
	return value, true
}

func payloadScopeDefect(envelope protocol.Envelope) error {
	var scope payloadScope
	if err := json.Unmarshal(envelope.Payload, &scope); err != nil {
		return nil
	}
	if value, present := scopeMember(scope.SessionID); present && value != string(envelope.SessionID) {
		return &MalformedFrameError{Detail: fmt.Sprintf("payload names session %q, envelope %q", value, envelope.SessionID)}
	}
	if value, present := scopeMember(scope.RunID); present && value != string(envelope.RunID) {
		return &MalformedFrameError{Detail: fmt.Sprintf("payload names run %q, envelope %q", value, envelope.RunID)}
	}
	if value, present := scopeMember(scope.ToolCallID); present && envelope.ToolCallID != "" && value != string(envelope.ToolCallID) {
		return &MalformedFrameError{Detail: fmt.Sprintf("payload names tool call %q, envelope %q", value, envelope.ToolCallID)}
	}
	return nil
}

func errorResponseScopeDefect(path string, failure, request protocol.Envelope) error {
	if request.SessionID != "" && failure.SessionID != request.SessionID {
		return fmt.Errorf("client: %s error response is scoped to session %q, want %q", path, failure.SessionID, request.SessionID)
	}
	if request.RunID != "" && failure.RunID != request.RunID {
		return fmt.Errorf("client: %s error response is scoped to run %q, want %q", path, failure.RunID, request.RunID)
	}
	return nil
}
