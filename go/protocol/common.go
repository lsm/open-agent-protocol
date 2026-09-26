package protocol

import (
	"encoding/json"
	"fmt"
)

type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleDeveloper MessageRole = "developer"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

type ContentPartType string

const (
	ContentText       ContentPartType = "text"
	ContentReasoning  ContentPartType = "reasoning"
	ContentImage      ContentPartType = "image"
	ContentToolCall   ContentPartType = "tool_call"
	ContentToolResult ContentPartType = "tool_result"
)

type MessageContent json.RawMessage

func (c *MessageContent) UnmarshalJSON(data []byte) error {
	if !json.Valid(data) {
		return fmt.Errorf("invalid message content JSON")
	}
	*c = append((*c)[:0], data...)
	return nil
}

func (c MessageContent) MarshalJSON() ([]byte, error) {
	if len(c) == 0 {
		return []byte("null"), nil
	}
	if !json.Valid(c) {
		return nil, fmt.Errorf("invalid message content JSON")
	}
	return append([]byte(nil), c...), nil
}

func TextContent(text string) MessageContent {
	data, _ := json.Marshal(text)
	return MessageContent(data)
}

func PartsContent(parts []ContentPart) MessageContent {
	data, _ := json.Marshal(parts)
	return MessageContent(data)
}

func (c MessageContent) Text() (string, bool) {
	var text string
	if json.Unmarshal(c, &text) != nil {
		return "", false
	}
	return text, true
}

func (c MessageContent) Parts() ([]ContentPart, bool) {
	var parts []ContentPart
	if json.Unmarshal(c, &parts) != nil {
		return nil, false
	}
	return parts, true
}

type Message struct {
	ID       MessageID                  `json:"id,omitempty"`
	Role     MessageRole                `json:"role"`
	Content  MessageContent             `json:"content"`
	Metadata map[string]json.RawMessage `json:"metadata,omitempty"`
}

type ContentPart struct {
	Type          ContentPartType `json:"type"`
	Text          string          `json:"text,omitempty"`
	Reasoning     string          `json:"reasoning,omitempty"`
	Image         *ImageContent   `json:"image,omitempty"`
	ToolCallID    ToolCallID      `json:"tool_call_id,omitempty"`
	Name          string          `json:"name,omitempty"`
	ArgumentsJSON json.RawMessage `json:"arguments_json,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	IsError       *bool           `json:"is_error,omitempty"`
	Carry         string          `json:"carry,omitempty"`
}

func (p ContentPart) MarshalJSON() ([]byte, error) {
	type contentPart struct {
		Type          ContentPartType `json:"type"`
		Text          *string         `json:"text,omitempty"`
		Reasoning     *string         `json:"reasoning,omitempty"`
		Image         *ImageContent   `json:"image,omitempty"`
		ToolCallID    ToolCallID      `json:"tool_call_id,omitempty"`
		Name          string          `json:"name,omitempty"`
		ArgumentsJSON json.RawMessage `json:"arguments_json,omitempty"`
		Result        json.RawMessage `json:"result,omitempty"`
		IsError       *bool           `json:"is_error,omitempty"`
		Carry         string          `json:"carry,omitempty"`
	}
	out := contentPart{
		Type: p.Type, Image: p.Image, ToolCallID: p.ToolCallID,
		Name: p.Name, ArgumentsJSON: p.ArgumentsJSON, Result: p.Result, IsError: p.IsError, Carry: p.Carry,
	}
	if p.Type == ContentText {
		text := p.Text
		out.Text = &text
	}
	if p.Type == ContentReasoning {
		reasoning := p.Reasoning
		out.Reasoning = &reasoning
	}
	return json.Marshal(out)
}

type ImageContent struct {
	URL       string `json:"url,omitempty"`
	Data      string `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

type Usage struct {
	InputTokens  uint64 `json:"input_tokens,omitempty"`
	OutputTokens uint64 `json:"output_tokens,omitempty"`
	TotalTokens  uint64 `json:"total_tokens,omitempty"`
}

type ProtocolError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retriable *bool          `json:"retriable,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

type ErrorResponse struct {
	Error ProtocolError `json:"error"`
}

type RecoveryMetadata struct {
	Recovered         bool      `json:"recovered,omitempty"`
	PreviousSessionID SessionID `json:"previous_session_id,omitempty"`
	PreviousRunID     RunID     `json:"previous_run_id,omitempty"`
	ResumeCursor      string    `json:"resume_cursor,omitempty"`
	Reason            string    `json:"reason,omitempty"`
}
