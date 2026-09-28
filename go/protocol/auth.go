package protocol

type AuthFlowID string

type AuthProvider struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	AuthKinds  []CredentialKind `json:"auth_kinds"`
	AuthStatus string           `json:"auth_status"`
	LastError  string           `json:"last_error,omitempty"`
}

// CredentialKind is how a provider accepts a credential. Named for the
// credential rather than the auth because AuthKind in this repository's SDK
// surface already means a kind of auth error, and a reader holding both would
// have two unrelated meanings of the same word.
type CredentialKind string

const (
	CredentialKindAPIKey CredentialKind = "api_key"
	CredentialKindOAuth  CredentialKind = "oauth"
	CredentialKindNone   CredentialKind = "none"
)

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
	FlowID       AuthFlowID `json:"flow_id"`
	ProviderID   string     `json:"provider_id"`
	Kind         string     `json:"kind"`
	URL          string     `json:"url,omitempty"`
	Instructions string     `json:"instructions,omitempty"`
	Message      string     `json:"message,omitempty"`
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
