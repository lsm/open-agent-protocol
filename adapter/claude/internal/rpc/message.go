package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var (
	ErrInvalidMessage = errors.New("claude rpc: invalid stream-json message")
	ErrInvalidControl = errors.New("claude rpc: invalid control-plane message")
)

// The frame types of the pinned stream-json boundary (CLI 2.1.263).
const (
	TypeUser              = "user"
	TypeAssistant         = "assistant"
	TypeSystem            = "system"
	TypeResult            = "result"
	TypeStreamEvent       = "stream_event"
	TypeToolProgress      = "tool_progress"
	TypeCommandLifecycle  = "command_lifecycle"
	TypeKeepAlive         = "keep_alive"
	TypeConversationReset = "conversation_reset"
	TypeRateLimitEvent    = "rate_limit_event"
	TypeControlRequest    = "control_request"
	TypeControlResponse   = "control_response"
	TypeControlCancel     = "control_cancel_request"
)

type MessageKind uint8

const (
	// KindObservation covers every message-stream frame: the modeled
	// vocabulary above, and unknown types — the boundary is documented as
	// forward-compatible and both reference hosts ignore unrecognized types,
	// so the adapter records them as observations instead of failing.
	KindObservation MessageKind = iota + 1
	// KindControlRequest is a reverse control_request from the CLI (the
	// permission ask) that must be answered with exactly one control_response.
	KindControlRequest
	// KindControlResponse answers a control_request this side issued.
	KindControlResponse
	// KindControlCancel withdraws an in-flight reverse request the CLI
	// originated; there is no reply to the cancel itself.
	KindControlCancel
)

// ControlResponseEnvelope is the parsed `response` member of a
// control_response frame.
type ControlResponseEnvelope struct {
	Success   bool
	RequestID string
	Response  json.RawMessage // success payload (may be absent)
	Error     string          // error payload
}

// Message is one decoded stream-json frame. Observations keep their full raw
// bytes for the typed vocabulary layer; control frames carry their parsed
// correlation state.
type Message struct {
	Kind      MessageKind
	Type      string
	Subtype   string
	RequestID string // control_request / control_cancel_request
	Response  *ControlResponseEnvelope
	Raw       json.RawMessage // full frame bytes (observations and control requests)
}

// UserTurnMessage wraps a complete inbound user frame
// ({"type":"user","message":{...},...}) for writing.
func UserTurnMessage(frame json.RawMessage) Message {
	return Message{Kind: KindObservation, Type: TypeUser, Raw: append(json.RawMessage(nil), frame...)}
}

// ControlRequestMessage builds a host-originated control_request envelope.
// request is the request object ({"subtype":...,...}).
func ControlRequestMessage(id string, request json.RawMessage) Message {
	return Message{Kind: KindControlRequest, RequestID: id, Raw: append(json.RawMessage(nil), request...)}
}

// ControlResponseMessage builds a control_response envelope. response is the
// complete response object ({"subtype":"success"|"error","request_id":...,
// ["response"|"error"]:...}).
func ControlResponseMessage(response json.RawMessage) Message {
	return Message{Kind: KindControlResponse, Response: &ControlResponseEnvelope{}, Raw: append(json.RawMessage(nil), response...)}
}

