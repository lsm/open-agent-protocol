package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	Version = "v0.85.1"
	Commit  = "d981de1229ef899957bbe968bc8dcda02a21f477"
)

var ErrInvalid = errors.New("pi native: invalid v0.85.1 message")

type CommandType string

const (
	CommandPrompt                     CommandType = "prompt"
	CommandSteer                      CommandType = "steer"
	CommandFollowUp                   CommandType = "follow_up"
	CommandAbort                      CommandType = "abort"
	CommandClearQueue                 CommandType = "clear_queue"
	CommandNewSession                 CommandType = "new_session"
	CommandGetState                   CommandType = "get_state"
	CommandSetModel                   CommandType = "set_model"
	CommandCycleModel                 CommandType = "cycle_model"
	CommandGetAvailableModels         CommandType = "get_available_models"
	CommandSetThinkingLevel           CommandType = "set_thinking_level"
	CommandCycleThinkingLevel         CommandType = "cycle_thinking_level"
	CommandGetAvailableThinkingLevels CommandType = "get_available_thinking_levels"
	CommandSetSteeringMode            CommandType = "set_steering_mode"
	CommandSetFollowUpMode            CommandType = "set_follow_up_mode"
	CommandCompact                    CommandType = "compact"
	CommandSetAutoCompaction          CommandType = "set_auto_compaction"
	CommandSetAutoRetry               CommandType = "set_auto_retry"
	CommandAbortRetry                 CommandType = "abort_retry"
	CommandBash                       CommandType = "bash"
	CommandAbortBash                  CommandType = "abort_bash"
	CommandGetSessionStats            CommandType = "get_session_stats"
	CommandExportHTML                 CommandType = "export_html"
	CommandSwitchSession              CommandType = "switch_session"
	CommandFork                       CommandType = "fork"
	CommandClone                      CommandType = "clone"
	CommandGetForkMessages            CommandType = "get_fork_messages"
	CommandGetEntries                 CommandType = "get_entries"
	CommandGetTree                    CommandType = "get_tree"
	CommandGetLastAssistantText       CommandType = "get_last_assistant_text"
	CommandSetSessionName             CommandType = "set_session_name"
	CommandGetMessages                CommandType = "get_messages"
	CommandGetCommands                CommandType = "get_commands"
)

type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

type QueueMode string

const (
	QueueAll        QueueMode = "all"
	QueueOneAtATime QueueMode = "one-at-a-time"
)

type StreamingBehavior string

const (
	StreamingSteer    StreamingBehavior = "steer"
	StreamingFollowUp StreamingBehavior = "followUp"
)

