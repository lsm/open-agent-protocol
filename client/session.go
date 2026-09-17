package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/lsm/open-agent-protocol/protocol"
)

type Session struct {
	client  *Client
	id      protocol.SessionID
	adapter string
}

func (s *Session) ID() protocol.SessionID { return s.id }

func (s *Session) Adapter() string { return s.adapter }

func (s *Session) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, error) {
	var admission protocol.MessageSubmitResponse
	if err := s.scope(&request.SessionID); err != nil {
		return admission, err
	}
	envelope, err := s.client.envelope(protocol.TypeSessionMessageSubmitRequest, request)
	if err != nil {
		return admission, err
	}
	envelope.SessionID = s.id
	response, err := s.client.exchange(ctx, http.MethodPost, s.path("/submit"), &envelope, protocol.TypeSessionMessageSubmitResponse)
	if err != nil {
		return admission, err
	}
	if err := response.DecodePayload(&admission); err != nil {
		return admission, err
	}
	return admission, nil
}

type ToolsOption func(*protocol.ToolsListRequest)

func AllowDegradedTools(keys ...string) ToolsOption {
	return func(request *protocol.ToolsListRequest) {
		request.AllowDegradedFeatures = append(request.AllowDegradedFeatures, keys...)
	}
}

type ToolCatalog struct {
	Revision string

	Tools protocol.ToolsListResponse
}

func (s *Session) Tools(ctx context.Context, options ...ToolsOption) (ToolCatalog, error) {
	var listing ToolCatalog
	request := protocol.ToolsListRequest{SessionID: s.id}
	for _, option := range options {
		option(&request)
	}
	path := s.path("/tools")
	if len(request.AllowDegradedFeatures) > 0 {
		query := url.Values{}
		for _, key := range request.AllowDegradedFeatures {
			query.Add("allow_degraded", key)
		}
		path += "?" + query.Encode()
	}
	response, err := s.client.exchange(ctx, http.MethodGet, path, nil, protocol.TypeActionToolsListResponse)
	if err != nil {
		return ToolCatalog{}, err
	}

	if response.CapabilityRevision == "" {
		return ToolCatalog{}, fmt.Errorf("client: %s response carries no capability revision", s.path("/tools"))
	}
	if err := response.DecodePayload(&listing.Tools); err != nil {
		return ToolCatalog{}, err
	}

	if err := s.bindResponse(s.path("/tools"), response, listing.Tools.SessionID, true); err != nil {
		return ToolCatalog{}, err
	}
	listing.Revision = response.CapabilityRevision
	return listing, nil
}

func (s *Session) ResolvePermission(ctx context.Context, request protocol.PermissionResolveRequest) error {
	if err := s.scope(&request.SessionID); err != nil {
		return err
	}
	if request.RespondedBy == "" {
		request.RespondedBy = s.client.participant
	}
	envelope, err := s.client.envelope(protocol.TypeActionPermissionResolveRequest, request)
	if err != nil {
		return err
	}
	envelope.SessionID, envelope.RunID = s.id, request.RunID
	_, err = s.client.exchange(ctx, http.MethodPost, s.path("/resolve"), &envelope, protocol.TypeActionPermissionResolveResponse)
	return err
}

func (s *Session) ResolveInput(ctx context.Context, request protocol.UserInputResolveRequest) error {
	if err := s.scope(&request.SessionID); err != nil {
		return err
	}
	if request.RespondedBy == "" {
		request.RespondedBy = s.client.participant
	}
	envelope, err := s.client.envelope(protocol.TypeUserInputResolveRequest, request)
	if err != nil {
		return err
	}
	envelope.SessionID, envelope.RunID = s.id, request.RunID
	_, err = s.client.exchange(ctx, http.MethodPost, s.path("/resolve"), &envelope, protocol.TypeUserInputResolveResponse)
	return err
}

func (s *Session) Cancel(ctx context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	var ack protocol.RunCancelResponse
	request, err := s.client.envelope(protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: s.id, RunID: runID})
	if err != nil {
		return ack, err
	}
	request.SessionID, request.RunID = s.id, runID
	response, err := s.client.exchange(ctx, http.MethodPost, s.path("/cancel"), &request, protocol.TypeRunCancelResponse)
	if err != nil {
		return ack, err
	}
	if err := response.DecodePayload(&ack); err != nil {
		return ack, err
	}
	return ack, nil
}

