package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/lsm/open-agent-protocol/go/internal/jsonwalk"
)

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

type Prompt struct {
	Text  string          `json:"text"`
	Files json.RawMessage `json:"files,omitempty"`
	Agent json.RawMessage `json:"agents,omitempty"`
}

type PromptRequest struct {
	ID       MessageID       `json:"id,omitempty"`
	Text     string          `json:"text"`
	Files    json.RawMessage `json:"files,omitempty"`
	Agents   json.RawMessage `json:"agents,omitempty"`
	Delivery Delivery        `json:"delivery,omitempty"`
	Resume   *bool           `json:"resume,omitempty"`
}

type Admitted struct {
	ID        MessageID `json:"id"`
	SessionID SessionID `json:"sessionID"`
	Time      struct {
		Created int64 `json:"created"`
	} `json:"time"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
	Delivery Delivery        `json:"delivery"`
}

func (a Admitted) Validate() error {
	if !a.ID.Valid() || !a.SessionID.Valid() || !a.Delivery.Valid() || a.Type != "user" || a.Time.Created < 0 {
		return fmt.Errorf("%w: invalid admitted receipt", ErrInvalidWire)
	}
	return nil
}

type Location struct {
	Directory string          `json:"directory"`
	Raw       json.RawMessage `json:"-"`
}

type SessionInfo struct {
	ID        SessionID       `json:"id"`
	ParentID  string          `json:"parentID,omitempty"`
	Fork      json.RawMessage `json:"fork,omitempty"`
	ProjectID string          `json:"projectID"`
	Outcome   string          `json:"outcome,omitempty"`
	Agent     string          `json:"agent,omitempty"`
	Model     *ModelRef       `json:"model,omitempty"`
	Cost      float64         `json:"cost"`
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
		Idle     *int64 `json:"idle,omitempty"`
		Viewed   *int64 `json:"viewed,omitempty"`
		Archived *int64 `json:"archived,omitempty"`
	} `json:"time"`
	Title       string          `json:"title,omitempty"`
	Location    json.RawMessage `json:"location"`
	Subpath     string          `json:"subpath,omitempty"`
	Revert      json.RawMessage `json:"revert,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	Permissions json.RawMessage `json:"permissions,omitempty"`
}

func (i SessionInfo) Validate() error {
	if !i.ID.Valid() || i.ProjectID == "" || i.Time.Created < 0 || i.Time.Updated < 0 {
		return fmt.Errorf("%w: invalid session info", ErrInvalidWire)
	}
	return nil
}

func (i SessionInfo) Directory() string {
	var location struct {
		Directory string `json:"directory"`
	}
	if json.Unmarshal(i.Location, &location) != nil {
		return ""
	}
	return location.Directory
}

type ServerInfo struct {
	Version      string          `json:"version"`
	PID          int             `json:"pid"`
	URLs         []string        `json:"urls"`
	Paths        json.RawMessage `json:"paths"`
	Capabilities json.RawMessage `json:"capabilities"`
}

type Type string

const (
	TypeServerConnected      Type = "server.connected"
	TypeInboxEnqueued        Type = "session.inbox.enqueued"
	TypeInboxDelivered       Type = "session.inbox.delivered"
	TypeInboxCancelled       Type = "session.inbox.cancelled"
	TypeInboxDeliveryChanged Type = "session.inbox.delivery.changed"
	TypeExecutionStarted     Type = "session.execution.started"
	TypeExecutionSucceeded   Type = "session.execution.succeeded"
	TypeExecutionFailed      Type = "session.execution.failed"
	TypeExecutionInterrupted Type = "session.execution.interrupted"
	TypeStepStarted          Type = "session.step.started"
	TypeStepStreamed         Type = "session.step.streamed"
	TypeStepEnded            Type = "session.step.ended"
	TypeStepFailed           Type = "session.step.failed"
	TypeTextStarted          Type = "session.text.started"
	TypeTextDelta            Type = "session.text.delta"
	TypeTextEnded            Type = "session.text.ended"
	TypeReasoningStarted     Type = "session.reasoning.started"
	TypeReasoningDelta       Type = "session.reasoning.delta"
	TypeReasoningEnded       Type = "session.reasoning.ended"
	TypeToolInputStarted     Type = "session.tool.input.started"
	TypeToolInputDelta       Type = "session.tool.input.delta"
	TypeToolInputEnded       Type = "session.tool.input.ended"
	TypeToolCalled           Type = "session.tool.called"
	TypeToolProgress         Type = "session.tool.progress"
	TypeToolSuccess          Type = "session.tool.success"
	TypeToolFailed           Type = "session.tool.failed"
	TypeCreated              Type = "session.created"
	TypeAgentSelected        Type = "session.agent.selected"
	TypeModelSelected        Type = "session.model.selected"
	TypeMoved                Type = "session.moved"
	TypeRenamed              Type = "session.renamed"
	TypeMetadataUpdated      Type = "session.metadata.updated"
	TypePermissions          Type = "session.permissions"
	TypeViewed               Type = "session.viewed"
	TypeUsageUpdated         Type = "session.usage.updated"
	TypeDeleted              Type = "session.deleted"
	TypeForked               Type = "session.forked"
	TypeInstructionsUpdated  Type = "session.instructions.updated"
	TypeSynthetic            Type = "session.synthetic"
	TypeSkillActivated       Type = "session.skill.activated"
	TypeShellStarted         Type = "session.shell.started"
	TypeShellEnded           Type = "session.shell.ended"
	TypeRetryScheduled       Type = "session.retry.scheduled"
	TypeCompactionStarted    Type = "session.compaction.started"
	TypeCompactionDelta      Type = "session.compaction.delta"
	TypeCompactionEnded      Type = "session.compaction.ended"
	TypeCompactionFailed     Type = "session.compaction.failed"
	TypeRevertStaged         Type = "session.revert.staged"
	TypeRevertCleared        Type = "session.revert.cleared"
	TypeRevertCommitted      Type = "session.revert.committed"
)

