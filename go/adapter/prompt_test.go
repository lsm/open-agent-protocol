package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestRunToTerminalAnswersBothGatesAndReportsTheRun(t *testing.T) {
	session := newTestSession(t, 64)
	var gates []string
	var seen int
	outcome, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{
		Policy: func(_ context.Context, gate adapter.Gate) (adapter.GateAnswer, error) {
			if gate.ToolName == "" {
				return adapter.GateAnswer{}, errors.New("a gate named no tool")
			}
			if gate.Permission != nil {
				gates = append(gates, "permission:"+gate.ToolName)
				return adapter.GateAnswer{ChoiceID: "approve", Granted: true}, nil
			}
			question := gate.UserInput.Questions[0]
			gates = append(gates, "input:"+gate.ToolName)
			return adapter.GateAnswer{Answers: []protocol.InputAnswer{{QuestionID: question.ID, SelectedOptionIDs: []string{question.Options[0].ID}}}}, nil
		},
		Observe: func(protocol.Envelope) { seen++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Text != "The golden script completed." {
		t.Fatalf("text = %q", outcome.Text)
	}
	if outcome.RunID == "" {
		t.Fatal("outcome names no run")
	}
	if outcome.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1", outcome.ToolCalls)
	}
	if seen == 0 {
		t.Fatal("the observer saw no envelope")
	}
	if len(gates) < 2 {
		t.Fatalf("gates answered = %v, want a permission and an input", gates)
	}
}

func TestRunToTerminalRefusesAGateWithNoPolicy(t *testing.T) {
	session := newTestSession(t, 64)
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{})
	if !errors.Is(err, adapter.ErrGateUnanswered) {
		t.Fatalf("err = %v, want the unanswered gate", err)
	}
}

func TestRunToTerminalReportsAFailedRun(t *testing.T) {
	failure := protocol.ProtocolError{Code: "provider_unavailable", Message: "the provider is down"}
	payload := protocol.RunFailedPayload{SessionID: "session-1", RunID: "run-1", Error: failure, Usage: &protocol.Usage{InputTokens: 4}}
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeRunFailed, 1, payload)},
	}}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{})
	var failed *adapter.RunFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("err = %v, want a failed run", err)
	}
	if failed.Failure.Code != failure.Code || failed.Usage == nil || failed.Usage.InputTokens != 4 {
		t.Fatalf("failure = %+v", failed)
	}
	if !errors.Is(err, adapter.ErrRunFailed) {
		t.Fatalf("err = %v, want ErrRunFailed", err)
	}
}

func TestRunToTerminalReportsACancelledRun(t *testing.T) {
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeRunCancelled, 1, protocol.RunCancelledPayload{SessionID: "session-1", RunID: "run-1"})},
	}}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{})
	if !errors.Is(err, adapter.ErrRunCancelled) {
		t.Fatalf("err = %v, want the cancelled run", err)
	}
}

func TestRunToTerminalReportsAStreamWithNoTerminal(t *testing.T) {
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeRunStatusUpdated, 1, protocol.RunStatusUpdatedPayload{SessionID: "session-1", RunID: "run-1", Status: protocol.RunRunning})},
	}}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{})
	if !errors.Is(err, adapter.ErrNoTerminal) {
		t.Fatalf("err = %v, want no terminal", err)
	}
}

func TestRunToTerminalResumesAfterAnOverflow(t *testing.T) {
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeContentDelta, 7, protocol.ContentDeltaPayload{SessionID: "session-1", RunID: "run-1", MessageID: "message-1", Part: protocol.ContentPart{Type: protocol.ContentText, Text: "hello "}})},
		{Error: adapter.ErrEventStreamOverflow},
	}}
	replay := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeRunCompleted, 9, protocol.RunCompletedPayload{SessionID: "session-1", RunID: "run-1", FinalResponse: protocol.Message{ID: "message-1", Role: protocol.RoleAssistant, Content: protocol.TextContent("hello world")}})},
	}}
	session.resume = replay
	outcome, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Text != "hello world" {
		t.Fatalf("text = %q", outcome.Text)
	}
	if session.resumeRequest.RunID != "run-1" || session.resumeRequest.AfterSequence != 7 {
		t.Fatalf("resumed from %+v", session.resumeRequest)
	}
}