func (s *Session) State(ctx context.Context) (protocol.SessionState, error) {
	var state protocol.SessionState
	path := s.path("/state")
	response, err := s.client.exchange(ctx, http.MethodGet, path, nil, protocol.TypeSessionStateResponse)
	if err != nil {
		return protocol.SessionState{}, err
	}
	if err := response.DecodePayload(&state); err != nil {
		return protocol.SessionState{}, err
	}

	if err := s.bindResponse(path, response, state.SessionID, true); err != nil {
		return protocol.SessionState{}, err
	}
	return state, nil
}

func (s *Session) bindResponse(path string, response protocol.Envelope, payloadScope protocol.SessionID, required bool) error {
	if response.SessionID != s.id {
		return fmt.Errorf("client: %s response is scoped to session %q, want %q", path, response.SessionID, s.id)
	}
	if payloadScope == "" && !required {
		return nil
	}
	if payloadScope != response.SessionID {
		return fmt.Errorf("client: %s payload names session %q, envelope %q", path, payloadScope, response.SessionID)
	}
	return nil
}

type ModelsOption func(*url.Values)

func AllowDegraded(keys ...string) ModelsOption {
	return func(query *url.Values) {
		for _, key := range keys {
			query.Add("allow_degraded", key)
		}
	}
}

type Catalog struct {
	Revision string

	Models protocol.ModelsResponse
}

func (s *Session) Models(ctx context.Context, options ...ModelsOption) (Catalog, error) {
	var listing Catalog
	response, err := s.client.exchange(ctx, http.MethodGet, s.modelsPath(options...), nil, protocol.TypeModelsResponse)
	if err != nil {
		return listing, err
	}

	if response.SessionID != s.id {
		return listing, fmt.Errorf("client: %s response is scoped to session %q, want %q", s.path("/models"), response.SessionID, s.id)
	}

	if response.CapabilityRevision == "" {
		return listing, fmt.Errorf("client: %s response carries no capability revision", s.path("/models"))
	}
	if err := response.DecodePayload(&listing.Models); err != nil {
		return listing, err
	}
	if listing.Models.SessionID != response.SessionID {
		return listing, fmt.Errorf("client: %s payload names session %q, envelope %q", s.path("/models"), listing.Models.SessionID, response.SessionID)
	}
	listing.Revision = response.CapabilityRevision
	return listing, nil
}

func (s *Session) modelsPath(options ...ModelsOption) string {
	query := url.Values{}
	for _, option := range options {
		option(&query)
	}
	if len(query) == 0 {
		return s.path("/models")
	}
	return s.path("/models") + "?" + query.Encode()
}

func (s *Session) Close(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.client.url(s.path("/close")), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.http.Do(request)
	if err != nil {
		return fmt.Errorf("client: close session %s: %w", s.id, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("client: read close response: %w", err)
	}
	if response.StatusCode != http.StatusNoContent {
		if err := s.client.failureError(response, body); err != nil {
			return err
		}

		return &ServerError{
			Status:  response.StatusCode,
			Message: fmt.Sprintf("close returned status %d, want %d No Content", response.StatusCode, http.StatusNoContent),
		}
	}
	return nil
}

func (s *Session) scope(sessionID *protocol.SessionID) error {
	switch *sessionID {
	case "":
		*sessionID = s.id
	case s.id:
	default:
		return fmt.Errorf("client: request session_id %q does not match session %q", *sessionID, s.id)
	}
	return nil
}

func (s *Session) path(suffix string) string {
	return "/sessions/" + url.PathEscape(string(s.id)) + suffix
}

func FinalText(envelope protocol.Envelope) (string, bool) {
	if envelope.Type != protocol.TypeRunCompleted {
		return "", false
	}
	var payload protocol.RunCompletedPayload
	if err := envelope.DecodePayload(&payload); err != nil {
		return "", false
	}
	return payload.FinalResponse.Content.Text()
}
