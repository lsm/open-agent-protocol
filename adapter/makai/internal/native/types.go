// Package native contains the reduced, pinned Makai agent-protocol wire types.
// It intentionally does not contain OAP semantics.
package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const ProtocolVersion uint8 = 1

var (
	ErrInvalidEnvelope = errors.New("makai native: invalid envelope")
	ErrUnsupportedType = errors.New("makai native: unsupported payload type")
	sessionIDPattern   = regexp.MustCompile(`^[0-9A-Za-z]{21}$`)
	ulidPattern        = regexp.MustCompile(`^[0-7][0-9A-TV-Za-tv-z]{25}$`)
)

type SessionID string

func (id SessionID) Valid() bool { return sessionIDPattern.MatchString(string(id)) }

type MessageID string

func (id MessageID) Valid() bool { return ulidPattern.MatchString(string(id)) }

type Type string

const (
	TypeAgentStart       Type = "agent_start"
	TypeAgentMessage     Type = "agent_message"
	TypeAgentStop        Type = "agent_stop"
	TypeAgentStatus      Type = "agent_status"
	TypeToolList         Type = "tool_list"
	TypeAgentStarted     Type = "agent_started"
	TypeAgentEvent       Type = "agent_event"
	TypeAgentResult      Type = "agent_result"
	TypeAgentStopped     Type = "agent_stopped"
	TypeAgentError       Type = "agent_error"
	TypeSessionInfo      Type = "session_info"
	TypeToolListResponse Type = "tool_list_response"
	TypeToolExecute      Type = "tool_execute"
	TypeToolResult       Type = "tool_result"
	TypeToolStreaming    Type = "tool_streaming"
	TypePing             Type = "ping"
	TypePong             Type = "pong"
	TypeGoodbye          Type = "goodbye"
	TypeAck              Type = "ack"
	TypeNack             Type = "nack"
)

func (t Type) Supported() bool {
	switch t {
	case TypeAgentStart, TypeAgentMessage, TypeAgentStop, TypeAgentStatus, TypeToolList, TypeAgentStarted,
		TypeAgentEvent, TypeAgentResult, TypeAgentStopped, TypeAgentError, TypeSessionInfo, TypeToolListResponse,
		TypeToolExecute, TypeToolResult, TypeToolStreaming, TypePing, TypePong, TypeGoodbye,
		TypeAck, TypeNack:
		return true
	default:
		return false
	}
}