func TestRunToTerminalReportsAReplayGap(t *testing.T) {
	gap := &adapter.ReplayGap{RequestedAfter: 3, OldestAvailable: 9, LatestAvailable: 12}
	session := &scriptedSession{
		results: []adapter.Result{
			{Envelope: envelope(protocol.TypeContentDelta, 4, protocol.ContentDeltaPayload{SessionID: "session-1", RunID: "run-1", MessageID: "message-1", Part: protocol.ContentPart{Type: protocol.ContentText, Text: "hi"}})},
			{Error: adapter.ErrEventStreamOverflow},
		},
		resumeError: gap,
	}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{})
	var reported *adapter.ReplayGap
	if !errors.As(err, &reported) || reported.RequestedAfter != 3 {
		t.Fatalf("err = %v, want the replay gap", err)
	}
}

func TestRunToTerminalReportsAGapCarriedInTheRecovery(t *testing.T) {
	gap := &adapter.ReplayGap{RequestedAfter: 5, OldestAvailable: 11, LatestAvailable: 14}
	session := &scriptedSession{
		results: []adapter.Result{
			{Envelope: envelope(protocol.TypeContentDelta, 5, protocol.ContentDeltaPayload{SessionID: "session-1", RunID: "run-1", MessageID: "message-1", Part: protocol.ContentPart{Type: protocol.ContentText, Text: "hi"}})},
			{Error: adapter.ErrEventStreamOverflow},
		},
		recoveryGap: gap,
	}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{})
	var reported *adapter.ReplayGap
	if !errors.As(err, &reported) || reported.OldestAvailable != 11 {
		t.Fatalf("err = %v, want the gap the recovery carries", err)
	}
}

func TestRunToTerminalBoundsResumes(t *testing.T) {
	overflow := adapter.Result{Error: adapter.ErrEventStreamOverflow}
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeContentDelta, 1, protocol.ContentDeltaPayload{SessionID: "session-1", RunID: "run-1", MessageID: "message-1", Part: protocol.ContentPart{Type: protocol.ContentText, Text: "hi"}})},
		overflow, overflow, overflow, overflow,
	}}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{MaxResumes: 1})
	if !errors.Is(err, adapter.ErrEventStreamOverflow) {
		t.Fatalf("err = %v, want the overflow past the bound", err)
	}
	if session.resumes != 1 {
		t.Fatalf("resumed %d times, want 1", session.resumes)
	}
}

func TestRunToTerminalReportsAStall(t *testing.T) {
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeContentDelta, 1, protocol.ContentDeltaPayload{SessionID: "session-1", RunID: "run-1", MessageID: "message-1", Part: protocol.ContentPart{Type: protocol.ContentText, Text: "hi"}})},
	}, hold: true}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{StallWindow: 20 * time.Millisecond})
	var stalled *adapter.StalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("err = %v, want a stall", err)
	}
	if stalled.Wait != 20*time.Millisecond {
		t.Fatalf("stall wait = %s", stalled.Wait)
	}
}

func TestRunToTerminalRefusesAnInvalidAnswerBeforeResolving(t *testing.T) {
	ask := protocol.UserInputRequestedPayload{
		InteractionID: "interaction-1", RequestedBy: "harness", RespondedBy: "user",
		SessionID: "session-1", RunID: "run-1", Title: "Continue?",
		Questions: []protocol.InputQuestion{{
			ID: "choice", Prompt: "Continue?", Kind: protocol.InputSingleChoice, Required: true,
			Options: []protocol.InputOption{{ID: "yes", Label: "Yes"}},
		}},
	}
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeUserInputRequested, 1, ask)},
	}}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{
		Policy: func(context.Context, adapter.Gate) (adapter.GateAnswer, error) {
			return adapter.GateAnswer{Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"not-offered"}}}}, nil
		},
	})
	if !errors.Is(err, adapter.ErrInvalidResolution) {
		t.Fatalf("err = %v, want the invalid answer refused", err)
	}
	if len(session.resolved) != 0 {
		t.Fatalf("resolved %d interactions after an answer the validator refused", len(session.resolved))
	}
}

