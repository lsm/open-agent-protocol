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
	ErrGateUnanswered    = errors.New("adapter: a gate arrived and no policy answers it")
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
	ToolName      string
	ToolArguments json.RawMessage

	answer GateAnswer
}

type GateAnswer struct {
	Answers []protocol.InputAnswer
	Granted bool
	Reason  string
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

func inputQuestion(questions []protocol.InputQuestion, id string) (protocol.InputQuestion, bool) {
	for _, question := range questions {
		if question.ID == id {
			return question, true
		}
	}
	return protocol.InputQuestion{}, false
}

func resolveGate(ctx context.Context, session Session, gate Gate) error {
	if gate.UserInput != nil {
		payload := gate.UserInput
		for _, answer := range gate.answer.Answers {
			question, found := inputQuestion(payload.Questions, answer.QuestionID)
			if !found {
				return fmt.Errorf("adapter: answer names no question of %s: %w", gate.InteractionID, ErrInvalidResolution)
			}
			if err := ValidateInputAnswer(question, answer); err != nil {
				return fmt.Errorf("adapter: answer for %s: %w", answer.QuestionID, err)
			}
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
	return session.Resolve(ctx, InteractionResolution{
		RunID:       gate.RunID,
		RespondedBy: payload.RespondedBy,
		Permission: &protocol.PermissionResolveRequest{
			InteractionID: gate.InteractionID,
			RequestedBy:   payload.RequestedBy,
			RespondedBy:   payload.RespondedBy,
			SessionID:     payload.SessionID,
			RunID:         payload.RunID,
			Granted:       gate.answer.Granted,
			Reason:        gate.answer.Reason,
		},
	})
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

func RunToTerminal(ctx context.Context, session Session, request protocol.MessageSubmitRequest, options RunOptions) (RunOutcome, error) {
	stall := options.StallWindow
	if stall <= 0 {
		stall = DefaultStallWindow
	}
	maxResumes := options.MaxResumes
	if maxResumes <= 0 {
		maxResumes = DefaultMaxResumes
	}

	submitCtx, cancelSubmit := context.WithTimeout(ctx, stall)
	_, events, err := session.Submit(submitCtx, request)
	submitStalled := ctx.Err() == nil && errors.Is(submitCtx.Err(), context.DeadlineExceeded)
	cancelSubmit()
	if err != nil {
		if submitStalled {
			return RunOutcome{}, &StalledError{Wait: stall}
		}
		return RunOutcome{}, err
	}

	var (
		outcome    RunOutcome
		runID      protocol.RunID
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
		select {
		case <-ctx.Done():
			if runID != "" {
				_, _ = session.Cancel(context.WithoutCancel(ctx), runID)
			}
			return outcome, ctx.Err()
		case gateErr := <-resolveErr:
			return outcome, gateErr
		case <-stallTimer.C:
			resolving.Wait()
			return outcome, &StalledError{Wait: stall}
		case result, ok := <-queue:
			if !ok {
				break drain
			}
			if !stallTimer.Stop() {
				select {
				case <-stallTimer.C:
				default:
				}
			}
			stallTimer.Reset(stall)
			if result.Error != nil {
				if !errors.Is(result.Error, ErrEventStreamOverflow) {
					return outcome, result.Error
				}
				if runID == "" || resumes >= maxResumes {
					return outcome, fmt.Errorf("adapter: resume bound %d reached: %w", maxResumes, result.Error)
				}
				resumes++
				_, replay, resumeErr := session.Resume(ctx, ResumeRequest{RunID: runID, AfterSequence: lastSeq})
				var gap *ReplayGap
				switch {
				case errors.As(resumeErr, &gap):
					return outcome, gap
				case resumeErr != nil:
					return outcome, resumeErr
				case replay == nil:
					return outcome, ErrResumeUnsupported
				}
				<-readerDone
				queue = make(chan Result, runEventQueue)
				readerDone = make(chan struct{})
				go pumpTo(replay, queue, done, readerDone)
				continue drain
			}

			envelope := result.Envelope
			if options.Observe != nil {
				options.Observe(envelope)
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
	}
	resolving.Wait()
	select {
	case gateErr := <-resolveErr:
		return outcome, gateErr
	default:
	}
	if !terminal {
		return outcome, &NoTerminalError{}
	}
	outcome.RunID = runID
	return outcome, nil
}
