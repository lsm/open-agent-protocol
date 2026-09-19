package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
)

const PinnedTag = "v1.18.29"

var (
	ErrInvalidWire = errors.New("opencode native: invalid wire payload")

	sessionIDPattern = regexp.MustCompile(`^ses[A-Za-z0-9_-]+$`)
	messageIDPattern = regexp.MustCompile(`^msg_[A-Za-z0-9_-]+$`)
	eventIDPattern   = regexp.MustCompile(`^evt_[A-Za-z0-9_-]+$`)
)

type SessionID string

func (id SessionID) Valid() bool { return sessionIDPattern.MatchString(string(id)) }

type MessageID string

func (id MessageID) Valid() bool { return messageIDPattern.MatchString(string(id)) }

type EventID string

func (id EventID) Valid() bool { return eventIDPattern.MatchString(string(id)) }

type Delivery string

const (
	DeliverySteer Delivery = "steer"
	DeliveryQueue Delivery = "queue"
)

func (d Delivery) Valid() bool { return d == DeliverySteer || d == DeliveryQueue }

type FinishReason string

type ModelRef struct {
	ID         string          `json:"id"`
	ProviderID string          `json:"providerID"`
	Variant    string          `json:"variant,omitempty"`
	Raw        json.RawMessage `json:"-"`
}

type TokenAccounting struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	Reasoning float64 `json:"reasoning"`
	Cache     struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
}

type UnknownErrorBlock struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type Prompt struct {
	Text  string          `json:"text"`
	Files json.RawMessage `json:"files,omitempty"`
	Agent json.RawMessage `json:"agents,omitempty"`
}

type PromptRequest struct {
	ID       MessageID `json:"id,omitempty"`
	Prompt   Prompt    `json:"prompt"`
	Delivery Delivery  `json:"delivery,omitempty"`
	Resume   *bool     `json:"resume,omitempty"`
}

type Admitted struct {
	AdmittedSeq int64     `json:"admittedSeq"`
	ID          MessageID `json:"id"`
	SessionID   SessionID `json:"sessionID"`
	Prompt      Prompt    `json:"prompt"`
	Delivery    Delivery  `json:"delivery"`
	TimeCreated int64     `json:"timeCreated"`
	PromotedSeq *int64    `json:"promotedSeq,omitempty"`
}

func (a Admitted) Validate() error {
	if !a.ID.Valid() || !a.SessionID.Valid() || !a.Delivery.Valid() || a.TimeCreated < 0 {
		return fmt.Errorf("%w: invalid admitted receipt", ErrInvalidWire)
	}
	return nil
}

