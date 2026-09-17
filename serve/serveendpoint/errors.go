package serveendpoint

import (
	"context"
	"errors"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

type refusal struct {
	code    string
	message string
	details map[string]any
}

func (r *refusal) Error() string { return r.message }

func (s *Server) errorEnvelope(request protocol.Envelope, err error) protocol.Envelope {
	code, message, details := "internal", err.Error(), map[string]any(nil)

	var terminal *base.RunTerminalError
	var own *refusal
	if errors.As(err, &own) {
		code, message, details = own.code, own.message, own.details
	} else if refusalCode, refusalMessage, refusalDetails, typed := serve.ControlRefusal(err); typed {
		code, message, details = refusalCode, refusalMessage, refusalDetails
	} else {
		switch {
		case errors.Is(err, serve.ErrUnknownAdapter):
			code = "unknown_adapter"
		case errors.Is(err, serve.ErrSessionExists):
			code = "session_exists"
		case errors.Is(err, serve.ErrUnknownSession):
			code = "unknown_session"
		case errors.Is(err, serve.ErrScopeMismatch):
			code = "scope_mismatch"
		case errors.Is(err, base.ErrSessionClosed):
			code = "session_closed"
		case errors.As(err, &terminal):
			code = "run_terminal"
		case errors.Is(err, base.ErrRunNotFound):
			code = "run_not_found"
		case errors.Is(err, base.ErrRunActive):
			code = "run_active"
		case errors.Is(err, base.ErrInvalidSubmission), errors.Is(err, base.ErrUnsupportedInput):
			code = "invalid_submission"
		case errors.Is(err, base.ErrInteractionNotFound), errors.Is(err, base.ErrInteractionResolved),
			errors.Is(err, base.ErrWrongResponder), errors.Is(err, base.ErrInvalidResolution):
			code = "resolution_rejected"
		case errors.Is(err, base.ErrToolCatalogUnavailable):
			code = "tool_catalog_unavailable"
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			code = "request_cancelled"
		}
	}

	payload := protocol.ErrorResponse{Error: protocol.ProtocolError{Code: code, Message: message, Details: details}}
	answer, buildErr := protocol.NewEnvelope(protocol.TypeErrorResponse, s.nextID("error"), payload)
	if buildErr != nil {

		answer, _ = protocol.NewEnvelope(protocol.TypeErrorResponse, s.nextID("error"), protocol.ErrorResponse{
			Error: protocol.ProtocolError{Code: code, Message: "the endpoint could not encode this refusal"},
		})
	}
	answer.InReplyTo = request.ID
	answer.SessionID = request.SessionID
	answer.RunID = request.RunID
	return answer
}
