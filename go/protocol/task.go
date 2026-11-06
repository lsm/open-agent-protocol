package protocol

import "encoding/json"

type TaskSchedule struct {
	Expression   string `json:"expression,omitempty"`
	TimeZone     string `json:"time_zone,omitempty"`
	NextFireAtMS *int64 `json:"next_fire_at_ms,omitempty"`
	Recurring    *bool  `json:"recurring,omitempty"`
	Durable      *bool  `json:"durable,omitempty"`
	ExpiresAtMS  *int64 `json:"expires_at_ms,omitempty"`
}

type SessionTask struct {
	TaskID      TaskID                     `json:"task_id"`
	Kind        string                     `json:"kind"`
	Label       string                     `json:"label,omitempty"`
	Status      string                     `json:"status"`
	RunID       RunID                      `json:"run_id,omitempty"`
	ToolCallID  ToolCallID                 `json:"tool_call_id,omitempty"`
	Ambient     bool                       `json:"ambient,omitempty"`
	Summary     string                     `json:"summary,omitempty"`
	Cause       string                     `json:"cause,omitempty"`
	Extensions  map[string]json.RawMessage `json:"extensions,omitempty"`
	Cancellable bool                       `json:"cancellable"`
	Schedule    *TaskSchedule              `json:"schedule,omitempty"`
}

type SessionTaskStarted struct {
	Task SessionTask `json:"task"`
}
type SessionTaskUpdated struct {
	TaskID       TaskID  `json:"task_id"`
	Status       string  `json:"status,omitempty"`
	Label        *string `json:"label,omitempty"`
	NextFireAtMS *int64  `json:"next_fire_at_ms,omitempty"`
}
type SessionTaskEnded struct {
	TaskID  TaskID `json:"task_id"`
	Status  string `json:"status"`
	Summary string `json:"summary,omitempty"`
	Cause   string `json:"cause"`
}
type SessionTaskCancelRequest struct {
	SessionID SessionID `json:"session_id"`
	TaskID    TaskID    `json:"task_id"`
}
type SessionTaskCancelResponse struct {
	Outcome string `json:"outcome"`
}
type SessionTaskOutputRequest struct {
	SessionID SessionID `json:"session_id"`
	TaskID    TaskID    `json:"task_id"`
	From      *uint64   `json:"from,omitempty"`
}
type SessionTaskOutputResponse struct {
	Output    string `json:"output"`
	Next      uint64 `json:"next"`
	Truncated bool   `json:"truncated"`
	Ended     bool   `json:"ended"`
}
