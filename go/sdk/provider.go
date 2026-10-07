package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ProviderService struct {
	transport *transport
	timeout   time.Duration
}

func (s *ProviderService) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	if s.transport != nil && !s.transport.legacyWire {
		return s.oapComplete(ctx, req)
	}
	if err := validateExecutionRequest(req.ModelRef, req.Messages); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, abortError(err, "provider complete")
	}

	fallbackProvider := providerIDFromRef(req.ModelRef)
	streamID := newULID()
	sub := s.transport.subscribeStream(streamID)
	defer sub.close()

	payload := buildExecutionPayload(req.ModelRef, req.Messages, req.Tools, req.Options, false)
	if err := s.transport.send(newStreamEnvelope("complete_request", streamID, payload)); err != nil {
		return nil, err
	}

	for {
		f, err := sub.next(ctx, s.timeout, "provider complete_response")
		if err != nil {

			cancelStream(s.transport, streamID)
			sub.drain(drainIdle, drainBudget)
			return nil, withStreamID(err, streamID)
		}
		switch f.Type {
		case "ack":
			continue
		case "nack":
			return nil, nackToError(f, fallbackProvider, streamID, "")
		case "stream_error":
			return nil, errorFrameToError(f, fallbackProvider, streamID, "")
		case "result", "complete_response":
			return responseOrAuthError(parseCompletionResponse(f.payload()), fallbackProvider)
		default:
			cancelStream(s.transport, streamID)
			sub.drain(drainIdle, drainBudget)
			return nil, &StreamError{
				Kind:     KindTransportError,
				Message:  fmt.Sprintf("unexpected frame type %q while awaiting a provider result", f.Type),
				StreamID: streamID,
			}
		}
	}
}

func (s *ProviderService) Stream(ctx context.Context, req CompletionRequest) (*ProviderStream, error) {
	if s.transport != nil && !s.transport.legacyWire {
		return s.oapStream(ctx, req)
	}
	if err := validateExecutionRequest(req.ModelRef, req.Messages); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, abortError(err, "provider stream")
	}

	streamID := newULID()
	sub := s.transport.subscribeStream(streamID)

	payload := buildExecutionPayload(req.ModelRef, req.Messages, req.Tools, req.Options, true)
	if err := s.transport.send(newStreamEnvelope("stream_request", streamID, payload)); err != nil {
		sub.close()
		return nil, err
	}

	return &ProviderStream{
		ctx:              ctx,
		transport:        s.transport,
		sub:              sub,
		streamID:         streamID,
		timeout:          s.timeout,
		fallbackProvider: providerIDFromRef(req.ModelRef),
		tools:            newToolBuffer(),
	}, nil
}

type ProviderStream struct {
	oap              bool
	oapModelRef      string
	oapInferenceID   string
	oapPartKinds     map[int]string
	oapResponse      *CompletionResponse
	ctx              context.Context
	transport        *transport
	sub              *subscription
	streamID         string
	timeout          time.Duration
	fallbackProvider string
	tools            *toolBuffer

	current  ProviderEvent
	err      error
	done     bool
	finished bool
}

func (s *ProviderStream) Next() bool {
	if s.oap {
		return s.oapNext()
	}
	if s.done {
		return false
	}
	for {
		f, err := s.sub.next(s.ctx, s.timeout, "provider stream event")
		if err != nil {
			s.fail(err)
			return false
		}
		switch f.Type {
		case "ack":
			continue
		case "nack":
			s.fail(nackToError(f, s.fallbackProvider, s.streamID, ""))
			return false
		}

		event := normalizeProviderFrame(f, s.tools)
		if event == nil {
			continue
		}
		if errEvent, ok := event.(*ErrorEvent); ok {

			if errEvent.Code == CodeAuthRequired {
				s.fail(newAuthRequiredError(firstNonEmpty(errEvent.ProviderID, s.fallbackProvider), errEvent.Message))
				return false
			}
			s.current = event
			s.done = true
			s.finished = true
			return true
		}
		if _, ok := event.(*MessageEnd); ok {
			s.done = true
			s.finished = true
		}
		s.current = event
		return true
	}
}