type ImageContent struct {
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type Command struct {
	ID                 string            `json:"id,omitempty"`
	Type               CommandType       `json:"type"`
	Message            *string           `json:"message,omitempty"`
	Images             []ImageContent    `json:"images,omitempty"`
	StreamingBehavior  StreamingBehavior `json:"streamingBehavior,omitempty"`
	ParentSession      *string           `json:"parentSession,omitempty"`
	Provider           *string           `json:"provider,omitempty"`
	ModelID            *string           `json:"modelId,omitempty"`
	Level              ThinkingLevel     `json:"level,omitempty"`
	Mode               QueueMode         `json:"mode,omitempty"`
	CustomInstructions *string           `json:"customInstructions,omitempty"`
	Enabled            *bool             `json:"enabled,omitempty"`
	BashCommand        *string           `json:"command,omitempty"`
	ExcludeFromContext *bool             `json:"excludeFromContext,omitempty"`
	OutputPath         *string           `json:"outputPath,omitempty"`
	SessionPath        *string           `json:"sessionPath,omitempty"`
	EntryID            *string           `json:"entryId,omitempty"`
	Since              *string           `json:"since,omitempty"`
	Name               *string           `json:"name,omitempty"`
}

func String(value string) *string { return &value }
func Bool(value bool) *bool       { return &value }

func (c Command) Validate() error {
	if !validCommandType(c.Type) {
		return fmt.Errorf("%w: unknown command type %q", ErrInvalid, c.Type)
	}
	if err := c.validateFields(); err != nil {
		return err
	}
	require := func(value *string, name string) error {
		if value == nil {
			return fmt.Errorf("%w: %s requires %s", ErrInvalid, c.Type, name)
		}
		return nil
	}
	switch c.Type {
	case CommandPrompt, CommandSteer, CommandFollowUp:
		if err := require(c.Message, "message"); err != nil {
			return err
		}
	case CommandSetModel:
		if err := require(c.Provider, "provider"); err != nil {
			return err
		}
		if err := require(c.ModelID, "modelId"); err != nil {
			return err
		}
	case CommandSetThinkingLevel:
		if !validThinking(c.Level) {
			return fmt.Errorf("%w: invalid thinking level %q", ErrInvalid, c.Level)
		}
	case CommandSetSteeringMode, CommandSetFollowUpMode:
		if c.Mode != QueueAll && c.Mode != QueueOneAtATime {
			return fmt.Errorf("%w: invalid queue mode %q", ErrInvalid, c.Mode)
		}
	case CommandSetAutoCompaction, CommandSetAutoRetry:
		if c.Enabled == nil {
			return fmt.Errorf("%w: %s requires enabled", ErrInvalid, c.Type)
		}
	case CommandBash:
		if err := require(c.BashCommand, "command"); err != nil {
			return err
		}
	case CommandSwitchSession:
		if err := require(c.SessionPath, "sessionPath"); err != nil {
			return err
		}
	case CommandFork:
		if err := require(c.EntryID, "entryId"); err != nil {
			return err
		}
	case CommandSetSessionName:
		if err := require(c.Name, "name"); err != nil {
			return err
		}
	}
	return nil
}

func (c Command) validateFields() error {
	allowed := map[string]bool{"id": true, "type": true}
	add := func(names ...string) {
		for _, name := range names {
			allowed[name] = true
		}
	}
	switch c.Type {
	case CommandPrompt:
		add("message", "images", "streamingBehavior")
	case CommandSteer, CommandFollowUp:
		add("message", "images")
	case CommandNewSession:
		add("parentSession")
	case CommandSetModel:
		add("provider", "modelId")
	case CommandSetThinkingLevel:
		add("level")
	case CommandSetSteeringMode, CommandSetFollowUpMode:
		add("mode")
	case CommandCompact:
		add("customInstructions")
	case CommandSetAutoCompaction, CommandSetAutoRetry:
		add("enabled")
	case CommandBash:
		add("command", "excludeFromContext")
	case CommandExportHTML:
		add("outputPath")
	case CommandSwitchSession:
		add("sessionPath")
	case CommandFork:
		add("entryId")
	case CommandGetEntries:
		add("since")
	case CommandSetSessionName:
		add("name")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name := range fields {
		if !allowed[name] {
			return fmt.Errorf("%w: field %s is not valid for %s", ErrInvalid, name, c.Type)
		}
	}
	return nil
}

func validCommandType(t CommandType) bool {
	switch t {
	case CommandPrompt, CommandSteer, CommandFollowUp, CommandAbort, CommandClearQueue,
		CommandNewSession, CommandGetState, CommandSetModel, CommandCycleModel,
		CommandGetAvailableModels, CommandSetThinkingLevel, CommandCycleThinkingLevel,
		CommandGetAvailableThinkingLevels, CommandSetSteeringMode, CommandSetFollowUpMode,
		CommandCompact, CommandSetAutoCompaction, CommandSetAutoRetry, CommandAbortRetry,
		CommandBash, CommandAbortBash, CommandGetSessionStats, CommandExportHTML,
		CommandSwitchSession, CommandFork, CommandClone, CommandGetForkMessages,
		CommandGetEntries, CommandGetTree, CommandGetLastAssistantText,
		CommandSetSessionName, CommandGetMessages, CommandGetCommands:
		return true
	default:
		return false
	}
}

func validThinking(level ThinkingLevel) bool {
	switch level {
	case ThinkingOff, ThinkingMinimal, ThinkingLow, ThinkingMedium, ThinkingHigh, ThinkingXHigh, ThinkingMax:
		return true
	default:
		return false
	}
}

type Response struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Command CommandType     `json:"command"`
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func (r Response) Validate() error {
	if r.Type != "response" || !validCommandType(r.Command) {
		return fmt.Errorf("%w: invalid response discriminator", ErrInvalid)
	}
	if r.Success {
		if r.Error != "" {
			return fmt.Errorf("%w: successful response has error", ErrInvalid)
		}
	} else if r.Error == "" {
		return fmt.Errorf("%w: failed response requires error", ErrInvalid)
	}
	if len(r.Data) > 0 && !json.Valid(r.Data) {
		return fmt.Errorf("%w: invalid response data", ErrInvalid)
	}
	return nil
}

type SessionState struct {
	Model                 json.RawMessage `json:"model,omitempty"`
	ThinkingLevel         ThinkingLevel   `json:"thinkingLevel"`
	IsStreaming           bool            `json:"isStreaming"`
	IsCompacting          bool            `json:"isCompacting"`
	SteeringMode          QueueMode       `json:"steeringMode"`
	FollowUpMode          QueueMode       `json:"followUpMode"`
	SessionFile           string          `json:"sessionFile,omitempty"`
	SessionID             string          `json:"sessionId"`
	SessionName           string          `json:"sessionName,omitempty"`
	AutoCompactionEnabled bool            `json:"autoCompactionEnabled"`
	MessageCount          int             `json:"messageCount"`
	PendingMessageCount   int             `json:"pendingMessageCount"`
}

type EventType string

const (
	EventAgentStart                     EventType = "agent_start"
	EventAgentEnd                       EventType = "agent_end"
	EventAgentSettled                   EventType = "agent_settled"
	EventTurnStart                      EventType = "turn_start"
	EventTurnEnd                        EventType = "turn_end"
	EventMessageStart                   EventType = "message_start"
	EventMessageUpdate                  EventType = "message_update"
	EventMessageEnd                     EventType = "message_end"
	EventToolExecutionStart             EventType = "tool_execution_start"
	EventToolExecutionUpdate            EventType = "tool_execution_update"
	EventToolExecutionEnd               EventType = "tool_execution_end"
	EventQueueUpdate                    EventType = "queue_update"
	EventCompactionStart                EventType = "compaction_start"
	EventCompactionEnd                  EventType = "compaction_end"
	EventEntryAppended                  EventType = "entry_appended"
	EventSessionInfoChanged             EventType = "session_info_changed"
	EventThinkingLevelChanged           EventType = "thinking_level_changed"
	EventAutoRetryStart                 EventType = "auto_retry_start"
	EventAutoRetryEnd                   EventType = "auto_retry_end"
	EventSummarizationRetryScheduled    EventType = "summarization_retry_scheduled"
	EventSummarizationRetryAttemptStart EventType = "summarization_retry_attempt_start"
	EventSummarizationRetryFinished     EventType = "summarization_retry_finished"
	EventBashExecutionUpdate            EventType = "bash_execution_update"
	EventExtensionError                 EventType = "extension_error"
)

func ValidateEvent(data []byte, eventType EventType) error {
	if !ValidEventType(eventType) {
		return fmt.Errorf("%w: unknown event type %q", ErrInvalid, eventType)
	}
	var object map[string]json.RawMessage
	if err := DecodeStrict(data, &object); err != nil {
		return err
	}
	required := []string{"type"}
	allowed := map[string]bool{"type": true}
	add := func(names ...string) {
		for _, name := range names {
			allowed[name] = true
		}
	}
	require := func(names ...string) { required = append(required, names...); add(names...) }
	switch eventType {
	case EventAgentEnd:
		require("messages", "willRetry")
	case EventTurnEnd:
		require("message", "toolResults")
	case EventMessageStart, EventMessageEnd:
		require("message")
	case EventMessageUpdate:
		require("usage", "assistantMessageEvent")
	case EventToolExecutionStart:
		require("toolCallId", "toolName", "args")
	case EventToolExecutionUpdate:
		require("toolCallId", "toolName", "args", "partialResult")
	case EventToolExecutionEnd:
		require("toolCallId", "toolName", "result", "isError")
	case EventQueueUpdate:
		require("steering", "followUp")
	case EventCompactionStart:
		require("reason")
	case EventCompactionEnd:
		require("reason", "aborted", "willRetry")
		add("result", "errorMessage")
	case EventEntryAppended:
		require("entry")
	case EventSessionInfoChanged:
		add("name")
	case EventThinkingLevelChanged:
		require("level")
	case EventAutoRetryStart, EventSummarizationRetryScheduled:
		require("attempt", "maxAttempts", "delayMs", "errorMessage")
	case EventAutoRetryEnd:
		require("success", "attempt")
		add("finalError")
	case EventSummarizationRetryAttemptStart:
		require("source")
		add("reason")
	case EventBashExecutionUpdate:
		require("delta")
		add("id")
	case EventExtensionError:
		require("extensionPath", "event", "error")
	}
	for _, name := range required {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("%w: %s event requires %s", ErrInvalid, eventType, name)
		}
	}
	for name := range object {
		if !allowed[name] {
			return fmt.Errorf("%w: field %s is not valid for %s", ErrInvalid, name, eventType)
		}
	}
	return nil
}

func ValidEventType(t EventType) bool {
	switch t {
	case EventAgentStart, EventAgentEnd, EventAgentSettled, EventTurnStart, EventTurnEnd,
		EventMessageStart, EventMessageUpdate, EventMessageEnd, EventToolExecutionStart,
		EventToolExecutionUpdate, EventToolExecutionEnd, EventQueueUpdate,
		EventCompactionStart, EventCompactionEnd, EventEntryAppended,
		EventSessionInfoChanged, EventThinkingLevelChanged, EventAutoRetryStart,
		EventAutoRetryEnd, EventSummarizationRetryScheduled,
		EventSummarizationRetryAttemptStart, EventSummarizationRetryFinished,
		EventBashExecutionUpdate, EventExtensionError:
		return true
	default:
		return false
	}
}

type Event struct {
	Type EventType
	Raw  json.RawMessage
}

func (e Event) MarshalJSON() ([]byte, error) {
	if !ValidEventType(e.Type) || !json.Valid(e.Raw) {
		return nil, ErrInvalid
	}
	return append([]byte(nil), e.Raw...), nil
}

type ExtensionMethod string

const (
	ExtensionSelect        ExtensionMethod = "select"
	ExtensionConfirm       ExtensionMethod = "confirm"
	ExtensionInput         ExtensionMethod = "input"
	ExtensionEditor        ExtensionMethod = "editor"
	ExtensionNotify        ExtensionMethod = "notify"
	ExtensionSetStatus     ExtensionMethod = "setStatus"
	ExtensionSetWidget     ExtensionMethod = "setWidget"
	ExtensionSetTitle      ExtensionMethod = "setTitle"
	ExtensionSetEditorText ExtensionMethod = "set_editor_text"
)

type ExtensionUIRequest struct {
	Type            string          `json:"type"`
	ID              string          `json:"id"`
	Method          ExtensionMethod `json:"method"`
	Title           string          `json:"title,omitempty"`
	Options         []string        `json:"options,omitempty"`
	Timeout         *int64          `json:"timeout,omitempty"`
	Message         string          `json:"message,omitempty"`
	Placeholder     string          `json:"placeholder,omitempty"`
	Prefill         string          `json:"prefill,omitempty"`
	NotifyType      string          `json:"notifyType,omitempty"`
	StatusKey       string          `json:"statusKey,omitempty"`
	StatusText      *string         `json:"statusText,omitempty"`
	WidgetKey       string          `json:"widgetKey,omitempty"`
	WidgetLines     []string        `json:"widgetLines,omitempty"`
	WidgetPlacement string          `json:"widgetPlacement,omitempty"`
	Text            string          `json:"text,omitempty"`
}

func (r ExtensionUIRequest) Validate() error {
	if r.Type != "extension_ui_request" || r.ID == "" {
		return fmt.Errorf("%w: invalid extension request", ErrInvalid)
	}
	require := func(value, name string) error {
		if value == "" {
			return fmt.Errorf("%w: %s requires %s", ErrInvalid, r.Method, name)
		}
		return nil
	}
	switch r.Method {
	case ExtensionSelect:
		if err := require(r.Title, "title"); err != nil {
			return err
		}
		if r.Options == nil {
			return fmt.Errorf("%w: select requires options", ErrInvalid)
		}
	case ExtensionConfirm:
		if err := require(r.Title, "title"); err != nil {
			return err
		}
		if err := require(r.Message, "message"); err != nil {
			return err
		}
	case ExtensionInput, ExtensionEditor:
		if err := require(r.Title, "title"); err != nil {
			return err
		}
	case ExtensionNotify:
		if err := require(r.Message, "message"); err != nil {
			return err
		}
		if r.NotifyType != "" && r.NotifyType != "info" && r.NotifyType != "warning" && r.NotifyType != "error" {
			return fmt.Errorf("%w: invalid notifyType", ErrInvalid)
		}
	case ExtensionSetStatus:
		if err := require(r.StatusKey, "statusKey"); err != nil {
			return err
		}
	case ExtensionSetWidget:
		if err := require(r.WidgetKey, "widgetKey"); err != nil {
			return err
		}
		if r.WidgetPlacement != "" && r.WidgetPlacement != "aboveEditor" && r.WidgetPlacement != "belowEditor" {
			return fmt.Errorf("%w: invalid widgetPlacement", ErrInvalid)
		}
	case ExtensionSetTitle:
		if err := require(r.Title, "title"); err != nil {
			return err
		}
	case ExtensionSetEditorText:

	default:
		return fmt.Errorf("%w: unknown extension method %q", ErrInvalid, r.Method)
	}
	return r.validateFields()
}

func (r ExtensionUIRequest) validateFields() error {
	allowed := map[string]bool{"type": true, "id": true, "method": true}
	add := func(names ...string) {
		for _, name := range names {
			allowed[name] = true
		}
	}
	switch r.Method {
	case ExtensionSelect:
		add("title", "options", "timeout")
	case ExtensionConfirm:
		add("title", "message", "timeout")
	case ExtensionInput:
		add("title", "placeholder", "timeout")
	case ExtensionEditor:
		add("title", "prefill")
	case ExtensionNotify:
		add("message", "notifyType")
	case ExtensionSetStatus:
		add("statusKey", "statusText")
	case ExtensionSetWidget:
		add("widgetKey", "widgetLines", "widgetPlacement")
	case ExtensionSetTitle:
		add("title")
	case ExtensionSetEditorText:
		add("text")
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name := range fields {
		if !allowed[name] {
			return fmt.Errorf("%w: field %s is not valid for %s", ErrInvalid, name, r.Method)
		}
	}
	return nil
}

type ExtensionUIResponse struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	Value     *string `json:"value,omitempty"`
	Confirmed *bool   `json:"confirmed,omitempty"`
	Cancelled bool    `json:"cancelled,omitempty"`
}

func (r ExtensionUIResponse) Validate() error {
	if r.Type != "extension_ui_response" || r.ID == "" {
		return fmt.Errorf("%w: invalid extension response", ErrInvalid)
	}
	count := 0
	if r.Value != nil {
		count++
	}
	if r.Confirmed != nil {
		count++
	}
	if r.Cancelled {
		count++
	}
	if count != 1 {
		return fmt.Errorf("%w: extension response requires exactly one outcome", ErrInvalid)
	}
	return nil
}

func DecodeStrict(data []byte, dst any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
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
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
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
		default:
			return errors.New("unexpected closing delimiter")
		}
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
