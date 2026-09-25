package serveendpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func (s *Server) handle(ctx context.Context, streams context.Context, line []byte) error {

	var shape struct {
		Protocol string `json:"protocol"`
		Control  string `json:"control"`
	}
	if err := json.Unmarshal(line, &shape); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedLine, err)
	}
	if shape.Protocol == "" && shape.Control != "" {
		var frame controlFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			return fmt.Errorf("%w: %v", ErrMalformedLine, err)
		}
		return s.handleControl(streams, frame)
	}
	if shape.Protocol == "" {

		return fmt.Errorf("%w: the line declares neither protocol nor control", ErrMalformedLine)
	}
	var envelope protocol.Envelope
	decodeErr := json.Unmarshal(line, &envelope)
	if envelope.ID == "" {

		return fmt.Errorf("%w: an envelope needs an id to be answerable", ErrMalformedLine)
	}
	if decodeErr != nil {

		return s.write(ctx, s.errorEnvelope(envelope, &refusal{
			code: "invalid_request", message: decodeErr.Error(),
		}))
	}
	if missing := missingBaseMembers(line); len(missing) > 0 {

		return s.write(ctx, s.errorEnvelope(envelope, &refusal{
			code:    "invalid_request",
			message: "the envelope omits required member(s): " + strings.Join(missing, ", "),
		}))
	}
	answer, after, err := s.serve(ctx, streams, envelope)
	if err != nil {
		answer, after = s.errorEnvelope(envelope, err), nil
	}
	if writeErr := s.write(ctx, answer); writeErr != nil {
		return writeErr
	}

	if after != nil {
		after()
	}
	return nil
}

var baseMembers = []string{"protocol", "version", "profile", "type", "id", "payload"}

func missingBaseMembers(line []byte) []string {
	var present map[string]json.RawMessage
	if json.Unmarshal(line, &present) != nil {
		return nil
	}
	var missing []string
	for _, member := range baseMembers {
		raw, ok := present[member]
		if !ok || string(bytes.TrimSpace(raw)) == "null" {
			missing = append(missing, member)
		}
	}
	return missing
}

func (s *Server) serve(ctx context.Context, streams context.Context, e protocol.Envelope) (protocol.Envelope, func(), error) {
	plain := func(answer protocol.Envelope, err error) (protocol.Envelope, func(), error) {
		return answer, nil, err
	}
	if err := s.refuseStaleRevision(ctx, e); err != nil {
		return protocol.Envelope{}, nil, err
	}
	switch e.Type {
	case protocol.TypeProtocolInitializeRequest:
		return plain(s.initialize(ctx, e))
	case protocol.TypeCapabilitiesRequest:
		return plain(s.capabilities(ctx, e))
	case protocol.TypeSessionOpenRequest:
		return plain(s.open(ctx, e))
	case protocol.TypeSessionStateRequest:
		return plain(s.state(ctx, e))
	case protocol.TypeSessionModelSwitchRequest:
		return s.switchModel(ctx, streams, e)
	case protocol.TypeSessionMessageSubmitRequest:
		return s.submit(ctx, streams, e)
	case protocol.TypeRunCancelRequest:
		return plain(s.cancel(ctx, e))
	case protocol.TypeActionPermissionResolveRequest, protocol.TypeUserInputResolveRequest, protocol.TypeActionCallResolveRequest:
		return plain(s.resolve(ctx, e))
	case protocol.TypeModelsRequest:
		return plain(s.models(ctx, e))
	case protocol.TypeActionToolsListRequest:
		return plain(s.tools(ctx, e))
	}
	return protocol.Envelope{}, nil, &refusal{code: "unsupported_request", message: fmt.Sprintf("this endpoint serves no %s", e.Type)}
}

func (s *Server) initialize(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	descriptor, err := s.hub.Probe(ctx, s.adapter)
	if err != nil {
		return protocol.Envelope{}, err
	}

	var request protocol.InitializeRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
	}
	if request.Participant != nil && request.Participant.ID != "" {
		s.participantMu.Lock()
		s.participant = request.Participant.ID
		s.participantMu.Unlock()
	}
	answer, err := protocol.NewEnvelope(protocol.TypeProtocolInitializeResponse, s.nextID("response"), protocol.InitializeResponse{
		ProtocolVersion: protocol.Version,
		Profile:         protocol.Profile,
		Endpoint:        descriptor.Capabilities.Endpoint,
	})
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.CapabilityRevision = descriptor.CapabilityRevision
	return answer, nil
}