func (t Type) Supported() bool {
	switch t {
	case TypeInboxEnqueued, TypeInboxDelivered, TypeInboxCancelled, TypeInboxDeliveryChanged,
		TypeExecutionStarted, TypeExecutionSucceeded, TypeExecutionFailed, TypeExecutionInterrupted,
		TypeStepStarted, TypeStepStreamed, TypeStepEnded, TypeStepFailed,
		TypeTextStarted, TypeTextDelta, TypeTextEnded, TypeReasoningStarted, TypeReasoningDelta, TypeReasoningEnded,
		TypeToolInputStarted, TypeToolInputDelta, TypeToolInputEnded, TypeToolCalled, TypeToolProgress,
		TypeToolSuccess, TypeToolFailed, TypeCreated, TypeAgentSelected, TypeModelSelected, TypeMoved,
		TypeRenamed, TypeMetadataUpdated, TypePermissions, TypeViewed, TypeUsageUpdated, TypeDeleted,
		TypeForked, TypeInstructionsUpdated, TypeSynthetic, TypeSkillActivated, TypeShellStarted,
		TypeShellEnded, TypeRetryScheduled, TypeCompactionStarted, TypeCompactionDelta, TypeCompactionEnded,
		TypeCompactionFailed, TypeRevertStaged, TypeRevertCleared, TypeRevertCommitted:
		return true
	default:
		return false
	}
}

func (t Type) Durable() bool {
	switch t {
	case TypeTextDelta, TypeReasoningDelta, TypeToolInputDelta, TypeToolProgress, TypeCompactionDelta, TypeUsageUpdated:
		return false
	default:
		return t.Supported()
	}
}

func (t Type) SessionScoped() bool {
	return strings.HasPrefix(string(t), "session.")
}

type DurablePosition struct {
	AggregateID string `json:"aggregateID"`
	Seq         int64  `json:"seq"`
	Version     int    `json:"version"`
}

type Event struct {
	ID        EventID          `json:"id"`
	Created   int64            `json:"created,omitempty"`
	Type      Type             `json:"type"`
	Location  json.RawMessage  `json:"location,omitempty"`
	Metadata  json.RawMessage  `json:"metadata,omitempty"`
	Durable   *DurablePosition `json:"durable,omitempty"`
	Data      json.RawMessage  `json:"data"`
	SessionID SessionID        `json:"-"`
}

func DecodeEvent(data []byte) (Event, error) {
	var envelope struct {
		ID       EventID          `json:"id"`
		Created  int64            `json:"created"`
		Type     Type             `json:"type"`
		Location json.RawMessage  `json:"location"`
		Metadata json.RawMessage  `json:"metadata"`
		Durable  *DurablePosition `json:"durable"`
		Data     json.RawMessage  `json:"data"`
	}
	if err := decodeStrict(data, &envelope, true); err != nil {
		return Event{}, fmt.Errorf("%w: envelope: %v", ErrInvalidWire, err)
	}
	if !envelope.ID.Valid() {
		return Event{}, fmt.Errorf("%w: invalid event id", ErrInvalidWire)
	}
	event := Event{ID: envelope.ID, Created: envelope.Created, Type: envelope.Type, Location: envelope.Location, Metadata: envelope.Metadata, Durable: envelope.Durable, Data: envelope.Data}
	if !envelope.Type.SessionScoped() {
		return event, nil
	}
	var scope struct {
		SessionID SessionID `json:"sessionID"`
	}
	if err := json.Unmarshal(envelope.Data, &scope); err != nil || !scope.SessionID.Valid() {
		return Event{}, fmt.Errorf("%w: %s without a session id", ErrInvalidWire, envelope.Type)
	}
	event.SessionID = scope.SessionID
	if !envelope.Type.Supported() {
		return event, fmt.Errorf("%w: unsupported event type %q", ErrUnsupportedType, envelope.Type)
	}
	if envelope.Type.Durable() {
		if envelope.Durable == nil || envelope.Durable.AggregateID != string(scope.SessionID) || envelope.Durable.Seq < 0 {
			return Event{}, fmt.Errorf("%w: %s without its durable position", ErrInvalidWire, envelope.Type)
		}
	} else if envelope.Durable != nil {
		return Event{}, fmt.Errorf("%w: ephemeral %s with a durable position", ErrInvalidWire, envelope.Type)
	}
	return event, nil
}

