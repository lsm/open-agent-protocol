package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/lsm/open-agent-protocol/protocol"
)

// Session is one open adapter session on the daemon. Its methods mirror the
// daemon's HTTP operations one-to-one; Submit resolves synchronously with the
// admission, and run events arrive through Events.
type Session struct {
	client  *Client
	id      protocol.SessionID
	adapter string
}

// ID is the daemon-confirmed session identifier.
func (s *Session) ID() protocol.SessionID { return s.id }

// Adapter is the adapter name the session was opened on.
func (s *Session) Adapter() string { return s.adapter }

// Submit admits one message submission and returns the adapter's admission.
// A zero SessionID in the request is filled from the session; a mismatching
// one is refused before the wire.
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

// ResolvePermission resolves one pending permission gate. InteractionID,
// RunID, and RequestedBy must echo the action.permission.requested payload;
// an empty SessionID or RespondedBy is filled from the session.
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

// ResolveInput resolves one pending user-input gate. InteractionID, RunID,
// and RequestedBy must echo the user.input.requested payload; an empty
// SessionID or RespondedBy is filled from the session.
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

// Cancel requests cancellation of one run and returns the acknowledgement.
// The confirmed run.cancelled event on the event stream is authoritative.
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

// State reads the session's authoritative state.
func (s *Session) State(ctx context.Context) (protocol.SessionState, error) {
	var state protocol.SessionState
	response, err := s.client.exchange(ctx, http.MethodGet, s.path("/state"), nil, protocol.TypeSessionStateResponse)
	if err != nil {
		return state, err
	}
	if err := response.DecodePayload(&state); err != nil {
		return state, err
	}
	return state, nil
}

// Close closes the session. An active run refuses the close; cancel it first.
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
		if err := statusError(response, body); err != nil {
			return err
		}
		// The close contract is exactly 204 No Content: any other success —
		// a proxy page, an incompatible daemon — is not a confirmation.
		return &ServerError{
			Status:  response.StatusCode,
			Message: fmt.Sprintf("close returned status %d, want %d No Content", response.StatusCode, http.StatusNoContent),
		}
	}
	return nil
}

// scope fills or verifies the payload session id the daemon's scope check
// compares against the addressed session.
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

// path builds one session-scoped path with the session id escaped.
func (s *Session) path(suffix string) string {
	return "/sessions/" + url.PathEscape(string(s.id)) + suffix
}

// FinalText returns the final-response text of a run.completed envelope. It
// reports false for any other envelope or a non-text final response.
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
