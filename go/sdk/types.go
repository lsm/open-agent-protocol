package sdk

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentPartType string

const (
	PartText       ContentPartType = "text"
	PartThinking   ContentPartType = "thinking"
	PartImage      ContentPartType = "image"
	PartToolCall   ContentPartType = "tool_call"
	PartToolResult ContentPartType = "tool_result"
)

type ContentPart struct {
	Type ContentPartType `json:"type"`

	Text          string `json:"text,omitempty"`
	TextSignature string `json:"text_signature,omitempty"`

	Thinking          string `json:"thinking,omitempty"`
	ThinkingSignature string `json:"thinking_signature,omitempty"`

	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	ImageURL string `json:"image_url,omitempty"`

	ToolCallID    string `json:"tool_call_id,omitempty"`
	Name          string `json:"name,omitempty"`
	ArgumentsJSON string `json:"arguments_json,omitempty"`

	ToolCallCarry string `json:"tool_call_carry,omitempty"`

	ToolName    string        `json:"tool_name,omitempty"`
	Content     []ContentPart `json:"content,omitempty"`
	IsError     bool          `json:"is_error,omitempty"`
	DetailsJSON string        `json:"details_json,omitempty"`
}

type Message struct {
	Role  Role
	Text  string
	Parts []ContentPart

	Name string

	ToolCallID string
}

func UserMessage(text string) Message { return Message{Role: RoleUser, Text: text} }

func SystemMessage(text string) Message { return Message{Role: RoleSystem, Text: text} }

func AssistantMessage(text string) Message { return Message{Role: RoleAssistant, Text: text} }

func ToolMessage(toolCallID, toolName, result string) Message {
	return Message{Role: RoleTool, Name: toolName, ToolCallID: toolCallID, Text: result}
}

type ToolInvocation struct {
	ToolCallID string

	ToolName string

	ArgumentsJSON string
}

type ToolFunc func(ctx context.Context, call ToolInvocation) (string, error)

type Tool struct {
	Name string

	Description string

	ParametersSchemaJSON string

	Execute ToolFunc
}

type ReasoningLevel string

const (
	ReasoningOff     ReasoningLevel = "off"
	ReasoningMinimal ReasoningLevel = "minimal"
	ReasoningLow     ReasoningLevel = "low"
	ReasoningMedium  ReasoningLevel = "medium"
	ReasoningHigh    ReasoningLevel = "high"
	ReasoningXHigh   ReasoningLevel = "xhigh"
	ReasoningMax     ReasoningLevel = "max"
)

type RunOptions struct {
	Temperature *float64

	MaxTokens *int

	ReasoningEffort ReasoningLevel

	Metadata map[string]string

	SessionID string
}

func Temperature(t float64) *float64 { return &t }

func MaxTokens(n int) *int { return &n }

type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
}

type ResponseMessage struct {
	Role Role

	Text string

	Parts []ContentPart
}

type CompletionResponse struct {
	Message ResponseMessage

	Usage *Usage

	ProviderID string
	API        string
	ModelID    string

	StopReason string

	ErrorMessage string
}

type CompletionRequest struct {
	ModelRef string

	Messages []Message

	Tools []Tool

	Options *RunOptions
}

type AgentRequest struct {
	ModelRef string

	Messages []Message

	Tools []Tool

	Options *RunOptions
}
