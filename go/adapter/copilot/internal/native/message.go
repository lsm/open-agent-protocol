package native

import (
	"encoding/json"
	"errors"

	"github.com/lsm/open-agent-protocol/go/internal/jsonwalk"
)

var ErrWire = errors.New("invalid Copilot JSON-RPC frame")

type Kind string

const (
	Request      Kind = "request"
	Notification Kind = "notification"
	Response     Kind = "response"
	Failure      Kind = "failure"
)

type Message struct {
	Kind   Kind
	ID     json.RawMessage
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Code   int64
	Error  string
}

func Parse(frame []byte) (Message, error) {
	if jsonwalk.RejectDuplicateKeys(frame) != nil {
		return Message{}, ErrWire
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(frame, &object) != nil || object == nil || Text(object, "jsonrpc") != "2.0" {
		return Message{}, ErrWire
	}
	m := Message{ID: object["id"], Params: object["params"], Result: object["result"]}
	if m.ID != nil && !validID(m.ID) {
		return Message{}, ErrWire
	}
	method, hasMethod := object["method"]
	failure, hasFailure := object["error"]
	_, hasResult := object["result"]
	if hasMethod {
		if json.Unmarshal(method, &m.Method) != nil || m.Method == "" || hasResult || hasFailure {
			return Message{}, ErrWire
		}
		m.Kind = Notification
		if m.ID != nil {
			m.Kind = Request
		}
		return m, nil
	}
	if m.ID == nil || hasResult == hasFailure {
		return Message{}, ErrWire
	}
	m.Kind = Response
	if hasFailure {
		var body map[string]json.RawMessage
		if json.Unmarshal(failure, &body) != nil || body == nil || json.Unmarshal(body["code"], &m.Code) != nil || string(body["code"]) == "null" {
			return Message{}, ErrWire
		}
		m.Error = Text(body, "message")
		if m.Error == "" {
			return Message{}, ErrWire
		}
		m.Kind = Failure
	}
	return m, nil
}

func validID(raw json.RawMessage) bool {
	if len(raw) > 0 && raw[0] == '"' {
		var id string
		return json.Unmarshal(raw, &id) == nil
	}
	var id int64
	return string(raw) != "null" && json.Unmarshal(raw, &id) == nil
}

func Text(object map[string]json.RawMessage, name string) string {
	var text string
	_ = json.Unmarshal(object[name], &text)
	return text
}