func (s *Server) refuseStaleRevision(ctx context.Context, e protocol.Envelope) error {
	if e.CapabilityRevision == "" {
		return nil
	}
	switch e.Type {
	case protocol.TypeProtocolInitializeRequest, protocol.TypeCapabilitiesRequest:
		return nil
	}
	descriptor, err := s.hub.Probe(ctx, s.adapter)
	if err != nil {
		return err
	}
	if descriptor.CapabilityRevision == "" || e.CapabilityRevision == descriptor.CapabilityRevision {
		return nil
	}
	return &refusal{
		code:    "stale_capabilities",
		message: fmt.Sprintf("capability revision %q is not the current %q", e.CapabilityRevision, descriptor.CapabilityRevision),
		details: map[string]any{"expected_revision": e.CapabilityRevision, "current_revision": descriptor.CapabilityRevision},
	}
}

func (s *Server) controlParticipant() protocol.ParticipantID {
	s.participantMu.Lock()
	defer s.participantMu.Unlock()
	if s.participant == "" {
		return serve.DefaultParticipant
	}
	return s.participant
}

func (s *Server) capabilities(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	descriptor, err := s.hub.Probe(ctx, s.adapter)
	if err != nil {
		return protocol.Envelope{}, err
	}
	capabilities := descriptor.Capabilities
	if len(capabilities.Bindings) == 0 {
		capabilities.Bindings = []protocol.Binding{{Kind: "stdio", Serialization: "jsonl"}}
	}
	answer, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, s.nextID("response"), capabilities)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.CapabilityRevision = descriptor.CapabilityRevision
	return answer, nil
}

func (s *Server) open(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	var request protocol.SessionOpenRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
	}
	open := base.OpenRequest{
		SessionID:             request.SessionID,
		Participant:           protocol.Participant{ID: s.controlParticipant()},
		AllowDegradedFeatures: request.AllowDegradedFeatures,
		Tools:                 request.Tools,
	}
	entry, state, err := s.hub.Open(ctx, s.adapter, open)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, s.nextID("response"), state)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = entry.ID()
	answer.CapabilityRevision = e.CapabilityRevision
	return answer, nil
}

func (s *Server) state(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	entry, err := s.session(e)
	if err != nil {
		return protocol.Envelope{}, err
	}
	state, err := entry.State(ctx)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeSessionStateResponse, s.nextID("response"), state)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = entry.ID()
	answer.CapabilityRevision = e.CapabilityRevision
	return answer, nil
}

func (s *Server) switchModel(ctx context.Context, streams context.Context, e protocol.Envelope) (protocol.Envelope, func(), error) {
	entry, err := s.session(e)
	if err != nil {
		return protocol.Envelope{}, nil, err
	}
	var request protocol.SessionModelSwitchRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, nil, &refusal{code: "invalid_payload", message: err.Error()}
	}
	response, state, err := entry.SwitchModel(ctx, request)
	if err != nil {
		return protocol.Envelope{}, nil, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeSessionModelSwitchResponse, s.nextID("response"), response)
	if err != nil {
		return protocol.Envelope{}, nil, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = entry.ID()
	answer.CapabilityRevision = e.CapabilityRevision
	if response.PreviousModelID == response.ModelID {
		return answer, nil, nil
	}
	updated, err := protocol.NewEnvelope(protocol.TypeSessionStateUpdated, s.nextID("event"), state)
	if err != nil {
		return protocol.Envelope{}, nil, err
	}
	updated.SessionID = entry.ID()
	updated.CapabilityRevision = e.CapabilityRevision
	sequence := s.nextStateSequence(entry.ID())
	updated.Sequence = &sequence
	return answer, func() {
		if err := s.write(streams, updated); err != nil {
			s.logger.Printf("serveendpoint: publishing model switch state: %v", err)
		}
	}, nil
}

func (s *Server) submit(ctx context.Context, streams context.Context, e protocol.Envelope) (protocol.Envelope, func(), error) {
	entry, err := s.session(e)
	if err != nil {
		return protocol.Envelope{}, nil, err
	}
	var request protocol.MessageSubmitRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, nil, &refusal{code: "invalid_payload", message: err.Error()}
	}
	subscription, err := s.hub.Subscribe(streams, entry.ID())
	if err != nil {
		return protocol.Envelope{}, nil, err
	}
	admission, err := entry.Submit(ctx, request)
	if err != nil {
		subscription.Close()
		return protocol.Envelope{}, nil, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, s.nextID("response"), admission)
	if err != nil {
		subscription.Close()
		return protocol.Envelope{}, nil, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = admission.SessionID
	answer.RunID = admission.RunID
	answer.CapabilityRevision = e.CapabilityRevision

	start := func() {
		s.pumps.Add(1)
		go func() {
			defer s.pumps.Done()
			s.pump(streams, subscription, admission.RunID)
		}()
	}
	return answer, start, nil
}

func (s *Server) pump(ctx context.Context, subscription *serve.Subscription, run protocol.RunID) {
	defer subscription.Close()
	var delivered uint64
	for {
		envelope, err := subscription.Next()
		if err != nil {
			s.reportLostStream(ctx, run, delivered, err)
			return
		}
		if run != "" && envelope.RunID != run {
			continue
		}
		if writeErr := s.write(ctx, envelope); writeErr != nil {
			s.reportLostStream(ctx, run, delivered, writeErr)
			return
		}
		if envelope.Sequence != nil {
			delivered = *envelope.Sequence
		}
	}
}

