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

type CommandApprovalParams struct {
	ThreadID              string          `json:"threadId"`
	TurnID                string          `json:"turnId"`
	ItemID                string          `json:"itemId"`
	Kind                  string          `json:"kind"`
	StartedAtMS           int64           `json:"startedAtMs"`
	ApprovalID            *string         `json:"approvalId,omitempty"`
	EnvironmentID         *string         `json:"environmentId"`
	Reason                *string         `json:"reason,omitempty"`
	NetworkContext        json.RawMessage `json:"networkApprovalContext,omitempty"`
	Command               *string         `json:"command,omitempty"`
	Cwd                   *string         `json:"cwd,omitempty"`
	CommandActions        json.RawMessage `json:"commandActions,omitempty"`
	AdditionalPermissions json.RawMessage `json:"additionalPermissions,omitempty"`
	AvailableDecisions    json.RawMessage `json:"availableDecisions,omitempty"`
	ExecPolicyAmendment   json.RawMessage `json:"proposedExecpolicyAmendment,omitempty"`
	NetworkAmendments     json.RawMessage `json:"proposedNetworkPolicyAmendments,omitempty"`
}

type FileApprovalParams struct {
	ThreadID    string  `json:"threadId"`
	TurnID      string  `json:"turnId"`
	ItemID      string  `json:"itemId"`
	StartedAtMS int64   `json:"startedAtMs"`
	Reason      *string `json:"reason,omitempty"`
	GrantRoot   *string `json:"grantRoot,omitempty"`
}

type ApprovalDecision string

const (
	ApprovalAccept           ApprovalDecision = "accept"
	ApprovalAcceptForSession ApprovalDecision = "acceptForSession"
	ApprovalDecline          ApprovalDecision = "decline"
	ApprovalCancel           ApprovalDecision = "cancel"
)

type ApprovalResponse struct {
	Decision ApprovalDecision `json:"decision"`
}

type UserInputRequestParams struct {
	ThreadID         string              `json:"threadId"`
	TurnID           string              `json:"turnId"`
	ItemID           string              `json:"itemId"`
	Questions        []UserInputQuestion `json:"questions"`
	IsBlocking       bool                `json:"isBlocking"`
	AutoResolutionMS *int64              `json:"autoResolutionMs"`
}

type UserInputQuestion struct {
	ID       string             `json:"id"`
	Header   string             `json:"header"`
	Question string             `json:"question"`
	IsOther  bool               `json:"isOther"`
	IsSecret bool               `json:"isSecret"`
	Options  *[]UserInputOption `json:"options"`
}

type UserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type UserInputResponse struct {
	Answers map[string]UserInputAnswer `json:"answers"`
}

type UserInputAnswer struct {
	Answers []string `json:"answers"`
}