type SessionInfo struct {
	ID        SessionID `json:"id"`
	ParentID  string    `json:"parentID,omitempty"`
	ProjectID string    `json:"projectID"`
	Agent     string    `json:"agent,omitempty"`
	Model     *ModelRef `json:"model,omitempty"`
	Cost      float64   `json:"cost"`
	Tokens    struct {
		Input     float64 `json:"input"`
		Output    float64 `json:"output"`
		Reasoning float64 `json:"reasoning"`
		Cache     struct {
			Read  float64 `json:"read"`
			Write float64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Time struct {
		Created  int64  `json:"created"`
		Updated  int64  `json:"updated"`
		Archived *int64 `json:"archived,omitempty"`
	} `json:"time"`
	Title    string          `json:"title"`
	Location json.RawMessage `json:"location"`
	Subpath  string          `json:"subpath,omitempty"`
	Revert   json.RawMessage `json:"revert,omitempty"`
}

func (i SessionInfo) Validate() error {
	if !i.ID.Valid() || i.ProjectID == "" || i.Time.Created < 0 || i.Time.Updated < 0 {
		return fmt.Errorf("%w: invalid session info", ErrInvalidWire)
	}
	return nil
}

type HistoryPage struct {
	Events  []Event `json:"data"`
	HasMore bool    `json:"hasMore"`
}

type Type string

const (
	TypeAgentSwitched     Type = "session.next.agent.switched"
	TypeModelSwitched     Type = "session.next.model.switched"
	TypeMoved             Type = "session.next.moved"
	TypePrompted          Type = "session.next.prompted"
	TypePromptAdmitted    Type = "session.next.prompt.admitted"
	TypeContextUpdated    Type = "session.next.context.updated"
	TypeSynthetic         Type = "session.next.synthetic"
	TypeShellStarted      Type = "session.next.shell.started"
	TypeShellEnded        Type = "session.next.shell.ended"
	TypeStepStarted       Type = "session.next.step.started"
	TypeStepEnded         Type = "session.next.step.ended"
	TypeStepFailed        Type = "session.next.step.failed"
	TypeTextStarted       Type = "session.next.text.started"
	TypeTextEnded         Type = "session.next.text.ended"
	TypeReasoningStarted  Type = "session.next.reasoning.started"
	TypeReasoningEnded    Type = "session.next.reasoning.ended"
	TypeToolInputStarted  Type = "session.next.tool.input.started"
	TypeToolInputEnded    Type = "session.next.tool.input.ended"
	TypeToolCalled        Type = "session.next.tool.called"
	TypeToolProgress      Type = "session.next.tool.progress"
	TypeToolSuccess       Type = "session.next.tool.success"
	TypeToolFailed        Type = "session.next.tool.failed"
	TypeRetried           Type = "session.next.retried"
	TypeCompactionStarted Type = "session.next.compaction.started"
	TypeCompactionEnded   Type = "session.next.compaction.ended"
	TypeRevertStaged      Type = "session.next.revert.staged"
	TypeRevertCleared     Type = "session.next.revert.cleared"
	TypeRevertCommitted   Type = "session.next.revert.committed"
)

func (t Type) Supported() bool {
	switch t {
	case TypeAgentSwitched, TypeModelSwitched, TypeMoved, TypePrompted, TypePromptAdmitted,
		TypeContextUpdated, TypeSynthetic, TypeShellStarted, TypeShellEnded,
		TypeStepStarted, TypeStepEnded, TypeStepFailed, TypeTextStarted, TypeTextEnded,
		TypeReasoningStarted, TypeReasoningEnded, TypeToolInputStarted, TypeToolInputEnded,
		TypeToolCalled, TypeToolProgress, TypeToolSuccess, TypeToolFailed, TypeRetried,
		TypeCompactionStarted, TypeCompactionEnded, TypeRevertStaged, TypeRevertCleared, TypeRevertCommitted:
		return true
	default:
		return false
	}
}

func (t Type) Durable() bool {
	switch t {
	case TypeStepFailed:
		return false
	case TypeTextStarted, TypeTextEnded, TypeReasoningStarted, TypeReasoningEnded,
		TypeToolInputStarted, TypeToolInputEnded, TypeToolCalled, TypeToolProgress,
		TypeToolSuccess, TypeToolFailed, TypeRetried, TypeCompactionStarted,
		TypeCompactionEnded, TypeRevertStaged, TypeRevertCleared, TypeRevertCommitted,
		TypeAgentSwitched, TypeModelSwitched, TypeMoved, TypePrompted,
		TypePromptAdmitted, TypeContextUpdated, TypeSynthetic, TypeShellStarted,
		TypeShellEnded, TypeStepStarted, TypeStepEnded:
		return true
	default:
		return false
	}
}

type DurablePosition struct {
	AggregateID string `json:"aggregateID"`
	Seq         int64  `json:"seq"`
	Version     int    `json:"version"`
}

type Event struct {
	ID      EventID          `json:"id"`
	Type    Type             `json:"type"`
	Durable *DurablePosition `json:"durable,omitempty"`
	Data    json.RawMessage  `json:"data"`
}

func DecodeEvent(data []byte) (Event, error) {
	var envelope struct {
		ID      EventID          `json:"id"`
		Type    Type             `json:"type"`
		Durable *DurablePosition `json:"durable"`
		Data    json.RawMessage  `json:"data"`
	}
	if err := decodeStrict(data, &envelope, true); err != nil {
		return Event{}, fmt.Errorf("%w: envelope: %v", ErrInvalidWire, err)
	}
	if !envelope.ID.Valid() {
		return Event{}, fmt.Errorf("%w: invalid event id", ErrInvalidWire)
	}
	if !envelope.Type.Supported() {
		return Event{}, fmt.Errorf("%w: unsupported event type %q", ErrUnsupportedType, envelope.Type)
	}
	if envelope.Durable == nil {
		return Event{}, fmt.Errorf("%w: session event without durable position", ErrInvalidWire)
	}
	if envelope.Durable.AggregateID == "" || envelope.Durable.Seq < 0 {
		return Event{}, fmt.Errorf("%w: invalid durable position", ErrInvalidWire)
	}
	return Event{ID: envelope.ID, Type: envelope.Type, Durable: envelope.Durable, Data: envelope.Data}, nil
}

var ErrUnsupportedType = errors.New("opencode native: unsupported event type")

type PromptedData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
	MessageID MessageID `json:"messageID"`
	Prompt    Prompt    `json:"prompt"`
	Delivery  Delivery  `json:"delivery"`
}

type SwitchedData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
	MessageID MessageID `json:"messageID"`
}