func (s *ProviderStream) Event() ProviderEvent { return s.current }

func (s *ProviderStream) Err() error { return s.err }

func (s *ProviderStream) Close() error {
	if s.oap {
		if s.sub == nil {
			return s.err
		}
		if !s.finished && s.oapInferenceID != "" {
			cancel := oapFrame(oapProvider, "inference.cancel.request", map[string]any{"reason": "caller_closed"})
			cancel.Unknown = map[string]json.RawMessage{"inference_id": mustMarshal(s.oapInferenceID)}
			s.transport.sendEnvelopeBestEffort(cancel)
		}
		s.sub.close()
		s.sub = nil
		s.done = true
		return s.err
	}
	if s.sub == nil {
		return s.err
	}
	if !s.finished {
		cancelStream(s.transport, s.streamID)
		s.sub.drain(drainIdle, drainBudget)
	}
	s.sub.close()
	s.sub = nil
	s.done = true
	return s.err
}

func (s *ProviderStream) fail(err error) {
	s.err = withStreamID(err, s.streamID)
	s.done = true
	s.current = nil
}

func nackToError(f *frame, fallbackProvider, streamID, sessionID string) error {
	payload := f.payload()
	code := payload.str("error_code", "code")
	providerID := payload.str("provider_id")
	if providerID == "" && isAuthCode(code) {
		providerID = fallbackProvider
	}
	message := payload.strOrDefault("request rejected", "reason", "message")
	if code == CodeAuthRequired {
		return newAuthRequiredError(providerID, message)
	}
	return &StreamError{
		Kind:       KindProviderError,
		Code:       code,
		ProviderID: providerID,
		Message:    message,
		StreamID:   streamID,
		SessionID:  sessionID,
	}
}

func errorFrameToError(f *frame, fallbackProvider, streamID, sessionID string) error {
	payload := f.payload()
	code := payload.str("code", "error_code")
	providerID := payload.str("provider_id")
	if providerID == "" && isAuthCode(code) {
		providerID = fallbackProvider
	}
	message := payload.strOrDefault("stream error", "message", "reason")
	if code == CodeAuthRequired {
		return newAuthRequiredError(providerID, message)
	}
	return &StreamError{
		Kind:       KindProviderError,
		Code:       code,
		ProviderID: providerID,
		Message:    message,
		StreamID:   streamID,
		SessionID:  sessionID,
	}
}

func isAuthCode(code string) bool {
	return code == CodeAuthRequired || code == CodeAuthExpired || code == CodeAuthRefreshFailed
}

func responseOrAuthError(response *CompletionResponse, fallbackProvider string) (*CompletionResponse, error) {
	if response.StopReason != "error" || !isAuthFailureMessage(response.ErrorMessage, response.API) {
		return response, nil
	}
	providerID := firstNonEmpty(response.ProviderID, fallbackProvider)
	message := response.ErrorMessage
	if message == "" {
		message = CodeAuthRequired
	}
	return nil, newAuthRequiredError(providerID, message)
}

func isAuthFailureMessage(message, api string) bool {
	if message == "" {
		return false
	}
	normalized := strings.ToLower(message)
	switch normalized {
	case CodeAuthRequired, CodeAuthExpired, CodeAuthRefreshFailed:
		return true
	}
	for _, needle := range []string{"authentication required", "401", "403", "unauthorized", "forbidden"} {
		if strings.Contains(normalized, needle) {
			return true
		}
	}
	if api == "anthropic-messages" {
		for _, needle := range []string{"authentication_error", "permission_error", "invalid api key"} {
			if strings.Contains(normalized, needle) {
				return true
			}
		}
	}
	return false
}

func withStreamID(err error, streamID string) error {
	var streamErr *StreamError
	if asStreamError(err, &streamErr) && streamErr.StreamID == "" {
		streamErr.StreamID = streamID
	}
	return err
}

func withSessionID(err error, sessionID string) error {
	var streamErr *StreamError
	if asStreamError(err, &streamErr) && streamErr.SessionID == "" {
		streamErr.SessionID = sessionID
	}
	return err
}