var ErrUnsupportedType = errors.New("opencode native: unsupported event type")

type SessionError struct {
	Type     string `json:"type"`
	Message  string `json:"message"`
	Status   *int   `json:"status,omitempty"`
	Response *struct {
		Body string `json:"body"`
	} `json:"response,omitempty"`
}

type UnknownErrorBlock = SessionError

type InboxItem struct {
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
	Delivery Delivery        `json:"delivery"`
}

type InboxEnqueuedData struct {
	SessionID SessionID `json:"sessionID"`
	InboxID   MessageID `json:"inboxID"`
	Item      InboxItem `json:"item"`
}

type InboxRefData struct {
	SessionID SessionID `json:"sessionID"`
	InboxID   MessageID `json:"inboxID"`
}

type InboxDeliveryChangedData struct {
	SessionID SessionID `json:"sessionID"`
	InboxID   MessageID `json:"inboxID"`
	Delivery  Delivery  `json:"delivery"`
}

type ExecutionData struct {
	SessionID SessionID `json:"sessionID"`
}

type ExecutionFailedData struct {
	SessionID SessionID    `json:"sessionID"`
	Error     SessionError `json:"error"`
}

type ExecutionInterruptedData struct {
	SessionID SessionID `json:"sessionID"`
	Reason    string    `json:"reason"`
}

type StepStartedData struct {
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	Agent            string    `json:"agent"`
	Model            ModelRef  `json:"model"`
	Snapshot         string    `json:"snapshot,omitempty"`
	Started          int64     `json:"started"`
}

type StepStreamedData struct {
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
}

type StepEndedData struct {
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	Finish           string          `json:"finish"`
	RawFinish        string          `json:"rawFinish,omitempty"`
	ProviderState    json.RawMessage `json:"providerState,omitempty"`
	Cost             float64         `json:"cost"`
	Tokens           TokenAccounting `json:"tokens"`
	Snapshot         string          `json:"snapshot,omitempty"`
	Files            []string        `json:"files,omitempty"`
}

type StepFailedData struct {
	SessionID        SessionID        `json:"sessionID"`
	AssistantMessage MessageID        `json:"assistantMessageID"`
	Error            SessionError     `json:"error"`
	Finish           string           `json:"finish,omitempty"`
	RawFinish        string           `json:"rawFinish,omitempty"`
	ProviderState    json.RawMessage  `json:"providerState,omitempty"`
	Cost             *float64         `json:"cost,omitempty"`
	Tokens           *TokenAccounting `json:"tokens,omitempty"`
	Snapshot         string           `json:"snapshot,omitempty"`
	Files            []string         `json:"files,omitempty"`
}

type PartStartedData struct {
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	Ordinal          int             `json:"ordinal"`
	State            json.RawMessage `json:"state,omitempty"`
}

type PartDeltaData struct {
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	Ordinal          int       `json:"ordinal"`
	Delta            string    `json:"delta"`
}

type PartEndedData struct {
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	Ordinal          int             `json:"ordinal"`
	Text             string          `json:"text"`
	State            json.RawMessage `json:"state,omitempty"`
}

type TextEndedData = PartEndedData

type ReasoningEndedData = PartEndedData

type ToolInputStartedData struct {
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	ID               string    `json:"id"`
	Name             string    `json:"name"`
}

type ToolInputDeltaData struct {
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	ID               string    `json:"id"`
	Delta            string    `json:"delta"`
}

type ToolInputEndedData struct {
	SessionID        SessionID `json:"sessionID"`
	AssistantMessage MessageID `json:"assistantMessageID"`
	ID               string    `json:"id"`
	Text             string    `json:"text"`
}

type ToolCalledData struct {
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	ID               string          `json:"id"`
	Input            map[string]any  `json:"input"`
	Executed         bool            `json:"executed"`
	State            json.RawMessage `json:"state,omitempty"`
}

type ToolProgressData struct {
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	ID               string          `json:"id"`
	Metadata         json.RawMessage `json:"metadata"`
}

type ToolSuccessData struct {
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	ID               string          `json:"id"`
	Content          []ToolContent   `json:"content"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	Executed         bool            `json:"executed"`
	ResultState      json.RawMessage `json:"resultState,omitempty"`
}

type ToolFailedData struct {
	SessionID        SessionID       `json:"sessionID"`
	AssistantMessage MessageID       `json:"assistantMessageID"`
	ID               string          `json:"id"`
	Error            SessionError    `json:"error"`
	Content          []ToolContent   `json:"content,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	Executed         bool            `json:"executed"`
	ResultState      json.RawMessage `json:"resultState,omitempty"`
}

type ToolContent struct {
	Type string          `json:"type"`
	Text string          `json:"text,omitempty"`
	URI  string          `json:"uri,omitempty"`
	MIME string          `json:"mime,omitempty"`
	Name string          `json:"name,omitempty"`
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
	if err := jsonwalk.RejectDuplicateKeys(data); err != nil {
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