type Envelope struct {
	Type      Type            `json:"type"`
	SessionID SessionID       `json:"session_id"`
	MessageID MessageID       `json:"message_id"`
	Sequence  uint64          `json:"sequence"`
	Timestamp int64           `json:"timestamp"`
	Version   uint8           `json:"version"`
	InReplyTo *MessageID      `json:"in_reply_to,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

func NewEnvelope(typ Type, sessionID SessionID, messageID MessageID, sequence uint64, timestamp int64, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	env := Envelope{Type: typ, SessionID: sessionID, MessageID: messageID, Sequence: sequence, Timestamp: timestamp, Version: ProtocolVersion, Payload: raw}
	if err := env.Validate(false); err != nil {
		return Envelope{}, err
	}
	return env, nil
}

// Validate checks the common envelope. allowErrorSequence permits the pinned
// server's exceptional agent_error sequence zero; all other frames start at 1.
func (e Envelope) Validate(allowErrorSequence bool) error {
	if !e.Type.Supported() {
		return fmt.Errorf("%w: %q", ErrUnsupportedType, e.Type)
	}
	if e.Version != ProtocolVersion {
		return fmt.Errorf("%w: version must be %d", ErrInvalidEnvelope, ProtocolVersion)
	}
	if !e.SessionID.Valid() {
		return fmt.Errorf("%w: invalid session_id", ErrInvalidEnvelope)
	}
	if !e.MessageID.Valid() {
		return fmt.Errorf("%w: invalid message_id", ErrInvalidEnvelope)
	}
	if e.InReplyTo != nil && !e.InReplyTo.Valid() {
		return fmt.Errorf("%w: invalid in_reply_to", ErrInvalidEnvelope)
	}
	if e.Sequence == 0 && !(allowErrorSequence && e.Type == TypeAgentError) {
		return fmt.Errorf("%w: sequence must be positive", ErrInvalidEnvelope)
	}
	if e.Timestamp < 0 {
		return fmt.Errorf("%w: timestamp must be non-negative", ErrInvalidEnvelope)
	}
	if len(e.Payload) == 0 || !json.Valid(e.Payload) || !jsonObject(e.Payload) {
		return fmt.Errorf("%w: payload must be a JSON object", ErrInvalidEnvelope)
	}
	if err := validatePayload(e.Type, e.Payload); err != nil {
		return err
	}
	var nested SessionID
	switch e.Type {
	case TypeAgentStart:
		p, _ := DecodePayload[AgentStart](e)
		if p.ResumeSessionID != nil {
			nested = *p.ResumeSessionID
		}
	case TypeAgentMessage:
		p, _ := DecodePayload[AgentMessage](e)
		nested = p.SessionID
	case TypeAgentStop:
		p, _ := DecodePayload[AgentStop](e)
		nested = p.SessionID
	case TypeAgentStatus:
		p, _ := DecodePayload[AgentStatusRequest](e)
		nested = p.SessionID
	case TypeAgentStarted:
		p, _ := DecodePayload[AgentStarted](e)
		nested = p.SessionID
	case TypeAgentStopped:
		p, _ := DecodePayload[AgentStopped](e)
		nested = p.SessionID
	case TypeSessionInfo:
		p, _ := DecodePayload[SessionInfo](e)
		nested = p.SessionID
	}
	if nested != "" && nested != e.SessionID {
		return fmt.Errorf("%w: payload session_id does not match envelope", ErrInvalidEnvelope)
	}
	return nil
}

func DecodePayload[T any](e Envelope) (T, error) {
	var value T
	if err := strictDecode(e.Payload, &value); err != nil {
		return value, fmt.Errorf("%w: %s payload: %v", ErrInvalidEnvelope, e.Type, err)
	}
	return value, nil
}

type AgentStart struct {
	ConfigJSON      string     `json:"config_json"`
	SystemPrompt    string     `json:"system_prompt,omitempty"`
	ResumeSessionID *SessionID `json:"resume_session_id,omitempty"`
}
type AgentMessage struct {
	SessionID   SessionID `json:"session_id"`
	MessageJSON string    `json:"message_json"`
	OptionsJSON string    `json:"options_json,omitempty"`
}
type AgentStop struct {
	SessionID SessionID `json:"session_id"`
	Reason    string    `json:"reason,omitempty"`
}
type AgentStatusRequest struct {
	SessionID SessionID `json:"session_id"`
}
type AgentStarted struct {
	SessionID SessionID `json:"session_id"`
}
type AgentEvent struct {
	EventJSON string `json:"event_json"`
}
type AgentResult struct {
	ResultJSON string `json:"result_json"`
}
type AgentStopped struct {
	SessionID SessionID `json:"session_id"`
	Reason    string    `json:"reason,omitempty"`
}
type AgentErrorCode string

const (
	ErrorInvalidRequest  AgentErrorCode = "invalid_request"
	ErrorAgentNotFound   AgentErrorCode = "agent_not_found"
	ErrorToolNotFound    AgentErrorCode = "tool_not_found"
	ErrorToolExecution   AgentErrorCode = "tool_execution_error"
	ErrorContextOverflow AgentErrorCode = "context_overflow"
	ErrorRateLimited     AgentErrorCode = "rate_limited"
	ErrorInternal        AgentErrorCode = "internal_error"
	ErrorAgentBusy       AgentErrorCode = "agent_busy"
	ErrorSessionExpired  AgentErrorCode = "session_expired"
	ErrorAuthRequired    AgentErrorCode = "auth_required"
)

type AgentError struct {
	Code    AgentErrorCode `json:"code"`
	Message string         `json:"message"`
}
type AgentStatus string

const (
	StatusStarting       AgentStatus = "starting"
	StatusReady          AgentStatus = "ready"
	StatusProcessing     AgentStatus = "processing"
	StatusWaitingForTool AgentStatus = "waiting_for_tool"
	StatusStopping       AgentStatus = "stopping"
	StatusStopped        AgentStatus = "stopped"
	StatusError          AgentStatus = "error"
)

type SessionInfo struct {
	SessionID    SessionID   `json:"session_id"`
	Status       AgentStatus `json:"status"`
	Model        string      `json:"model"`
	MessageCount uint32      `json:"message_count"`
	CreatedAt    int64       `json:"created_at"`
	UpdatedAt    int64       `json:"updated_at"`
}
type ToolList struct {
	Prefix string `json:"prefix,omitempty"`
}
type ToolDefinition struct {
	Name                 string `json:"name"`
	Description          string `json:"description"`
	ParametersSchemaJSON string `json:"parameters_schema_json"`
}
type ToolListResponse struct {
	Tools []ToolDefinition `json:"tools"`
}
type ToolExecute struct {
	ToolCallID  string `json:"tool_call_id"`
	ToolName    string `json:"tool_name"`
	ArgsJSON    string `json:"args_json"`
	CallbackURL string `json:"callback_url,omitempty"`
}
type ToolResult struct {
	ToolCallID  string `json:"tool_call_id"`
	ResultJSON  string `json:"result_json"`
	IsError     bool   `json:"is_error"`
	DetailsJSON string `json:"details_json,omitempty"`
}
type ToolStreaming struct {
	ToolCallID  string `json:"tool_call_id"`
	PartialJSON string `json:"partial_json"`
}
type Empty struct{}
type Pong struct {
	PingID string `json:"ping_id"`
}
type Goodbye struct {
	Reason string `json:"reason,omitempty"`
}
type Ack struct {
	AcknowledgedID MessageID `json:"acknowledged_id"`
}
type Nack struct {
	RejectedID MessageID `json:"rejected_id"`
	Reason     string    `json:"reason"`
	ErrorCode  *string   `json:"error_code,omitempty"`
}

func validatePayload(t Type, raw []byte) error {
	var target any
	switch t {
	case TypeAgentStart:
		target = &AgentStart{}
	case TypeAgentMessage:
		target = &AgentMessage{}
	case TypeAgentStop:
		target = &AgentStop{}
	case TypeAgentStatus:
		target = &AgentStatusRequest{}
	case TypeToolList:
		target = &ToolList{}
	case TypeAgentStarted:
		target = &AgentStarted{}
	case TypeAgentEvent:
		target = &AgentEvent{}
	case TypeAgentResult:
		target = &AgentResult{}
	case TypeAgentStopped:
		target = &AgentStopped{}
	case TypeAgentError:
		target = &AgentError{}
	case TypeSessionInfo:
		target = &SessionInfo{}
	case TypeToolListResponse:
		target = &ToolListResponse{}
	case TypeToolExecute:
		target = &ToolExecute{}
	case TypeToolResult:
		target = &ToolResult{}
	case TypeToolStreaming:
		target = &ToolStreaming{}
	case TypePing:
		target = &Empty{}
	case TypePong:
		target = &Pong{}
	case TypeGoodbye:
		target = &Goodbye{}
	case TypeAck:
		target = &Ack{}
	case TypeNack:
		target = &Nack{}
	default:
		return fmt.Errorf("%w: %q", ErrUnsupportedType, t)
	}
	if err := strictDecode(raw, target); err != nil {
		return fmt.Errorf("%w: %s payload: %v", ErrInvalidEnvelope, t, err)
	}
	switch p := target.(type) {
	case *AgentStart:
		if !validEmbeddedJSON(p.ConfigJSON) || (p.ResumeSessionID != nil && !p.ResumeSessionID.Valid()) {
			return fmt.Errorf("%w: invalid agent_start payload", ErrInvalidEnvelope)
		}
	case *AgentMessage:
		if !p.SessionID.Valid() || !validEmbeddedJSON(p.MessageJSON) || (p.OptionsJSON != "" && !validEmbeddedJSON(p.OptionsJSON)) {
			return fmt.Errorf("%w: invalid agent_message payload", ErrInvalidEnvelope)
		}
	case *AgentStop:
		if !p.SessionID.Valid() {
			return fmt.Errorf("%w: invalid agent_stop session", ErrInvalidEnvelope)
		}
	case *AgentStatusRequest:
		if !p.SessionID.Valid() {
			return fmt.Errorf("%w: invalid agent_status session", ErrInvalidEnvelope)
		}
	case *AgentStarted:
		if !p.SessionID.Valid() {
			return fmt.Errorf("%w: invalid agent_started session", ErrInvalidEnvelope)
		}
	case *AgentStopped:
		if !p.SessionID.Valid() {
			return fmt.Errorf("%w: invalid agent_stopped session", ErrInvalidEnvelope)
		}
	case *AgentEvent:
		if !validEmbeddedJSON(p.EventJSON) {
			return fmt.Errorf("%w: invalid nested event JSON", ErrInvalidEnvelope)
		}
	case *AgentResult:
		if !validEmbeddedJSON(p.ResultJSON) {
			return fmt.Errorf("%w: invalid nested result JSON", ErrInvalidEnvelope)
		}
	case *AgentError:
		if p.Message == "" || !validErrorCode(p.Code) {
			return fmt.Errorf("%w: invalid agent_error payload", ErrInvalidEnvelope)
		}
	case *SessionInfo:
		if !p.SessionID.Valid() || !validStatus(p.Status) {
			return fmt.Errorf("%w: invalid session_info payload", ErrInvalidEnvelope)
		}
	case *ToolListResponse:
		for _, tool := range p.Tools {
			if tool.Name == "" || !validEmbeddedJSON(tool.ParametersSchemaJSON) {
				return fmt.Errorf("%w: invalid tool_list_response payload", ErrInvalidEnvelope)
			}
		}
	case *ToolExecute:
		if p.ToolCallID == "" || p.ToolName == "" || !validEmbeddedJSON(p.ArgsJSON) {
			return fmt.Errorf("%w: invalid tool_execute payload", ErrInvalidEnvelope)
		}
	case *ToolResult:
		if p.ToolCallID == "" || !validEmbeddedJSON(p.ResultJSON) || (p.DetailsJSON != "" && !validEmbeddedJSON(p.DetailsJSON)) {
			return fmt.Errorf("%w: invalid tool_result payload", ErrInvalidEnvelope)
		}
	case *ToolStreaming:
		if p.ToolCallID == "" || !validEmbeddedJSON(p.PartialJSON) {
			return fmt.Errorf("%w: invalid tool_streaming payload", ErrInvalidEnvelope)
		}
	case *Pong:
		if p.PingID == "" {
			return fmt.Errorf("%w: empty ping_id", ErrInvalidEnvelope)
		}
	case *Ack:
		if !p.AcknowledgedID.Valid() {
			return fmt.Errorf("%w: invalid acknowledged_id", ErrInvalidEnvelope)
		}
	case *Nack:
		if !p.RejectedID.Valid() || p.Reason == "" {
			return fmt.Errorf("%w: invalid nack payload", ErrInvalidEnvelope)
		}
	}
	return nil
}
func strictDecode(data []byte, dst any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
func validEmbeddedJSON(s string) bool {
	return s != "" && rejectDuplicateKeys([]byte(s)) == nil
}

func rejectDuplicateKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		default:
			return errors.New("unexpected closing delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
func jsonObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}
func validStatus(s AgentStatus) bool {
	switch s {
	case StatusStarting, StatusReady, StatusProcessing, StatusWaitingForTool, StatusStopping, StatusStopped, StatusError:
		return true
	}
	return false
}
func validErrorCode(c AgentErrorCode) bool {
	switch c {
	case ErrorInvalidRequest, ErrorAgentNotFound, ErrorToolNotFound, ErrorToolExecution, ErrorContextOverflow, ErrorRateLimited, ErrorInternal, ErrorAgentBusy, ErrorSessionExpired, ErrorAuthRequired:
		return true
	}
	return false
}