func ParseMessage(data []byte) (Message, error) {
	object, err := parseObject(data)
	if err != nil {
		return Message{}, err
	}
	var message Message
	message.Raw = append(json.RawMessage(nil), data...)

	typeRaw, ok := object["type"]
	if !ok {
		return Message{}, fmt.Errorf("%w: type is required", ErrInvalidMessage)
	}
	if err := json.Unmarshal(typeRaw, &message.Type); err != nil || message.Type == "" {
		return Message{}, fmt.Errorf("%w: type must be a non-empty string", ErrInvalidMessage)
	}
	if subtypeRaw, has := object["subtype"]; has {
		if err := json.Unmarshal(subtypeRaw, &message.Subtype); err != nil || message.Subtype == "" {
			return Message{}, fmt.Errorf("%w: subtype must be a non-empty string", ErrInvalidMessage)
		}
	}

	switch message.Type {
	case TypeControlRequest:
		message.Kind = KindControlRequest
		if err := json.Unmarshal(object["request_id"], &message.RequestID); err != nil || message.RequestID == "" {
			return Message{}, fmt.Errorf("%w: control_request requires a non-empty request_id", ErrInvalidControl)
		}
		request, err := requireObject(object, "request")
		if err != nil {
			return Message{}, fmt.Errorf("%w: %v", ErrInvalidControl, err)
		}
		var requestSubtype string
		if err := json.Unmarshal(request["subtype"], &requestSubtype); err != nil || requestSubtype == "" {
			return Message{}, fmt.Errorf("%w: control request requires a non-empty subtype", ErrInvalidControl)
		}
		message.Subtype = requestSubtype
	case TypeControlResponse:
		message.Kind = KindControlResponse
		response, err := requireObject(object, "response")
		if err != nil {
			return Message{}, fmt.Errorf("%w: %v", ErrInvalidControl, err)
		}
		envelope := &ControlResponseEnvelope{}
		var state string
		if err := json.Unmarshal(response["subtype"], &state); err != nil || state == "" {
			return Message{}, fmt.Errorf("%w: control response requires a non-empty subtype", ErrInvalidControl)
		}
		if err := json.Unmarshal(response["request_id"], &envelope.RequestID); err != nil || envelope.RequestID == "" {
			return Message{}, fmt.Errorf("%w: control response requires a non-empty request_id", ErrInvalidControl)
		}
		switch state {
		case "success":
			envelope.Success = true
			if payload, has := response["response"]; has && !bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
				envelope.Response = cloneRaw(payload)
			}
		case "error":
			var message string
			if err := json.Unmarshal(response["error"], &message); err != nil || message == "" {
				return Message{}, fmt.Errorf("%w: error control response requires a non-empty error", ErrInvalidControl)
			}
			envelope.Error = message
		default:
			return Message{}, fmt.Errorf("%w: control response subtype %q is not success or error", ErrInvalidControl, state)
		}
		message.Response = envelope
	case TypeControlCancel:
		message.Kind = KindControlCancel
		if err := json.Unmarshal(object["request_id"], &message.RequestID); err != nil || message.RequestID == "" {
			return Message{}, fmt.Errorf("%w: control_cancel_request requires a non-empty request_id", ErrInvalidControl)
		}
	default:
		message.Kind = KindObservation
	}
	return message, nil
}

func (message Message) MarshalJSON() ([]byte, error) {
	switch message.Kind {
	case KindControlRequest:
		if message.RequestID == "" || len(message.Raw) == 0 {
			return nil, ErrInvalidMessage
		}
		request, err := parseObject(message.Raw)
		if err != nil {
			return nil, err
		}
		if _, ok := request["subtype"]; !ok {
			return nil, fmt.Errorf("%w: control request requires a subtype", ErrInvalidControl)
		}
		return json.Marshal(map[string]any{
			"type":       TypeControlRequest,
			"request_id": message.RequestID,
			"request":    request,
		})
	case KindControlResponse:
		if len(message.Raw) == 0 {
			return nil, ErrInvalidMessage
		}
		response, err := parseObject(message.Raw)
		if err != nil {
			return nil, err
		}
		var state string
		if err := json.Unmarshal(response["subtype"], &state); err != nil || (state != "success" && state != "error") {
			return nil, fmt.Errorf("%w: control response subtype must be success or error", ErrInvalidControl)
		}
		var id string
		if err := json.Unmarshal(response["request_id"], &id); err != nil || id == "" {
			return nil, fmt.Errorf("%w: control response requires a non-empty request_id", ErrInvalidControl)
		}
		return json.Marshal(map[string]any{
			"type":     TypeControlResponse,
			"response": response,
		})
	case KindObservation:
		// The only observation this side writes is a user turn; the frame is
		// validated as one so a malformed submit never reaches the wire.
		if message.Type != TypeUser {
			return nil, fmt.Errorf("%w: only user frames are written as observations", ErrInvalidMessage)
		}
		frame, err := parseObject(message.Raw)
		if err != nil {
			return nil, err
		}
		if _, ok := frame["message"]; !ok {
			return nil, fmt.Errorf("%w: user frame requires a message", ErrInvalidMessage)
		}
		return json.Marshal(frame)
	default:
		return nil, ErrInvalidMessage
	}
}

// parseObject validates the envelope framing: exactly one JSON object, no
// surrounding whitespace, no duplicate keys. Deliberately narrower than the
// native reader, which strips lines, skips blank and non-JSON output, and
// drops a truncated tail at EOF.
func parseObject(data []byte) (map[string]json.RawMessage, error) {
	if len(data) == 0 || data[0] != '{' || data[len(data)-1] != '}' {
		return nil, fmt.Errorf("%w: frame must be exactly one JSON object", ErrInvalidMessage)
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("%w: expected one object", ErrInvalidMessage)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: trailing JSON: %v", ErrInvalidMessage, err)
	}
	return object, nil
}

func requireObject(object map[string]json.RawMessage, member string) (map[string]json.RawMessage, error) {
	raw, ok := object[member]
	if !ok {
		return nil, fmt.Errorf("%s is required", member)
	}
	nested, err := parseObject(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be one object: %v", member, err)
	}
	return nested, nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				kt, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := kt.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return errors.New("unexpected closing delimiter")
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func cloneRaw(value json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), value...) }
func ensureEOF(decoder *json.Decoder) error {
	var value any
	err := decoder.Decode(&value)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("additional JSON value")
	}
	return err
}