type SyntheticData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
	MessageID MessageID `json:"messageID"`
	Text      string    `json:"text"`
}

type ShellStartedData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
	MessageID MessageID `json:"messageID"`
	CallID    string    `json:"callID"`
	Command   string    `json:"command"`
}

type ShellEndedData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
	CallID    string    `json:"callID"`
	Output    string    `json:"output"`
}

type StepStartedData struct {
	Timestamp        int64     `json:"timestamp"`
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	Agent            string    `json:"agent"`
	Model            ModelRef  `json:"model"`
	Snapshot         string    `json:"snapshot,omitempty"`
}

type StepEndedData struct {
	Timestamp        int64           `json:"timestamp"`
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	Finish           string          `json:"finish"`
	Cost             float64         `json:"cost"`
	Tokens           TokenAccounting `json:"tokens"`
	Snapshot         string          `json:"snapshot,omitempty"`
	Files            []string        `json:"files,omitempty"`
}

type StepFailedData struct {
	Timestamp        int64             `json:"timestamp"`
	SessionID        SessionID         `json:"sessionID"`
	AssistantMessage MessageID         `json:"assistantMessageID"`
	Error            UnknownErrorBlock `json:"error"`
}

type TextStartedData struct {
	Timestamp        int64     `json:"timestamp"`
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	TextID           string    `json:"textID"`
}

type TextEndedData struct {
	Timestamp        int64     `json:"timestamp"`
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	TextID           string    `json:"textID"`
	Text             string    `json:"text"`
}

type ReasoningStartedData struct {
	Timestamp        int64           `json:"timestamp"`
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	ReasoningID      string          `json:"reasoningID"`
	ProviderMetadata json.RawMessage `json:"providerMetadata,omitempty"`
}

type ReasoningEndedData struct {
	Timestamp        int64           `json:"timestamp"`
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	ReasoningID      string          `json:"reasoningID"`
	Text             string          `json:"text"`
	ProviderMetadata json.RawMessage `json:"providerMetadata,omitempty"`
}

type ToolInputStartedData struct {
	Timestamp        int64     `json:"timestamp"`
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	CallID           string    `json:"callID"`
	Name             string    `json:"name"`
}

type ToolInputEndedData struct {
	Timestamp        int64     `json:"timestamp"`
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	CallID           string    `json:"callID"`
	Text             string    `json:"text"`
}

type ToolCalledData struct {
	Timestamp        int64          `json:"timestamp"`
	SessionID        SessionID      `json:"sessionID"`
	AssistantMessage MessageID      `json:"assistantMessageID"`
	CallID           string         `json:"callID"`
	Tool             string         `json:"tool"`
	Input            map[string]any `json:"input"`
	Provider         struct {
		Executed         bool            `json:"executed"`
		ProviderMetadata json.RawMessage `json:"metadata,omitempty"`
	} `json:"provider"`
}

type ToolProgressData struct {
	Timestamp        int64          `json:"timestamp"`
	SessionID        SessionID      `json:"sessionID"`
	AssistantMessage MessageID      `json:"assistantMessageID"`
	CallID           string         `json:"callID"`
	Structured       map[string]any `json:"structured"`
	Content          []ToolContent  `json:"content"`
}

