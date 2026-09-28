package provider

import (
	"encoding/json"
	"strings"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type StopReason string

const (
	StopAborted StopReason = "aborted"
	StopError   StopReason = "error"
)

const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceFunction = "function"
)

type ToolChoice struct {
	Mode     string
	Function string
}

type TextPart struct {
	Text string
}

type ImagePart struct {
	URL    string
	Detail string
}

type ThinkingPart struct {
	Thinking  string
	Signature string
}

type ToolCall struct {
	ID         string
	Name       string
	Arguments  string
	ThoughtSig string
	HasThought bool
}

type ContentPart struct {
	Text     *TextPart
	Image    *ImagePart
	Thinking *ThinkingPart
	ToolCall *ToolCall
}

type UserContent struct {
	Text     string
	HasText  bool
	Parts    []ContentPart
	UseParts bool
}

type AssistantContent struct {
	Parts      []ContentPart
	StopReason StopReason
}

type AssistantBlock struct {
	Text     *TextPart
	Thinking *ThinkingPart
	ToolCall *ToolCall
}

type ToolResult struct {
	ToolCallID string
	ToolName   string
	Parts      []ContentPart
}

type Message struct {
	User       *UserContent
	Assistant  *AssistantContent
	ToolResult *ToolResult
}

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type Context struct {
	Messages     []Message
	SystemPrompt string
	HasSystem    bool
	Tools        []Tool
}

type StreamOptions struct {
	MaxTokens       int
	HasMaxTokens    bool
	Temperature     float64
	HasTemperature  bool
	ReasoningEffort string
	ToolChoice      ToolChoice
	HasToolChoice   bool
}

func IsOpenRouterAnthropic(model Model) bool {
	if !model.HasBaseURL || model.BaseURL == "" {
		return false
	}
	if !strings.Contains(model.BaseURL, "openrouter") {
		return false
	}
	return strings.HasPrefix(model.ID, "anthropic/")
}

func shouldSkipAssistant(msg Message) bool {
	if msg.Assistant == nil {
		return false
	}
	return msg.Assistant.StopReason == StopAborted || msg.Assistant.StopReason == StopError
}

func collectToolCallIDs(messages []Message) map[string]bool {
	ids := map[string]bool{}
	for _, msg := range messages {
		if msg.Assistant == nil {
			continue
		}
		for _, part := range msg.Assistant.Parts {
			if part.ToolCall != nil {
				ids[part.ToolCall.ID] = true
			}
		}
	}
	return ids
}

func isOrphanedToolResult(msg Message, ids map[string]bool) bool {
	if len(ids) == 0 {
		return false
	}
	if msg.ToolResult == nil {
		return false
	}
	if len(msg.ToolResult.ToolCallID) > 0 {
		return !ids[msg.ToolResult.ToolCallID]
	}
	return false
}

func hasVisibleText(s string) bool {
	if s == "" {
		return false
	}
	return strings.Trim(s, " \t\r\n") != ""
}

func partIsVisibleText(part ContentPart) bool {
	return part.Text != nil && hasVisibleText(part.Text.Text)
}

func partIsVisibleThinking(part ContentPart) bool {
	return part.Thinking != nil && hasVisibleText(part.Thinking.Thinking)
}
