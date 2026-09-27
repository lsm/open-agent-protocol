package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	DefaultStallWindow = 2 * time.Minute
	DefaultMaxResumes  = 3
	runEventQueue      = 256
)

var (
	ErrStalled           = errors.New("adapter: the run's stream went silent")
	ErrRunFailed         = errors.New("adapter: the run failed")
	ErrRunCancelled      = errors.New("adapter: the run was cancelled")
	ErrNoTerminal        = errors.New("adapter: the stream ended without a terminal run event")
	ErrResumeUnsupported = errors.New("adapter: the session offers no stream to resume")
	ErrGateUnanswered    = errors.New("adapter: a gate was not answered")
	ErrNoCallResolver    = errors.New("adapter: the session does not resolve a tool call it asked the caller to provide")
)

type StalledError struct {
	Wait time.Duration
}

func (e *StalledError) Error() string { return fmt.Sprintf("adapter: no event for %s", e.Wait) }

func (e *StalledError) Unwrap() error { return ErrStalled }

type RunFailedError struct {
	Failure protocol.ProtocolError
	Usage   *protocol.Usage
}

func (e *RunFailedError) Error() string {
	return fmt.Sprintf("adapter: run failed: %s: %s", e.Failure.Code, e.Failure.Message)
}

func (e *RunFailedError) Unwrap() error { return ErrRunFailed }

type RunCancelledError struct{}

func (e *RunCancelledError) Error() string { return "adapter: the run was cancelled" }

func (e *RunCancelledError) Unwrap() error { return ErrRunCancelled }

type NoTerminalError struct{}

func (e *NoTerminalError) Error() string {
	return "adapter: the stream ended without a terminal run event"
}

func (e *NoTerminalError) Unwrap() error { return ErrNoTerminal }

type Gate struct {
	RunID         protocol.RunID
	InteractionID protocol.InteractionID
	UserInput     *protocol.UserInputRequestedPayload
	Permission    *protocol.PermissionRequestedPayload
	Call          *protocol.ActionCallPayload
	ToolName      string
	ToolArguments json.RawMessage

	answer    GateAnswer
	requestID protocol.EnvelopeID
}

type GateAnswer struct {
	Answers  []protocol.InputAnswer
	ChoiceID string
	Granted  bool
	Reason   string
	Call     *CallAnswer
}

type CallAnswer struct {
	Started bool
	Result  json.RawMessage
	Error   *protocol.ProtocolError
}

type GatePolicy func(context.Context, Gate) (GateAnswer, error)

type RunOptions struct {
	Policy      GatePolicy
	StallWindow time.Duration
	MaxResumes  int
	Observe     func(protocol.Envelope)
}

type RunOutcome struct {
	RunID     protocol.RunID
	Response  protocol.Message
	Text      string
	Usage     *protocol.Usage
	ToolCalls int
}

type announcedCall struct {
	name      string
	arguments json.RawMessage
}

func MessageText(message protocol.Message) (string, error) {
	if len(message.Content) == 0 {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(message.Content, &text); err == nil {
		return text, nil
	}
	var parts []protocol.ContentPart
	if err := json.Unmarshal(message.Content, &parts); err != nil {
		return "", fmt.Errorf("adapter: message content is neither text nor parts: %w", err)
	}
	var out strings.Builder
	for _, part := range parts {
		out.WriteString(part.Text)
	}
	return out.String(), nil
}

func pumpTo(events EventStream, queue chan<- Result, done <-chan struct{}, readerDone chan<- struct{}) {
	defer close(readerDone)
	pump(events, queue, done)
}

func pump(events EventStream, queue chan<- Result, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case result, ok := <-events:
			if !ok {
				close(queue)
				return
			}
			select {
			case queue <- result:
			case <-done:
				return
			}
		}
	}
}

