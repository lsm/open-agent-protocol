// Package native contains the deliberately reduced Codex app-server wire model
// pinned by research/codex-app-server-8d7cc24-mapping.md.
package native

import "encoding/json"

const (
	MethodThreadStart     = "thread/start"
	MethodThreadResume    = "thread/resume"
	MethodTurnStart       = "turn/start"
	MethodTurnInterrupt   = "turn/interrupt"
	MethodTurnStarted     = "turn/started"
	MethodTurnCompleted   = "turn/completed"
	MethodItemStarted     = "item/started"
	MethodItemCompleted   = "item/completed"
	MethodAgentDelta      = "item/agentMessage/delta"
	MethodCommandApproval = "item/commandExecution/requestApproval"
	MethodFileApproval    = "item/fileChange/requestApproval"
	MethodUserInput       = "item/tool/requestUserInput"
)

type Thread struct {
	ID    string `json:"id"`
	Turns []Turn `json:"turns,omitempty"`
}

type Turn struct {
	ID     string     `json:"id"`
	Status TurnStatus `json:"status"`
	Items  []Item     `json:"items,omitempty"`
	Error  *TurnError `json:"error,omitempty"`
}

type TurnStatus string

const (
	TurnCompleted   TurnStatus = "completed"
	TurnInterrupted TurnStatus = "interrupted"
	TurnFailed      TurnStatus = "failed"
	TurnInProgress  TurnStatus = "inProgress"
)

type TurnError struct {
	Message           string          `json:"message"`
	CodexErrorInfo    json.RawMessage `json:"codexErrorInfo,omitempty"`
	AdditionalDetails *string         `json:"additionalDetails,omitempty"`
	Misalignment      json.RawMessage `json:"misalignment,omitempty"`
}

type ThreadStartParams struct {
	Model                 string          `json:"model,omitempty"`
	Cwd                   string          `json:"cwd,omitempty"`
	ApprovalPolicy        string          `json:"approvalPolicy,omitempty"`
	Sandbox               string          `json:"sandbox,omitempty"`
	Config                map[string]any  `json:"config,omitempty"`
	ExperimentalRaw       json.RawMessage `json:"experimentalRawEvents,omitempty"`
	BaseInstructions      string          `json:"baseInstructions,omitempty"`
	DeveloperInstructions string          `json:"developerInstructions,omitempty"`
}

type ThreadStartResponse struct {
	Thread        Thread  `json:"thread"`
	Model         string  `json:"model,omitempty"`
	ModelProvider string  `json:"modelProvider,omitempty"`
	ServiceTier   *string `json:"serviceTier,omitempty"`
	Cwd           string  `json:"cwd,omitempty"`
}

type ThreadResumeParams struct {
	ThreadID string `json:"threadId"`
}

type ThreadResumeResponse struct {
	Thread Thread `json:"thread"`
}

type UserInput struct {
	Type         string        `json:"type"`
	Text         string        `json:"text,omitempty"`
	TextElements []TextElement `json:"textElements,omitempty"`
}

type TextElement struct {
	ByteRange   ByteRange `json:"byteRange"`
	Placeholder *string   `json:"placeholder,omitempty"`
}

type ByteRange struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

type TurnStartParams struct {
	ThreadID              string      `json:"threadId"`
	Input                 []UserInput `json:"input"`
	Model                 string      `json:"model,omitempty"`
	ApprovalPolicy        string      `json:"approvalPolicy,omitempty"`
	DeveloperInstructions string      `json:"developerInstructions,omitempty"`
}

type TurnStartResponse struct {
	Turn Turn `json:"turn"`
}

type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type TurnInterruptResponse struct{}

type TurnStartedNotification struct {
	ThreadID string `json:"threadId"`
	Turn     Turn   `json:"turn"`
}

type TurnCompletedNotification struct {
	ThreadID string `json:"threadId"`
	Turn     Turn   `json:"turn"`
}

type AgentMessageDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type ItemNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	Item     Item   `json:"item"`
}

type Item struct {
	Type       string          `json:"type"`
	ID         string          `json:"id"`
	Text       string          `json:"text,omitempty"`
	Command    string          `json:"command,omitempty"`
	Cwd        string          `json:"cwd,omitempty"`
	Path       string          `json:"path,omitempty"`
	Changes    []FileChange    `json:"changes,omitempty"`
	Server     string          `json:"server,omitempty"`
	Tool       string          `json:"tool,omitempty"`
	Status     string          `json:"status,omitempty"`
	Arguments  json.RawMessage `json:"arguments,omitempty"`
	Output     *string         `json:"aggregatedOutput,omitempty"`
	ExitCode   *int            `json:"exitCode,omitempty"`
	DurationMS *int64          `json:"durationMs,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      *ToolError      `json:"error,omitempty"`
}

type FileChange struct {
	Path string          `json:"path"`
	Kind json.RawMessage `json:"kind"`
	Diff string          `json:"diff"`
}

type ToolError struct {
	Message string `json:"message"`
}
