package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type EventType string

const (
	EventAgentStart          EventType = "agent_start"
	EventAgentEnd            EventType = "agent_end"
	EventTurnStart           EventType = "turn_start"
	EventTurnEnd             EventType = "turn_end"
	EventMessageStart        EventType = "message_start"
	EventMessageUpdate       EventType = "message_update"
	EventMessageEnd          EventType = "message_end"
	EventContextUsage        EventType = "context_usage"
	EventPromptSegmentUsage  EventType = "prompt_segment_usage"
	EventToolExecutionStart  EventType = "tool_execution_start"
	EventToolExecutionUpdate EventType = "tool_execution_update"
	EventToolExecutionEnd    EventType = "tool_execution_end"
)

type EventHeader struct {
	Type EventType `json:"type"`
}

type AgentStartEvent struct {
	Type      EventType `json:"type"`
	SessionID SessionID `json:"session_id"`
}
type AgentEndEvent struct {
	Type         EventType `json:"type"`
	StopReason   string    `json:"stop_reason,omitempty"`
	ProviderID   string    `json:"provider_id,omitempty"`
	API          string    `json:"api,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
}
type TurnEvent struct {
	Type         EventType `json:"type"`
	StopReason   string    `json:"stop_reason,omitempty"`
	ErrorMessage string    `json:"error_message,omitempty"`
}
type MessageStartEvent struct {
	Type     EventType `json:"type"`
	API      string    `json:"api,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
}
type MessageUpdateEvent struct {
	Type  EventType       `json:"type"`
	Event json.RawMessage `json:"event"`
}
type MessageEndEvent struct {
	Type         EventType       `json:"type"`
	StopReason   string          `json:"stop_reason,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"`
}
type ContextUsageEvent struct {
	Type                EventType `json:"type"`
	SystemPromptBytes   uint64    `json:"system_prompt_bytes"`
	MessageBytes        uint64    `json:"message_bytes"`
	ToolDefinitionBytes uint64    `json:"tool_definition_bytes"`
	TotalBytes          uint64    `json:"total_bytes"`
	EstimatedTokens     uint64    `json:"estimated_tokens"`
	MessageCount        uint32    `json:"message_count"`
	ToolCount           uint32    `json:"tool_count"`
}
type PromptSegmentUsageEvent struct {
	Type            EventType `json:"type"`
	Segment         string    `json:"segment"`
	CacheRole       string    `json:"cache_role"`
	Bytes           uint64    `json:"bytes"`
	EstimatedTokens uint64    `json:"estimated_tokens"`
	ItemCount       uint32    `json:"item_count"`
}
type ToolExecutionStartEvent struct {
	Type       EventType `json:"type"`
	ToolCallID string    `json:"tool_call_id"`
	ToolName   string    `json:"tool_name"`
	ArgsJSON   string    `json:"args_json"`
}
type ToolExecutionUpdateEvent struct {
	Type              EventType `json:"type"`
	ToolCallID        string    `json:"tool_call_id"`
	ToolName          string    `json:"tool_name"`
	PartialResultJSON string    `json:"partial_result_json"`
}
type ToolExecutionEndEvent struct {
	Type                    EventType       `json:"type"`
	ToolCallID              string          `json:"tool_call_id"`
	ToolName                string          `json:"tool_name"`
	ResultJSON              string          `json:"result_json"`
	ContentJSON             string          `json:"content_json,omitempty"`
	IsError                 bool            `json:"is_error"`
	ArgsBytes               uint64          `json:"args_bytes"`
	RawResultBytes          uint64          `json:"raw_result_bytes"`
	ReturnedResultBytes     uint64          `json:"returned_result_bytes"`
	RawDetailsBytes         uint64          `json:"raw_details_bytes"`
	ReturnedDetailsBytes    uint64          `json:"returned_details_bytes"`
	RawTotalBytes           uint64          `json:"raw_total_bytes"`
	ReturnedTotalBytes      uint64          `json:"returned_total_bytes"`
	EstimatedReturnedTokens uint64          `json:"estimated_returned_tokens"`
	ArtifactCount           uint32          `json:"artifact_count"`
	Artifacts               json.RawMessage `json:"artifacts,omitempty"`
}