func resolveGate(ctx context.Context, session Session, gate Gate) error {
	if gate.Call != nil {
		payload := gate.Call
		answer := gate.answer.Call
		request := protocol.ActionCallResolveRequest{
			InteractionID: payload.InteractionID, SessionID: payload.SessionID, RunID: payload.RunID,
			ToolCallID: payload.ToolCallID, RequestedBy: payload.RequestedBy, RespondedBy: payload.RespondedBy,
			Result: answer.Result, Error: answer.Error,
		}
		if answer.Started {
			return fmt.Errorf("adapter: the policy acknowledged %s without a result, and this helper asks once per call, so the run would wait for an answer that never comes: %w", gate.InteractionID, ErrGateUnanswered)
		}
		if request.Arm() == "" {
			return fmt.Errorf("adapter: the policy answered %s with no result, no error and no acknowledgement: %w", gate.InteractionID, ErrGateUnanswered)
		}
		resolver, ok := session.(CallResolver)
		if !ok {
			return fmt.Errorf("adapter: %s asks for a provided call and this session cannot resolve one: %w", gate.InteractionID, ErrNoCallResolver)
		}
		_, err := resolver.ResolveCall(ctx, CallResolution{RequestID: gate.requestID, Request: request})
		return err
	}
	if gate.UserInput != nil {
		payload := gate.UserInput
		if len(payload.Questions) > 0 && len(gate.answer.Answers) == 0 {
			return fmt.Errorf("adapter: the policy answered %s with no answers, and the schema asks for at least one: %w", gate.InteractionID, ErrGateUnanswered)
		}
		if _, err := IndexInputAnswers(payload.Questions, gate.answer.Answers); err != nil {
			return fmt.Errorf("adapter: the policy answered %s with an answer the questions do not accept: %w", gate.InteractionID, err)
		}
		return session.Resolve(ctx, InteractionResolution{
			RunID:       gate.RunID,
			RespondedBy: payload.RespondedBy,
			Input: &protocol.UserInputResolveRequest{
				InteractionID: gate.InteractionID,
				RequestedBy:   payload.RequestedBy,
				RespondedBy:   payload.RespondedBy,
				SessionID:     payload.SessionID,
				RunID:         payload.RunID,
				Answers:       gate.answer.Answers,
			},
		})
	}
	payload := gate.Permission
	if gate.answer.ChoiceID == "" {
		return fmt.Errorf("adapter: the policy answered %s granting %t without naming one of its %d choices: %w", gate.InteractionID, gate.answer.Granted, len(payload.Choices), ErrGateUnanswered)
	}
	return session.Resolve(ctx, InteractionResolution{
		RunID:       gate.RunID,
		RespondedBy: payload.RespondedBy,
		Permission: &protocol.PermissionResolveRequest{
			InteractionID: gate.InteractionID,
			RequestedBy:   payload.RequestedBy,
			RespondedBy:   payload.RespondedBy,
			SessionID:     payload.SessionID,
			RunID:         payload.RunID,
			ChoiceID:      gate.answer.ChoiceID,
			Granted:       gate.answer.Granted,
			Reason:        gate.answer.Reason,
		},
	})
}

func resetStall(timer *time.Timer, window time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(window)
}

