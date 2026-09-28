package sdk

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrClosed = errors.New("oap sdk: client is closed")

	ErrBinaryNotFound = errors.New("oap sdk: oapx binary not found")

	ErrChecksumRequired = errors.New("oap sdk: sha256 checksum is required when resolving the oapx binary from a URL")

	ErrChecksumMismatch = errors.New("oap sdk: binary checksum mismatch")

	ErrProtocolVersion = errors.New("oap sdk: protocol version mismatch")
)

type ErrorKind string

const (
	KindProviderError ErrorKind = "provider_error"

	KindTransportError ErrorKind = "transport_error"

	KindAborted ErrorKind = "aborted"

	KindUnknown ErrorKind = "unknown"
)

type StreamError struct {
	Kind ErrorKind

	Code string

	ProviderID string

	Message string

	StreamID  string
	SessionID string

	err error
}

func (e *StreamError) Error() string {
	var b strings.Builder
	b.WriteString("oap sdk: ")
	b.WriteString(string(e.Kind))
	if e.Code != "" {
		b.WriteString("/")
		b.WriteString(e.Code)
	}
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.ProviderID != "" {
		fmt.Fprintf(&b, " (provider_id=%s)", e.ProviderID)
	}
	if ids := formatIDs(e.StreamID, e.SessionID); ids != "" {
		b.WriteString(" ")
		b.WriteString(ids)
	}
	return b.String()
}

func (e *StreamError) Unwrap() error { return e.err }

type AuthRequiredError struct {
	*StreamError
}

func (e *AuthRequiredError) Unwrap() error { return e.StreamError }

func newAuthRequiredError(providerID, message string) *AuthRequiredError {
	if message == "" {
		message = "authentication required for provider " + providerID
	}
	return &AuthRequiredError{StreamError: &StreamError{
		Kind:       KindProviderError,
		Code:       CodeAuthRequired,
		ProviderID: providerID,
		Message:    message,
	}}
}

type ProtocolError struct {
	Code string

	Message string

	StreamID string

	err error
}

func (e *ProtocolError) Error() string {
	var b strings.Builder
	b.WriteString("oap sdk: protocol error")
	if e.Code != "" {
		b.WriteString(" (")
		b.WriteString(e.Code)
		b.WriteString(")")
	}
	b.WriteString(": ")
	b.WriteString(e.Message)
	if ids := formatIDs(e.StreamID, ""); ids != "" {
		b.WriteString(" ")
		b.WriteString(ids)
	}
	return b.String()
}

func (e *ProtocolError) Unwrap() error { return e.err }

type AuthErrorKind string

const (
	AuthKindProviderError AuthErrorKind = "provider_error"

	AuthKindCancelled AuthErrorKind = "cancelled"

	AuthKindTransportError AuthErrorKind = "transport_error"

	AuthKindUnknown AuthErrorKind = "unknown"
)

type AuthError struct {
	Kind AuthErrorKind

	Code string

	Message string

	ProviderID string

	FlowID string

	err error
}

func (e *AuthError) Error() string {
	var b strings.Builder
	b.WriteString("oap sdk: auth ")
	b.WriteString(string(e.Kind))
	if e.Code != "" {
		b.WriteString("/")
		b.WriteString(e.Code)
	}
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.ProviderID != "" {
		fmt.Fprintf(&b, " (provider_id=%s)", e.ProviderID)
	}
	if e.FlowID != "" {
		fmt.Fprintf(&b, " (flow_id=%s)", e.FlowID)
	}
	return b.String()
}

func (e *AuthError) Unwrap() error { return e.err }

const (
	CodeAuthRequired = "auth_required"

	CodeAuthExpired = "auth_expired"

	CodeAuthRefreshFailed = "auth_refresh_failed"

	CodeAgentBusy = "agent_busy"

	CodeInvalidRequest = "invalid_request"

	CodeNotImplemented = "not_implemented"

	CodeMalformedResponse = "malformed_response"
)

func formatIDs(streamID, sessionID string) string {
	var parts []string
	if streamID != "" {
		parts = append(parts, "stream_id="+streamID)
	}
	if sessionID != "" {
		parts = append(parts, "session_id="+sessionID)
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func transportErrorf(cause error, format string, args ...any) *StreamError {
	return &StreamError{
		Kind:    KindTransportError,
		Message: fmt.Sprintf(format, args...),
		err:     cause,
	}
}

func abortError(cause error, operation string) *StreamError {
	return &StreamError{
		Kind:    KindAborted,
		Message: operation + " aborted: " + cause.Error(),
		err:     cause,
	}
}
