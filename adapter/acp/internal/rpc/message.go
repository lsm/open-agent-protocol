package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

var (
	ErrInvalidMessage = errors.New("acp rpc: invalid JSON-RPC message")
	ErrInvalidID      = errors.New("acp rpc: id must be a string or integer")
)

// RequestID preserves the string and integer identity domains allowed by ACP.
// IDs are correlation values only; their String representation is diagnostic.
type RequestID struct {
	text    string
	integer int64
	kind    idKind
}

type idKind uint8

const (
	idUnset idKind = iota
	idString
	idInteger
)

func StringID(value string) RequestID { return RequestID{text: value, kind: idString} }
func IntegerID(value int64) RequestID { return RequestID{integer: value, kind: idInteger} }

func (id RequestID) IsString() bool  { return id.kind == idString }
func (id RequestID) IsInteger() bool { return id.kind == idInteger }
func (id RequestID) StringValue() (string, bool) {
	return id.text, id.kind == idString
}
func (id RequestID) IntegerValue() (int64, bool) {
	return id.integer, id.kind == idInteger
}
func (id RequestID) String() string {
	switch id.kind {
	case idString:
		return strconv.Quote(id.text)
	case idInteger:
		return strconv.FormatInt(id.integer, 10)
	default:
		return "<unset>"
	}
}
func (id RequestID) valid() bool { return id.kind == idString || id.kind == idInteger }

func (id RequestID) MarshalJSON() ([]byte, error) {
	switch id.kind {
	case idString:
		return json.Marshal(id.text)
	case idInteger:
		return []byte(strconv.FormatInt(id.integer, 10)), nil
	default:
		return nil, ErrInvalidID
	}
}

func (id *RequestID) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidID, err)
	}
	if extraJSON(decoder) {
		return fmt.Errorf("%w: trailing JSON value", ErrInvalidID)
	}
	switch value := value.(type) {
	case string:
		*id = StringID(value)
		return nil
	case json.Number:
		integer, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return fmt.Errorf("%w: %q", ErrInvalidID, value)
		}
		*id = IntegerID(integer)
		return nil
	default:
		return ErrInvalidID
	}
}

type MessageKind uint8

const (
	MessageRequest MessageKind = iota + 1
	MessageNotification
	MessageResponse
	MessageError
)

type ErrorObject struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type Message struct {
	Kind   MessageKind
	ID     RequestID
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Error  *ErrorObject
}

func Request(id RequestID, method string, params json.RawMessage) Message {
	return Message{Kind: MessageRequest, ID: id, Method: method, Params: cloneRaw(params)}
}
func Notification(method string, params json.RawMessage) Message {
	return Message{Kind: MessageNotification, Method: method, Params: cloneRaw(params)}
}
func Response(id RequestID, result json.RawMessage) Message {
	return Message{Kind: MessageResponse, ID: id, Result: cloneRaw(result)}
}
func ErrorResponse(id RequestID, rpcError ErrorObject) Message {
	return Message{Kind: MessageError, ID: id, Error: &rpcError}
}

