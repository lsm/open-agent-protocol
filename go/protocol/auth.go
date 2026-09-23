package protocol

type AuthFlowID string

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
	Message      string       `json:"message,omitempty"`
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