func TestRunToTerminalRefusesAPermissionThePolicyNamedNoChoiceFor(t *testing.T) {
	ask := protocol.PermissionRequestedPayload{
		InteractionID: "permission-1", RequestedBy: "harness", RespondedBy: "user",
		SessionID: "session-1", RunID: "run-1", Title: "Allow it",
		Choices: []protocol.PermissionChoice{{ID: "allow", Label: "Allow"}, {ID: "deny", Label: "Deny"}},
	}
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeActionPermissionRequested, 1, ask)},
	}}
	_, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{
		Policy: func(context.Context, adapter.Gate) (adapter.GateAnswer, error) {
			return adapter.GateAnswer{Granted: true}, nil
		},
	})
	if !errors.Is(err, adapter.ErrGateUnanswered) {
		t.Fatalf("err = %v, want the unanswered permission", err)
	}
	if len(session.resolved) != 0 {
		t.Fatalf("resolved %d interactions without a choice to resolve them with", len(session.resolved))
	}
}

func TestRunToTerminalCancelsTheRunWhenItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	session := &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeContentDelta, 1, protocol.ContentDeltaPayload{SessionID: "session-1", RunID: "run-1", MessageID: "message-1", Part: protocol.ContentPart{Type: protocol.ContentText, Text: "hi"}})},
	}, hold: true}
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := adapter.RunToTerminal(ctx, session, plainSubmit, adapter.RunOptions{StallWindow: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancelled context", err)
	}
	if session.cancelled != "run-1" {
		t.Fatalf("cancelled %q, want run-1", session.cancelled)
	}
}

func TestRunToTerminalPrefersAQueuedEventOverAFiredStall(t *testing.T) {
	ask := protocol.UserInputRequestedPayload{
		InteractionID: "interaction-1", RequestedBy: "harness", RespondedBy: "user",
		SessionID: "session-1", RunID: "run-1", Title: "Continue?",
		Questions: []protocol.InputQuestion{{
			ID: "choice", Prompt: "Continue?", Kind: protocol.InputSingleChoice, Required: true,
			Options: []protocol.InputOption{{ID: "yes", Label: "Yes"}},
		}},
	}
	results := []adapter.Result{{Envelope: envelope(protocol.TypeUserInputRequested, 1, ask)}}
	for sequence := 2; sequence <= 12; sequence++ {
		results = append(results, adapter.Result{Envelope: envelope(protocol.TypeContentDelta, sequence, protocol.ContentDeltaPayload{
			SessionID: "session-1", RunID: "run-1", MessageID: "message-1",
			Part: protocol.ContentPart{Type: protocol.ContentText, Text: "working"},
		})})
	}
	results = append(results, adapter.Result{Envelope: envelope(protocol.TypeRunCompleted, 13, protocol.RunCompletedPayload{
		SessionID: "session-1", RunID: "run-1",
		FinalResponse: protocol.Message{ID: "message-1", Role: protocol.RoleAssistant, Content: protocol.TextContent("done")},
	})})
	session := &scriptedSession{results: results}
	outcome, err := adapter.RunToTerminal(context.Background(), session, plainSubmit, adapter.RunOptions{
		StallWindow: 5 * time.Millisecond,
		Policy: func(context.Context, adapter.Gate) (adapter.GateAnswer, error) {
			time.Sleep(20 * time.Millisecond)
			return adapter.GateAnswer{Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}, nil
		},
	})
	if err != nil {
		t.Fatalf("err = %v, want the run to finish rather than stall behind its own policy", err)
	}
	if outcome.Text != "done" {
		t.Fatalf("text = %q", outcome.Text)
	}
}