func ParseMessage(data []byte) (Message, error) {
	if !utf8.Valid(data) {
		return Message{}, fmt.Errorf("%w: frame is not UTF-8", ErrInvalidMessage)
	}
	// Decoding into a map would silently collapse a repeated id/result/method
	// with last-value-wins, so reject duplicates on the raw frame first.
	if err := rejectDuplicateKeys(data); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		return Message{}, fmt.Errorf("%w: %v", ErrInvalidMessage, err)
	}
	if object == nil || extraJSON(decoder) {
		return Message{}, fmt.Errorf("%w: expected one object", ErrInvalidMessage)
	}
	version, ok := object["jsonrpc"]
	if !ok || !rawStringEqual(version, "2.0") {
		return Message{}, fmt.Errorf("%w: jsonrpc must equal \"2.0\"", ErrInvalidMessage)
	}

	_, hasID := object["id"]
	_, hasMethod := object["method"]
	_, hasParams := object["params"]
	_, hasResult := object["result"]
	_, hasError := object["error"]
	if hasResult && hasError {
		return Message{}, fmt.Errorf("%w: result and error are mutually exclusive", ErrInvalidMessage)
	}
	if hasMethod && (hasResult || hasError) {
		return Message{}, fmt.Errorf("%w: method cannot accompany a response", ErrInvalidMessage)
	}
	if !hasMethod && hasParams {
		return Message{}, fmt.Errorf("%w: params requires method", ErrInvalidMessage)
	}

	var message Message
	if hasID {
		if err := json.Unmarshal(object["id"], &message.ID); err != nil {
			return Message{}, err
		}
	}
	switch {
	case hasMethod && hasID:
		message.Kind = MessageRequest
	case hasMethod:
		message.Kind = MessageNotification
	case hasID && hasResult:
		message.Kind = MessageResponse
	case hasID && hasError:
		message.Kind = MessageError
	default:
		return Message{}, fmt.Errorf("%w: unrecognized object shape", ErrInvalidMessage)
	}

	if hasMethod {
		if err := json.Unmarshal(object["method"], &message.Method); err != nil || message.Method == "" {
			return Message{}, fmt.Errorf("%w: method must be a non-empty string", ErrInvalidMessage)
		}
		if hasParams && !rawObjectOrArray(object["params"]) {
			return Message{}, fmt.Errorf("%w: params must be an object or array", ErrInvalidMessage)
		}
		message.Params = cloneRaw(object["params"])
	}
	if hasResult {
		message.Result = cloneRaw(object["result"])
	}
	if hasError {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(object["error"], &fields); err != nil || fields == nil {
			return Message{}, fmt.Errorf("%w: malformed error object", ErrInvalidMessage)
		}
		if _, ok := fields["code"]; !ok {
			return Message{}, fmt.Errorf("%w: error code is required", ErrInvalidMessage)
		}
		if _, ok := fields["message"]; !ok {
			return Message{}, fmt.Errorf("%w: error message is required", ErrInvalidMessage)
		}
		var rpcError ErrorObject
		if err := json.Unmarshal(object["error"], &rpcError); err != nil {
			return Message{}, fmt.Errorf("%w: malformed error object", ErrInvalidMessage)
		}
		message.Error = &rpcError
	}
	return message, nil
}

func (message Message) MarshalJSON() ([]byte, error) {
	object := map[string]any{"jsonrpc": "2.0"}
	switch message.Kind {
	case MessageRequest:
		if !message.ID.valid() || message.Method == "" || (len(message.Params) > 0 && !rawObjectOrArray(message.Params)) {
			return nil, ErrInvalidMessage
		}
		object["id"] = message.ID
		object["method"] = message.Method
		putRaw(object, "params", message.Params)
	case MessageNotification:
		if message.Method == "" || (len(message.Params) > 0 && !rawObjectOrArray(message.Params)) {
			return nil, ErrInvalidMessage
		}
		object["method"] = message.Method
		putRaw(object, "params", message.Params)
	case MessageResponse:
		if !message.ID.valid() || len(message.Result) == 0 || !json.Valid(message.Result) {
			return nil, ErrInvalidMessage
		}
		object["id"] = message.ID
		object["result"] = json.RawMessage(message.Result)
	case MessageError:
		if !message.ID.valid() || message.Error == nil {
			return nil, ErrInvalidMessage
		}
		object["id"] = message.ID
		object["error"] = message.Error
	default:
		return nil, ErrInvalidMessage
	}
	return json.Marshal(object)
}

func putRaw(object map[string]any, name string, value json.RawMessage) {
	if len(value) != 0 {
		object[name] = value
	}
}
func cloneRaw(value json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), value...) }
func extraJSON(decoder *json.Decoder) bool {
	var value any
	return decoder.Decode(&value) == nil
}
func rawStringEqual(raw json.RawMessage, want string) bool {
	var value string
	return json.Unmarshal(raw, &value) == nil && value == want
}
func rawObjectOrArray(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

// rejectDuplicateKeys walks every object in the frame and fails on a repeated
// key, which encoding/json would otherwise collapse with last-value-wins.
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
