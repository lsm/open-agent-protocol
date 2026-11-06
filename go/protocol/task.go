package protocol

import "encoding/json"

type SessionTaskKind string
type SessionTaskStatus string
type SessionTaskUpdateStatus string
type SessionTaskEndStatus string
type SessionTaskCause string
type SessionTaskCancelOutcome string

const (
	SessionTaskCommand  SessionTaskKind = "command"
	SessionTaskAgent    SessionTaskKind = "agent"
	SessionTaskSchedule SessionTaskKind = "schedule"
	SessionTaskOther    SessionTaskKind = "other"

	SessionTaskPending   SessionTaskStatus = "pending"
	SessionTaskRunning   SessionTaskStatus = "running"
	SessionTaskPaused    SessionTaskStatus = "paused"
	SessionTaskCompleted SessionTaskStatus = "completed"
	SessionTaskFailed    SessionTaskStatus = "failed"
	SessionTaskCancelled SessionTaskStatus = "cancelled"

	SessionTaskUpdatePending SessionTaskUpdateStatus = "pending"
	SessionTaskUpdateRunning SessionTaskUpdateStatus = "running"
	SessionTaskUpdatePaused  SessionTaskUpdateStatus = "paused"

	SessionTaskEndCompleted SessionTaskEndStatus = "completed"
	SessionTaskEndFailed    SessionTaskEndStatus = "failed"
	SessionTaskEndCancelled SessionTaskEndStatus = "cancelled"

	SessionTaskFinished         SessionTaskCause = "finished"
	SessionTaskCancelledByHost  SessionTaskCause = "cancelled_by_host"
	SessionTaskCancelledByModel SessionTaskCause = "cancelled_by_model"
	SessionTaskExpired          SessionTaskCause = "expired"
	SessionTaskSessionClosed    SessionTaskCause = "session_closed"

	SessionTaskCancelRequested      SessionTaskCancelOutcome = "requested"
	SessionTaskCancelAlreadyEnded   SessionTaskCancelOutcome = "already_ended"
	SessionTaskCancelNotCancellable SessionTaskCancelOutcome = "not_cancellable"
)

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
	Kind        SessionTaskKind            `json:"kind"`
	Label       string                     `json:"label,omitempty"`
	Status      SessionTaskStatus          `json:"status"`
	RunID       RunID                      `json:"run_id,omitempty"`
	ToolCallID  ToolCallID                 `json:"tool_call_id,omitempty"`
	Ambient     bool                       `json:"ambient,omitempty"`
	Summary     string                     `json:"summary,omitempty"`
	Cause       SessionTaskCause           `json:"cause,omitempty"`
	Extensions  map[string]json.RawMessage `json:"extensions,omitempty"`
	Cancellable bool                       `json:"cancellable"`
	Schedule    *TaskSchedule              `json:"schedule,omitempty"`
}

type SessionTaskStarted struct {
	Task SessionTask `json:"task"`
}
type SessionTaskUpdated struct {
	TaskID       TaskID                  `json:"task_id"`
	Status       SessionTaskUpdateStatus `json:"status,omitempty"`
	Label        *string                 `json:"label,omitempty"`
	NextFireAtMS *int64                  `json:"next_fire_at_ms,omitempty"`
}
type SessionTaskEnded struct {
	TaskID  TaskID               `json:"task_id"`
	Status  SessionTaskEndStatus `json:"status"`
	Summary string               `json:"summary,omitempty"`
	Cause   SessionTaskCause     `json:"cause"`
}
type SessionTaskCancelRequest struct {
	SessionID SessionID `json:"session_id"`
	TaskID    TaskID    `json:"task_id"`
}
type SessionTaskCancelResponse struct {
	Outcome SessionTaskCancelOutcome `json:"outcome"`
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