func (s *Server) reportLostStream(ctx context.Context, run protocol.RunID, delivered uint64, err error) {
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return
	}
	code := "stream_failed"
	var overflow *serve.OverflowError
	switch {
	case errors.As(err, &overflow):
		code = "overflow"
	case errors.Is(err, ErrFrameTooLarge):
		code = "frame_limit"
	}

	after := delivered
	s.logger.Printf("serveendpoint: run stream ended early (%s): %v", code, err)
	if writeErr := s.writeControl(ctx, controlFrame{
		Control: controlStreamLost, RunID: run, After: &after, Code: code,
		Message: "this run's events stopped reaching the host; replay from after to continue",
	}); writeErr != nil {
		s.logger.Printf("serveendpoint: reporting the lost stream: %v", writeErr)
	}
}

func (s *Server) cancel(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	entry, err := s.session(e)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var request protocol.RunCancelRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
	}

	if request.SessionID != entry.ID() {
		return protocol.Envelope{}, &refusal{
			code:    "scope_mismatch",
			message: fmt.Sprintf("payload session_id %q does not match the addressed session %q", request.SessionID, entry.ID()),
		}
	}
	ack, err := entry.Cancel(ctx, request.RunID)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeRunCancelResponse, s.nextID("response"), ack)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = entry.ID()
	answer.RunID = request.RunID
	answer.CapabilityRevision = e.CapabilityRevision
	return answer, nil
}

func (s *Server) resolve(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	entry, err := s.session(e)
	if err != nil {
		return protocol.Envelope{}, err
	}
	if e.Type == protocol.TypeActionCallResolveRequest {
		return s.resolveCall(ctx, entry, e)
	}
	var resolution base.InteractionResolution
	var answer protocol.Envelope
	if e.Type == protocol.TypeActionPermissionResolveRequest {
		var request protocol.PermissionResolveRequest
		if err := e.DecodePayload(&request); err != nil {
			return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
		}
		resolution = base.InteractionResolution{RunID: request.RunID, RespondedBy: request.RespondedBy, Permission: &request}
		if err := entry.Resolve(ctx, resolution); err != nil {
			return protocol.Envelope{}, err
		}
		answer, err = protocol.NewEnvelope(protocol.TypeActionPermissionResolveResponse, s.nextID("response"), protocol.PermissionResolveResponse{
			InteractionID: request.InteractionID, SessionID: request.SessionID, RunID: request.RunID, Accepted: true,
		})
	} else {
		var request protocol.UserInputResolveRequest
		if err := e.DecodePayload(&request); err != nil {
			return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
		}
		resolution = base.InteractionResolution{RunID: request.RunID, RespondedBy: request.RespondedBy, Input: &request}
		if err := entry.Resolve(ctx, resolution); err != nil {
			return protocol.Envelope{}, err
		}
		answer, err = protocol.NewEnvelope(protocol.TypeUserInputResolveResponse, s.nextID("response"), protocol.UserInputResolveResponse{
			InteractionID: request.InteractionID, SessionID: request.SessionID, RunID: request.RunID, Accepted: true,
		})
	}
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = entry.ID()
	answer.RunID = resolution.RunID

	answer.CapabilityRevision = e.CapabilityRevision
	return answer, nil
}

func (s *Server) resolveCall(ctx context.Context, entry *serve.Session, e protocol.Envelope) (protocol.Envelope, error) {
	var request protocol.ActionCallResolveRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
	}
	result, err := entry.ResolveCall(ctx, base.CallResolution{RequestID: e.ID, Request: request})
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeActionCallResolveResponse, s.nextID("response"), result)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = entry.ID()
	answer.RunID = request.RunID
	answer.CapabilityRevision = e.CapabilityRevision
	return answer, nil
}

func (s *Server) models(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	entry, err := s.session(e)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var request protocol.ModelsRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
	}
	catalog, err := entry.Models(ctx, request)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeModelsResponse, s.nextID("response"), catalog.Models)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = entry.ID()
	answer.CapabilityRevision = catalog.Revision
	return answer, nil
}

func (s *Server) tools(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	entry, err := s.session(e)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var request protocol.ToolsListRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
	}
	catalog, err := entry.Tools(ctx, request)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeActionToolsListResponse, s.nextID("response"), catalog.Tools)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = entry.ID()
	answer.CapabilityRevision = catalog.Revision
	return answer, nil
}

func (s *Server) session(e protocol.Envelope) (*serve.Session, error) {
	if e.SessionID == "" {
		return nil, &refusal{code: "invalid_request", message: "this request must name its session"}
	}
	entry, err := s.hub.Session(e.SessionID)
	if err != nil {
		return nil, err
	}
	return entry, nil
}