func awaitResolves(ctx context.Context, resolving *sync.WaitGroup) error {
	settled := make(chan struct{})
	go func() {
		resolving.Wait()
		close(settled)
	}()
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func inputGate(runID protocol.RunID, envelope protocol.Envelope, calls map[protocol.ToolCallID]announcedCall) (Gate, error) {
	var payload protocol.UserInputRequestedPayload
	if err := envelope.DecodePayload(&payload); err != nil {
		return Gate{}, err
	}
	gate := Gate{RunID: runID, InteractionID: payload.InteractionID, UserInput: &payload}
	if call, announced := calls[payload.ToolCallID]; announced {
		gate.ToolName = call.name
		gate.ToolArguments = call.arguments
	}
	return gate, nil
}

func permissionGate(runID protocol.RunID, envelope protocol.Envelope, calls map[protocol.ToolCallID]announcedCall) (Gate, error) {
	var payload protocol.PermissionRequestedPayload
	if err := envelope.DecodePayload(&payload); err != nil {
		return Gate{}, err
	}
	gate := Gate{RunID: runID, InteractionID: payload.InteractionID, Permission: &payload}
	if call, announced := calls[payload.ToolCallID]; announced {
		gate.ToolName = call.name
		gate.ToolArguments = call.arguments
	}
	return gate, nil
}

func RunToTerminal(ctx context.Context, session Session, request protocol.MessageSubmitRequest, options RunOptions) (outcome RunOutcome, err error) {
	stall := options.StallWindow
	if stall <= 0 {
		stall = DefaultStallWindow
	}
	maxResumes := options.MaxResumes
	if maxResumes <= 0 {
		maxResumes = DefaultMaxResumes
	}

	submitCtx, cancelSubmit := context.WithTimeout(ctx, stall)
	admission, events, err := session.Submit(submitCtx, request)
	submitStalled := ctx.Err() == nil && errors.Is(submitCtx.Err(), context.DeadlineExceeded)
	cancelSubmit()
	if err != nil {
		if submitStalled {
			return RunOutcome{}, &StalledError{Wait: stall}
		}
		return RunOutcome{}, err
	}

	runID := admission.RunID
	defer func() {
		if outcome.RunID == "" {
			outcome.RunID = runID
		}
		if err != nil && ctx.Err() != nil && runID != "" {
			_, _ = session.Cancel(context.WithoutCancel(ctx), runID)
		}
	}()

	var (
		lastSeq    uint64
		terminal   bool
		resumes    int
		calls      = map[protocol.ToolCallID]announcedCall{}
		resolving  sync.WaitGroup
		resolveErr = make(chan error, 1)
		done       = make(chan struct{})
	)
	defer close(done)

	queue := make(chan Result, runEventQueue)
	readerDone := make(chan struct{})
	go pumpTo(events, queue, done, readerDone)
	stallTimer := time.NewTimer(stall)
	defer stallTimer.Stop()

	answerGate := func(gate Gate) {
		resolving.Add(1)
		go func() {
			defer resolving.Done()
			if err := resolveGate(ctx, session, gate); err != nil {
				select {
				case resolveErr <- err:
				default:
				}
			}
		}()
	}

drain:
	for {
		var result Result
		var ok bool
		takeQueued := func() bool {
			select {
			case result, ok = <-queue:
				return true
			default:
				return false
			}
		}
		select {
		case <-ctx.Done():
			return outcome, ctx.Err()
		case gateErr := <-resolveErr:
			return outcome, gateErr
		case <-stallTimer.C:
			if takeQueued() {
				break
			}
			if err := awaitResolves(ctx, &resolving); err != nil {
				return outcome, err
			}
			if takeQueued() {
				break
			}
			select {
			case gateErr := <-resolveErr:
				return outcome, gateErr
			default:
			}
			return outcome, &StalledError{Wait: stall}
		case result, ok = <-queue:
		}
		if !ok {
			break drain
		}
		resetStall(stallTimer, stall)
		if result.Error != nil {
			if !errors.Is(result.Error, ErrEventStreamOverflow) {
				return outcome, result.Error
			}
			if runID == "" {
				return outcome, fmt.Errorf("adapter: the stream overflowed before the run named itself: %w", result.Error)
			}
			if resumes >= maxResumes {
				return outcome, fmt.Errorf("adapter: resume bound %d reached: %w", maxResumes, result.Error)
			}
			resumes++
			recovery, replay, resumeErr := session.Resume(ctx, ResumeRequest{RunID: runID, AfterSequence: lastSeq})
			resetStall(stallTimer, stall)
			var gap *ReplayGap
			switch {
			case errors.As(resumeErr, &gap):
				return outcome, gap
			case resumeErr != nil:
				return outcome, resumeErr
			case recovery.ReplayGap != nil:
				return outcome, recovery.ReplayGap
			case replay == nil:
				return outcome, ErrResumeUnsupported
			}
			recovered := time.NewTimer(stall)
			select {
			case <-readerDone:
			case <-ctx.Done():
				recovered.Stop()
				return outcome, ctx.Err()
			case <-recovered.C:
			}
			recovered.Stop()
			resetStall(stallTimer, stall)
			queue = make(chan Result, runEventQueue)
			readerDone = make(chan struct{})
			go pumpTo(replay, queue, done, readerDone)
			continue drain
		}

		envelope := result.Envelope
		if options.Observe != nil {
			options.Observe(envelope)
			resetStall(stallTimer, stall)
		}
		if envelope.RunID != "" {
			runID = envelope.RunID
		}
		if envelope.Sequence != nil {
			if *envelope.Sequence <= lastSeq {
				continue
			}
			lastSeq = *envelope.Sequence
		}
		switch envelope.Type {
		case protocol.TypeActionCallRequested:
			var call protocol.ActionCallPayload
			if err := envelope.DecodePayload(&call); err != nil {
				return outcome, err
			}
			calls[call.ToolCallID] = announcedCall{name: call.Name, arguments: call.ArgumentsJSON}
			if call.InteractionID == "" {
				break
			}
			if options.Policy == nil {
				return outcome, ErrGateUnanswered
			}
			gate := Gate{RunID: runID, InteractionID: call.InteractionID, Call: &call, ToolName: call.Name, ToolArguments: call.ArgumentsJSON, requestID: envelope.ID}
			answer, err := options.Policy(ctx, gate)
			resetStall(stallTimer, stall)
			if err != nil {
				return outcome, err
			}
			if answer.Call == nil {
				return outcome, ErrGateUnanswered
			}
			gate.answer = answer
			answerGate(gate)
		case protocol.TypeActionCallStarted:
			outcome.ToolCalls++
		case protocol.TypeUserInputRequested:
			gate, err := inputGate(runID, envelope, calls)
			if err != nil {
				return outcome, err
			}
			if options.Policy == nil {
				return outcome, ErrGateUnanswered
			}
			answer, err := options.Policy(ctx, gate)
			resetStall(stallTimer, stall)
			if err != nil {
				return outcome, err
			}
			gate.answer = answer
			answerGate(gate)
		case protocol.TypeActionPermissionRequested:
			gate, err := permissionGate(runID, envelope, calls)
			if err != nil {
				return outcome, err
			}
			if options.Policy == nil {
				return outcome, ErrGateUnanswered
			}
			answer, err := options.Policy(ctx, gate)
			resetStall(stallTimer, stall)
			if err != nil {
				return outcome, err
			}
			gate.answer = answer
			answerGate(gate)
		case protocol.TypeRunCompleted:
			var completed protocol.RunCompletedPayload
			if err := envelope.DecodePayload(&completed); err != nil {
				return outcome, err
			}
			text, err := MessageText(completed.FinalResponse)
			if err != nil {
				return outcome, err
			}
			outcome.RunID = completed.RunID
			outcome.Response = completed.FinalResponse
			outcome.Text = text
			outcome.Usage = completed.Usage
			terminal = true
		case protocol.TypeRunFailed:
			var failed protocol.RunFailedPayload
			if err := envelope.DecodePayload(&failed); err != nil {
				return outcome, err
			}
			outcome.RunID = failed.RunID
			outcome.Usage = failed.Usage
			return outcome, &RunFailedError{Failure: failed.Error, Usage: failed.Usage}
		case protocol.TypeRunCancelled:
			return outcome, &RunCancelledError{}
		}
	}
	if err := awaitResolves(ctx, &resolving); err != nil {
		return outcome, err
	}
	select {
	case gateErr := <-resolveErr:
		return outcome, gateErr
	default:
	}
	if !terminal {
		return outcome, &NoTerminalError{}
	}
	return outcome, nil
}