func TestRunToTerminalStopsWaitingForAnOverflowedStreamThatNeverCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &scriptedSession{
		results: []adapter.Result{
			{Envelope: envelope(protocol.TypeContentDelta, 1, protocol.ContentDeltaPayload{SessionID: "session-1", RunID: "run-1", MessageID: "message-1", Part: protocol.ContentPart{Type: protocol.ContentText, Text: "hi"}})},
			{Error: adapter.ErrEventStreamOverflow},
		},
		hold: true,
	}
	session.resume = &scriptedSession{results: []adapter.Result{
		{Envelope: envelope(protocol.TypeRunCompleted, 2, protocol.RunCompletedPayload{SessionID: "session-1", RunID: "run-1", FinalResponse: protocol.Message{ID: "message-1", Role: protocol.RoleAssistant, Content: protocol.TextContent("done")}})},
	}}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := adapter.RunToTerminal(ctx, session, plainSubmit, adapter.RunOptions{StallWindow: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancelled context rather than a wait on a stream that never closes", err)
	}
}

func TestRunToTerminalCancelsTheQueuedRunTheAdmissionNamed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	session := &scriptedSession{admissionRun: "run-9", hold: true}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := adapter.RunToTerminal(ctx, session, plainSubmit, adapter.RunOptions{StallWindow: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancelled context", err)
	}
	if session.cancelled != "run-9" {
		t.Fatalf("cancelled %q, want the run the admission named", session.cancelled)
	}
}

func envelope(typ protocol.EnvelopeType, sequence int, payload any) protocol.Envelope {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	value := uint64(sequence)
	return protocol.Envelope{
		Protocol:  "open-agent-protocol",
		Version:   "0.1",
		Profile:   "open-agent-protocol.agent-control-core",
		Type:      typ,
		ID:        protocol.EnvelopeID("event"),
		Payload:   raw,
		Sequence:  &value,
		SessionID: "session-1",
		RunID:     "run-1",
	}
}

type scriptedSession struct {
	mu            sync.Mutex
	results       []adapter.Result
	hold          bool
	resume        *scriptedSession
	resumeError   error
	recoveryGap   *adapter.ReplayGap
	admissionRun  protocol.RunID
	resumes       int
	resumeRequest adapter.ResumeRequest
	cancelled     protocol.RunID
	resolved      []adapter.InteractionResolution
}

func (s *scriptedSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, adapter.EventStream, error) {
	stream := make(chan adapter.Result, len(s.results)+1)
	for _, result := range s.results {
		stream <- result
	}
	if !s.hold {
		close(stream)
	}
	return protocol.MessageSubmitResponse{SessionID: "session-1", Accepted: true, Admission: protocol.AdmissionStarted, RunID: s.admissionRun}, stream, nil
}

func (s *scriptedSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: "session-1"}, nil
}

func (s *scriptedSession) Resolve(_ context.Context, resolution adapter.InteractionResolution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolved = append(s.resolved, resolution)
	return nil
}

func (s *scriptedSession) Cancel(_ context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelled = runID
	return protocol.RunCancelResponse{SessionID: "session-1", RunID: runID}, nil
}

func (s *scriptedSession) Resume(_ context.Context, request adapter.ResumeRequest) (adapter.Recovery, adapter.EventStream, error) {
	s.mu.Lock()
	s.resumes++
	s.resumeRequest = request
	resume := s.resume
	resumeErr := s.resumeError
	gap := s.recoveryGap
	s.mu.Unlock()
	if resumeErr != nil {
		return adapter.Recovery{}, nil, resumeErr
	}
	replayed := []adapter.Result{{Error: adapter.ErrEventStreamOverflow}}
	if resume != nil {
		replayed = resume.results
	}
	stream := make(chan adapter.Result, len(replayed))
	for _, result := range replayed {
		stream <- result
	}
	close(stream)
	return adapter.Recovery{RunID: request.RunID, RequestedAfter: request.AfterSequence, ReplayGap: gap}, stream, nil
}

func (s *scriptedSession) Close(context.Context) error { return nil }