type ToolSuccessData struct {
	Timestamp        int64           `json:"timestamp"`
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	CallID           string          `json:"callID"`
	Structured       map[string]any  `json:"structured"`
	Content          []ToolContent   `json:"content"`
	OutputPaths      []string        `json:"outputPaths,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	Provider         struct {
		Executed         bool            `json:"executed"`
		ProviderMetadata json.RawMessage `json:"metadata,omitempty"`
	} `json:"provider"`
}

type ToolFailedData struct {
	Timestamp        int64             `json:"timestamp"`
	SessionID        SessionID         `json:"sessionID"`
	AssistantMessage MessageID         `json:"assistantMessageID"`
	CallID           string            `json:"callID"`
	Error            UnknownErrorBlock `json:"error"`
	Result           json.RawMessage   `json:"result,omitempty"`
	Provider         struct {
		Executed         bool            `json:"executed"`
		ProviderMetadata json.RawMessage `json:"metadata,omitempty"`
	} `json:"provider"`
}

type ToolContent struct {
	Type string          `json:"type"`
	Text string          `json:"text,omitempty"`
	Raw  json.RawMessage `json:"-"`
}

func (c *ToolContent) UnmarshalJSON(data []byte) error {
	type plain ToolContent
	var value plain
	if err := decodeStrict(data, &value, false); err != nil {
		return err
	}
	*c = ToolContent(value)
	c.Raw = append(json.RawMessage(nil), data...)
	return nil
}

type RetriedData struct {
	Timestamp int64          `json:"timestamp"`
	SessionID SessionID      `json:"sessionID"`
	Attempt   float64        `json:"attempt"`
	Error     RetryErrorData `json:"error"`
}

type RetryErrorData struct {
	Message         string            `json:"message"`
	StatusCode      *float64          `json:"statusCode,omitempty"`
	IsRetryable     bool              `json:"isRetryable"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	ResponseBody    string            `json:"responseBody,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type CompactionStartedData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
	MessageID MessageID `json:"messageID"`
	Reason    string    `json:"reason"`
}

type CompactionEndedData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
	MessageID MessageID `json:"messageID"`
	Reason    string    `json:"reason"`
	Text      string    `json:"text"`
	Recent    string    `json:"recent"`
}

type RevertStagedData struct {
	Timestamp int64           `json:"timestamp"`
	SessionID SessionID       `json:"sessionID"`
	Revert    json.RawMessage `json:"revert"`
}

type EmptyData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
}

type RevertCommittedData struct {
	Timestamp int64     `json:"timestamp"`
	SessionID SessionID `json:"sessionID"`
	MessageID MessageID `json:"messageID"`
}

type APIError struct {
	Status int
	Tag    string
	Fields map[string]json.RawMessage
}

func (e *APIError) Error() string {
	if e.Tag == "" {
		return fmt.Sprintf("opencode native: HTTP %d", e.Status)
	}
	return fmt.Sprintf("opencode native: HTTP %d %s", e.Status, e.Tag)
}

func (e *APIError) IsSessionNotFound() bool {
	return e.Tag == "SessionNotFoundError"
}

func (e *APIError) IsConflict() bool {
	return e.Tag == "ConflictError" || e.Tag == "PromptConflictError"
}

func DecodeAPIError(status int, body []byte) *APIError {
	err := &APIError{Status: status, Fields: map[string]json.RawMessage{}}
	var tagged struct {
		Tag string `json:"_tag"`
	}
	if json.Unmarshal(body, &tagged) == nil {
		err.Tag = tagged.Tag
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) == nil {
		for key, value := range fields {
			if key != "_tag" {
				err.Fields[key] = value
			}
		}
	}
	return err
}

func DecodeData(e Event, dst any) error {
	if err := decodeStrict(e.Data, dst, true); err != nil {
		return fmt.Errorf("%w: %s data: %v", ErrInvalidWire, e.Type, err)
	}
	return nil
}

func decodeStrict(data []byte, dst any, unknown bool) error {
	if err := RejectDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if unknown {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func RejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := dec.Token()
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
			for dec.More() {
				keyToken, err := dec.Token()
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
			_, err := dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		default:
			return errors.New("unexpected closing delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
