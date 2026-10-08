package sdk

import (
	"context"
	"fmt"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type ProviderAuthInfo struct {
	ID string

	Name string

	AuthKinds []protocol.CredentialKind

	Status AuthStatus

	LastError string

	OverrideHost string
}

type AuthEventType string

const (
	AuthEventURL AuthEventType = "auth_url"

	AuthEventPrompt AuthEventType = "prompt"

	AuthEventProgress AuthEventType = "progress"

	AuthEventSuccess AuthEventType = "success"

	AuthEventError AuthEventType = "error"
)

type AuthEvent struct {
	Type AuthEventType

	FlowID string

	ProviderID string

	URL          string
	Instructions string

	PromptID string

	Message string

	AllowEmpty bool

	Code string
}

type LoginHandlers struct {
	OnEvent func(AuthEvent)
}

type AuthService struct {
	transport *transport
	timeout   time.Duration
}

func (s *AuthService) ListProviders(ctx context.Context) ([]ProviderAuthInfo, error) {
	request := oapFrame(oapAgent, "auth.providers.request", map[string]any{})
	sub := s.transport.subscribeStream(string(request.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, request)
	if err != nil {
		if failure, ok := err.(*StreamError); ok {
			return nil, &AuthError{Kind: AuthKindProviderError, Code: failure.Code, Message: failure.Message}
		}
		return nil, authErrorFrom(err, "", "")
	}
	if response.Type != "auth.providers.response" {
		return nil, &AuthError{Kind: AuthKindTransportError, Message: "expected auth.providers.response"}
	}
	result := make([]ProviderAuthInfo, 0, len(envelopePayload(response).arr("providers")))
	for _, raw := range envelopePayload(response).arr("providers") {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, &AuthError{Kind: AuthKindTransportError, Message: "auth provider entry is not an object"}
		}
		p := jsonObject(entry)
		raw := make([]protocol.CredentialKind, 0, len(p.arr("auth_kinds")))
		for _, kind := range p.arr("auth_kinds") {
			if name, ok := kind.(string); ok {
				raw = append(raw, protocol.CredentialKind(name))
			}
		}
		result = append(result, ProviderAuthInfo{ID: p.str("id"), Name: p.str("name"), AuthKinds: knownCredentialKinds(raw), Status: AuthStatus(p.str("auth_status")), LastError: p.str("last_error"), OverrideHost: p.str("override_host")})
	}
	return result, nil
}

func (s *AuthService) Login(ctx context.Context, providerID string, handlers LoginHandlers) error {
	if providerID == "" {
		return &AuthError{Kind: AuthKindProviderError, Code: CodeInvalidRequest, Message: "login requires a provider id"}
	}
	start := oapFrame(oapAgent, "auth.login.start.request", map[string]any{"provider_id": providerID})
	sub := s.transport.subscribeStream(string(start.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, start)
	if err != nil {
		if failure, ok := err.(*StreamError); ok {
			return &AuthError{Kind: AuthKindProviderError, Code: failure.Code, Message: failure.Message, ProviderID: providerID}
		}
		return authErrorFrom(err, providerID, "")
	}
	if response.Type != "auth.login.start.response" {
		return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, Message: "expected auth.login.start.response"}
	}
	flowID := envelopePayload(response).str("flow_id")
	if flowID == "" {
		return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, Message: "auth login start omitted flow_id"}
	}
	settled := false
	defer func() {
		if !settled {
			s.oapCancelFlow(flowID)
		}
	}()
	nextSequence := int64(1)
	for {
		in, err := sub.next(ctx, s.timeout, "OAP auth login")
		if err != nil {
			if ctx.Err() != nil {
				return &AuthError{Kind: AuthKindCancelled, ProviderID: providerID, FlowID: flowID, Message: "auth login aborted", err: ctx.Err()}
			}
			return authErrorFrom(err, providerID, flowID)
		}
		if in.broken != nil {
			return authErrorFrom(in.broken, providerID, flowID)
		}
		if in.kind() != "auth.login.event" && in.kind() != "auth.login.completed" {
			return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, FlowID: flowID, Message: fmt.Sprintf("unexpected OAP auth flow event %q", in.kind())}
		}
		if in.sequence() != nextSequence {
			return &AuthError{Kind: AuthKindTransportError, Code: "protocol_violation", ProviderID: providerID, FlowID: flowID, Message: "auth flow sequence gap"}
		}
		nextSequence++
		p := in.body()
		if p.str("flow_id") != flowID || p.str("provider_id") != providerID {
			return &AuthError{Kind: AuthKindTransportError, Code: "protocol_violation", ProviderID: providerID, FlowID: flowID, Message: "auth flow identity changed"}
		}
		if in.kind() == "auth.login.completed" {
			settled = true
			switch p.str("status") {
			case "success":
				if handlers.OnEvent != nil {
					handlers.OnEvent(AuthEvent{Type: AuthEventSuccess, FlowID: flowID, ProviderID: providerID})
				}
				return nil
			case "cancelled":
				failure := p.obj("error")
				message := failure.strOrDefault("auth login cancelled", "message")
				return &AuthError{Kind: AuthKindCancelled, Code: failure.str("code"), Message: message, ProviderID: providerID, FlowID: flowID}
			case "failed":
				failure := p.obj("error")
				return &AuthError{Kind: AuthKindProviderError, Code: failure.str("code"), Message: failure.strOrDefault("auth login failed", "message"), ProviderID: providerID, FlowID: flowID}
			default:
				return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, FlowID: flowID, Message: "unknown auth login terminal status"}
			}
		}
		event := AuthEvent{FlowID: flowID, ProviderID: providerID}
		switch p.str("kind") {
		case "url":
			event.Type = AuthEventURL
			event.URL = p.str("url")
			event.Instructions = p.str("instructions")
		case "progress":
			event.Type = AuthEventProgress
			event.Message = p.str("message")
		case "prompt":
			return &AuthError{Kind: AuthKindProviderError, Code: "auth_input_unavailable", ProviderID: providerID, FlowID: flowID, Message: "manual login input cannot be sent over OAP"}
		default:
			return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, FlowID: flowID, Message: "unknown OAP auth event kind"}
		}
		if handlers.OnEvent != nil {
			handlers.OnEvent(event)
		}
	}
}

func knownCredentialKinds(kinds []protocol.CredentialKind) []protocol.CredentialKind {
	kept := make([]protocol.CredentialKind, 0, len(kinds))
	for _, kind := range kinds {
		switch kind {
		case protocol.CredentialKindAPIKey, protocol.CredentialKindOAuth, protocol.CredentialKindNone:
			kept = append(kept, kind)
		}
	}
	return kept
}

func authErrorFrom(err error, providerID, flowID string) *AuthError {
	kind := AuthKindTransportError
	if isAbort(err) {
		kind = AuthKindCancelled
	}
	message := err.Error()
	var streamErr *StreamError
	if asStreamError(err, &streamErr) {
		message = streamErr.Message
	}
	return &AuthError{Kind: kind, ProviderID: providerID, FlowID: flowID, Message: message, err: contextCause(err)}
}

func contextCause(err error) error {
	var streamErr *StreamError
	if asStreamError(err, &streamErr) && streamErr.err != nil {
		return streamErr.err
	}
	return err
}
