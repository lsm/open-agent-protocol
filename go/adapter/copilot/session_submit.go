package copilot

import (
	"context"
	"errors"
	"fmt"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var _ base.Session = (*session)(nil)

func (s *session) Submit(ctx context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	var zero protocol.MessageSubmitResponse
	if err := ctx.Err(); err != nil {
		return zero, nil, err
	}
	request := submit.Request
	if err := base.RefuseUnadvertisedControls(request, protocol.FeatureDeliveryQueue); err != nil {
		return zero, nil, err
	}
	if request.Delivery == "" {
		request.Delivery = protocol.DeliveryAuto
	}
	if request.SessionID != s.reducer.sessionID {
		return zero, nil, base.ErrRunNotFound
	}
	if request.TargetRunID != "" || (request.Delivery != protocol.DeliveryAuto && request.Delivery != protocol.DeliveryQueue) {
		return zero, nil, base.ErrInvalidSubmission
	}
	prompt, err := textPrompt(request.Messages)
	if err != nil {
		return zero, nil, err
	}
	select {
	case s.sending <- struct{}{}:
		defer func() { <-s.sending }()
	case <-ctx.Done():
		return zero, nil, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return zero, nil, err
	}
	if s.closed || s.closing || s.unusable {
		return zero, nil, base.ErrSessionClosed
	}
	busy := s.reducer.live() > 0
	admitted, err := s.reducer.admit(request.Delivery == protocol.DeliveryQueue)
	if err != nil {
		return zero, nil, err
	}
	follower := s.journal.Reserve()
	defer follower.Release()
	s.pending = admitted
	call, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var reply struct {
		MessageID string `json:"messageId"`
	}
	s.mu.Unlock()
	err = s.process.Client.Call(call, "session.send", map[string]string{"sessionId": s.reducer.nativeID, "prompt": prompt}, &reply)
	s.mu.Lock()
	s.pending = nil
	held := s.held
	s.held = nil
	if err == nil && (s.closed || s.closing || s.unusable) {
		err = base.ErrSessionClosed
	}
	if err != nil || reply.MessageID == "" {
		s.reducer.runs = s.reducer.runs[:len(s.reducer.runs)-1]
		if err == nil {
			err = fmt.Errorf("copilot: session.send returned no messageId")
		}
		var refusal *rpc.RemoteError
		if !errors.As(err, &refusal) {
			if !s.closing {
				s.end("copilot_process_exit", "Copilot prompt admission failed")
			}
			_ = s.process.Client.Close()
		}
		return zero, nil, err
	}
	admitted.nativeMessage = reply.MessageID
	ack := protocol.MessageSubmitResponse{SessionID: s.reducer.sessionID, Accepted: true, SubmissionID: protocol.SubmissionID(s.reducer.ids.NewID("submission")), RunID: admitted.id, Status: admitted.status, RequestedDelivery: request.Delivery, Admission: protocol.AdmissionStarted, EffectiveDelivery: protocol.DeliveryStart, DeliveryResolution: "session_idle", MessageIDs: []protocol.MessageID{admitted.messageID}}
	if admitted.status == protocol.RunQueued {
		ack.Admission = protocol.AdmissionQueued
		ack.EffectiveDelivery = protocol.EffectiveDeliveryQueue
	}
	if busy {
		ack.DeliveryResolution = "session_busy"
	}
	follower.Bind(admitted.id)
	for _, event := range held {
		if err := s.reducer.observe(event); err != nil {
			s.end("copilot_invalid_event", "the Copilot server sent an invalid session event")
			_ = s.process.Client.Close()
			break
		}
	}
	s.flush()
	return ack, follower.Stream(), nil
}

func (s *session) observe(event native.Event) error {
	unbound := event.Type == "user.message" && native.Text(event.Data, "messageId") != ""
	for _, run := range s.reducer.runs {
		if run.nativeMessage == native.Text(event.Data, "messageId") {
			unbound = false
			break
		}
	}
	if s.pending != nil && (len(s.held) > 0 || (event.SessionID == s.reducer.nativeID && unbound)) {
		if len(s.held) >= cap(s.process.Client.Notifications()) {
			return rpc.ErrBackpressure
		}
		s.held = append(s.held, event)
		return nil
	}
	return s.reducer.observe(event)
}

func textPrompt(messages []protocol.Message) (string, error) {
	var prompt strings.Builder
	textBytes := 0
	for index, message := range messages {
		if message.Role != protocol.RoleUser {
			return "", base.ErrInvalidSubmission
		}
		if index > 0 {
			prompt.WriteString("\n\n")
		}
		if text, ok := message.Content.Text(); ok {
			textBytes += len(text)
			prompt.WriteString(text)
			continue
		}
		parts, ok := message.Content.Parts()
		if !ok || parts == nil {
			return "", base.ErrInvalidSubmission
		}
		for _, part := range parts {
			if part.Type != protocol.ContentText {
				return "", base.ErrInvalidSubmission
			}
			textBytes += len(part.Text)
			prompt.WriteString(part.Text)
		}
	}
	if textBytes == 0 {
		return "", base.ErrInvalidSubmission
	}
	return prompt.String(), nil
}

func (*session) Resolve(ctx context.Context, _ base.InteractionResolution) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return base.ErrInteractionNotFound
}

func (*session) Cancel(ctx context.Context, _ protocol.RunID) (protocol.RunCancelResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	return protocol.RunCancelResponse{}, &base.UnsupportedControlError{Feature: "run.cancel", Reason: base.ControlUnadvertised}
}
