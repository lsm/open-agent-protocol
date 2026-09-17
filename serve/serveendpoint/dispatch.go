package serveendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// handle serves one host line. A decode fault is a framing defect and ends
// Run; everything after that is answered on the wire with exactly one
// correlated envelope, because the binding promises a request is never left
// without an answer.
func (s *Server) handle(ctx context.Context, streams context.Context, line []byte) error {
	// A line is an envelope or a binding control frame, told apart by which
	// members it carries. Routing on shape keeps the two vocabularies
	// separate: a control frame is this transport's business and never
	// reaches the hub as protocol.
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
	var envelope protocol.Envelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedLine, err)
	}
	if envelope.Type == "" || envelope.ID == "" {
		return fmt.Errorf("%w: an envelope needs a type and an id", ErrMalformedLine)
	}
	answer, err := s.serve(ctx, streams, envelope)
	if err != nil {
		answer = s.errorEnvelope(envelope, err)
	}
	return s.write(answer)
}

// serve dispatches one request envelope to the hub and returns the envelope
// that answers it. The dispatch is on the envelope's own type: an endpoint has
// no op names, because the protocol already names every request.
func (s *Server) serve(ctx context.Context, streams context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	switch e.Type {
	case protocol.TypeProtocolInitializeRequest:
		return s.initialize(ctx, e)
	case protocol.TypeCapabilitiesRequest:
		return s.capabilities(ctx, e)
	case protocol.TypeSessionOpenRequest:
		return s.open(ctx, e)
	case protocol.TypeSessionStateRequest:
		return s.state(ctx, e)
	case protocol.TypeSessionMessageSubmitRequest:
		return s.submit(ctx, streams, e)
	case protocol.TypeRunCancelRequest:
		return s.cancel(ctx, e)
	case protocol.TypeActionPermissionResolveRequest, protocol.TypeUserInputResolveRequest:
		return s.resolve(ctx, e)
	case protocol.TypeModelsRequest:
		return s.models(ctx, e)
	case protocol.TypeActionToolsListRequest:
		return s.tools(ctx, e)
	}
	return protocol.Envelope{}, &refusal{code: "unsupported_request", message: fmt.Sprintf("this endpoint serves no %s", e.Type)}
}

func (s *Server) initialize(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	descriptor, err := s.hub.Probe(ctx, s.adapter)
	if err != nil {
		return protocol.Envelope{}, err
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

func (s *Server) capabilities(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	descriptor, err := s.hub.Probe(ctx, s.adapter)
	if err != nil {
		return protocol.Envelope{}, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, s.nextID("response"), descriptor.Capabilities)
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
		Participant:           protocol.Participant{ID: serve.DefaultParticipant},
		AllowDegradedFeatures: request.AllowDegradedFeatures,
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

// submit admits one message and streams the run it admits.
//
// The subscription is taken before the submit, not after. A subscription
// started afterwards races the hub's own reader, which begins draining the
// adapter stream inside Submit, so the run's opening envelopes can be gone
// before the pump attaches. Subscribing first turns a race the endpoint
// usually wins into one it cannot lose.
func (s *Server) submit(ctx context.Context, streams context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	entry, err := s.session(e)
	if err != nil {
		return protocol.Envelope{}, err
	}
	var request protocol.MessageSubmitRequest
	if err := e.DecodePayload(&request); err != nil {
		return protocol.Envelope{}, &refusal{code: "invalid_payload", message: err.Error()}
	}
	subscription, err := s.hub.Subscribe(streams, entry.ID())
	if err != nil {
		return protocol.Envelope{}, err
	}
	admission, err := entry.Submit(ctx, request)
	if err != nil {
		subscription.Close()
		return protocol.Envelope{}, err
	}
	answer, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, s.nextID("response"), admission)
	if err != nil {
		subscription.Close()
		return protocol.Envelope{}, err
	}
	answer.InReplyTo = e.ID
	answer.SessionID = admission.SessionID
	answer.RunID = admission.RunID
	answer.CapabilityRevision = e.CapabilityRevision
	s.pumps.Add(1)
	go func() {
		defer s.pumps.Done()
		s.pump(subscription)
	}()
	return answer, nil
}

// pump writes one run's events as they are produced. A clean end at the run's
// terminal needs no signal of its own: the terminal envelope is the marker,
// exactly as it is on every other binding.
//
// Every other ending does need one. This transport's pipe stays open after a
// run stream dies, so a host that simply stopped receiving envelopes cannot
// tell a dead subscription from a slow agent, and its trace would be missing
// a terminal with nothing to say why. Each abnormal ending therefore emits a
// stream.lost control frame carrying the position the host actually reached,
// which is a cursor it can replay from — the recovery this binding already
// defines.
func (s *Server) pump(subscription *serve.Subscription) {
	defer subscription.Close()
	var run protocol.RunID
	var delivered uint64
	for {
		envelope, err := subscription.Next()
		if err != nil {
			s.reportLostStream(run, delivered, err)
			return
		}
		if envelope.RunID != "" {
			run = envelope.RunID
		}
		if writeErr := s.write(envelope); writeErr != nil {
			s.reportLostStream(run, delivered, writeErr)
			return
		}
		if envelope.Sequence != nil {
			delivered = *envelope.Sequence
		}
	}
}

// reportLostStream names an ending the host could not otherwise observe. A
// clean end and a teardown are not endings of this kind: the first carries
// its own terminal envelope, and the second ends the whole process.
func (s *Server) reportLostStream(run protocol.RunID, delivered uint64, err error) {
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return
	}
	code := "stream_failed"
	var overflow *serve.OverflowError
	switch {
	case errors.As(err, &overflow):
		code = "overflow"
		if overflow.RunID != "" {
			run = overflow.RunID
		}
		delivered = overflow.LastSequence
	case errors.Is(err, ErrFrameTooLarge):
		code = "frame_limit"
	}
	after := delivered
	s.logger.Printf("serveendpoint: run stream ended early (%s): %v", code, err)
	if writeErr := s.writeControl(controlFrame{
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
	// A resolution exercises an optional feature, so the exchange is bound to
	// one descriptor snapshot: the request cites the revision it was made
	// under and the response repeats it. Dropping it here makes an otherwise
	// conformant exchange fail validation as a stale revision.
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

// session resolves the session an envelope addresses. An endpoint carries one
// agent loop, but the envelope still names its session, and answering a
// request scoped to a session this endpoint does not hold is a refusal rather
// than a silent substitution.
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
