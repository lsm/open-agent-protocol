package protocol

// Auth flow IDs are independent of session and run IDs. Prompt answers are
// sensitive and must not be included in trace diagnostics or error messages.
type AuthFlowID string
type AuthPromptID string

type AuthProvider struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	AuthStatus string `json:"auth_status"`
	LastError  string `json:"last_error,omitempty"`
}

type AuthProvidersRequest struct{}
type AuthProvidersResponse struct {
	Providers []AuthProvider `json:"providers"`
}

type AuthLoginStartRequest struct {
	ProviderID string `json:"provider_id"`
}
type AuthLoginStartResponse struct {
	FlowID AuthFlowID `json:"flow_id"`
}

type AuthLoginEvent struct {
	FlowID       AuthFlowID   `json:"flow_id"`
	ProviderID   string       `json:"provider_id"`
	Kind         string       `json:"kind"`
	URL          string       `json:"url,omitempty"`
	Instructions string       `json:"instructions,omitempty"`
	PromptID     AuthPromptID `json:"prompt_id,omitempty"`
	Message      string       `json:"message,omitempty"`
	AllowEmpty   *bool        `json:"allow_empty,omitempty"`
}

type AuthLoginReplyRequest struct {
	FlowID   AuthFlowID   `json:"flow_id"`
	PromptID AuthPromptID `json:"prompt_id"`
	Answer   string       `json:"answer"`
}
type AuthLoginReplyResponse struct {
	FlowID   AuthFlowID   `json:"flow_id"`
	PromptID AuthPromptID `json:"prompt_id"`
	Accepted bool         `json:"accepted"`
}

type AuthLoginCancelRequest struct {
	FlowID AuthFlowID `json:"flow_id"`
}
type AuthLoginCancelResponse struct {
	FlowID   AuthFlowID `json:"flow_id"`
	Accepted bool       `json:"accepted"`
}

type AuthLoginCompleted struct {
	FlowID     AuthFlowID     `json:"flow_id"`
	ProviderID string         `json:"provider_id"`
	Status     string         `json:"status"`
	Error      *ProtocolError `json:"error,omitempty"`
}
