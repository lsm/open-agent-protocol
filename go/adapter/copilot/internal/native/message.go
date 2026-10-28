package native

import (
	"encoding/json"
	"errors"
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
	return Message{}, ErrWire
}