type ProviderEventHeader struct {
	Type string `json:"type"`
}
type TextDeltaEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}
type ReasoningDeltaEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
}

type Result struct {
	Type         string             `json:"type"`
	StopReason   string             `json:"stop_reason"`
	Model        string             `json:"model"`
	API          string             `json:"api"`
	Provider     string             `json:"provider"`
	Timestamp    int64              `json:"timestamp"`
	Input        uint64             `json:"input"`
	Output       uint64             `json:"output"`
	CacheRead    uint64             `json:"cache_read"`
	CacheWrite   uint64             `json:"cache_write"`
	Content      []AssistantContent `json:"content"`
	ErrorMessage string             `json:"error_message,omitempty"`
}
type AssistantContent struct {
	Type              string `json:"type"`
	Text              string `json:"text,omitempty"`
	Thinking          string `json:"thinking,omitempty"`
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	ArgumentsJSON     string `json:"arguments_json,omitempty"`
	Data              string `json:"data,omitempty"`
	MimeType          string `json:"mime_type,omitempty"`
	TextSignature     string `json:"text_signature,omitempty"`
	ThinkingSignature string `json:"thinking_signature,omitempty"`
	ThoughtSignature  string `json:"thought_signature,omitempty"`
}

func DecodeResult(raw string) (Result, error) {
	var result Result
	if err := decodeNested(raw, &result, true); err != nil {
		return result, fmt.Errorf("makai native: invalid agent result: %w", err)
	}
	if result.Type != "result" || result.StopReason == "" {
		return result, errors.New("makai native: invalid agent result type or stop reason")
	}
	for _, content := range result.Content {
		switch content.Type {
		case "text":
		case "thinking":
		case "tool_call":
			if content.ID == "" || content.Name == "" || !validEmbeddedJSON(content.ArgumentsJSON) {
				return result, errors.New("makai native: invalid result tool call")
			}
		case "image":
			if content.Data == "" || content.MimeType == "" {
				return result, errors.New("makai native: invalid result image")
			}
		default:
			return result, fmt.Errorf("makai native: unknown result content type %q", content.Type)
		}
	}
	return result, nil
}

func DecodeEvent(raw string, target any) (EventHeader, error) {
	var header EventHeader
	if err := decodeNested(raw, &header, false); err != nil || header.Type == "" {
		return header, fmt.Errorf("makai native: invalid agent event: %w", firstNestedError(err, "missing type"))
	}
	if target != nil {
		if err := decodeNested(raw, target, true); err != nil {
			return header, fmt.Errorf("makai native: invalid %s event: %w", header.Type, err)
		}
	}
	return header, nil
}

func DecodeProviderEvent(raw json.RawMessage, target any) (ProviderEventHeader, error) {
	var header ProviderEventHeader
	if err := decodeNestedBytes(raw, &header, false); err != nil || header.Type == "" {
		return header, fmt.Errorf("makai native: invalid provider event: %w", firstNestedError(err, "missing type"))
	}
	if target != nil {
		if err := decodeNestedBytes(raw, target, false); err != nil {
			return header, fmt.Errorf("makai native: invalid provider event %s: %w", header.Type, err)
		}
	}
	return header, nil
}

func decodeNested(raw string, target any, strict bool) error {
	return decodeNestedBytes([]byte(raw), target, strict)
}
func decodeNestedBytes(raw []byte, target any, strict bool) error {
	if len(raw) == 0 || rejectDuplicateKeys(raw) != nil {
		return errors.New("malformed or duplicate-key JSON")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		d.DisallowUnknownFields()
	}
	if err := d.Decode(target); err != nil {
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
func firstNestedError(err error, fallback string) error {
	if err != nil {
		return err
	}
	return errors.New(fallback)
}
