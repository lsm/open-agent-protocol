package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/codex/appserver/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/codex/appserver/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

type session struct {
	mu       sync.Mutex
	opMu     sync.Mutex
	emitMu   sync.Mutex
	client   Client
	clock    adapter.Clock
	ids      adapter.IDGenerator
	capacity int

	participant  protocol.ParticipantID
	threadID     string
	model        string
	state        protocol.SessionState
	closed       bool
	active       *runState
	runs         map[protocol.RunID]*runState
	turns        map[string]protocol.RunID
	items        map[string]itemBinding
	interactions map[protocol.InteractionID]*interactionBinding
	journal      []protocol.Envelope
	stop         chan struct{}
}

type runState struct {
	id             protocol.RunID
	turnID         string
	status         protocol.RunStatus
	nextSequence   uint64
	started        bool
	terminal       bool
	cancelPending  bool
	cancelInFlight chan struct{}
	messageID      protocol.MessageID
	text           string
	subscribers    []*subscriber
}

const liveStreamCapacity = 32

type subscriber struct {
	stream   chan adapter.Result
	detached bool
}

type itemBinding struct {
	runID      protocol.RunID
	toolCallID protocol.ToolCallID
	name       string
	arguments  json.RawMessage
	started    bool
	terminal   bool
}

type interactionKind uint8

const (
	permissionInteraction interactionKind = iota + 1
	inputInteraction
)

type interactionBinding struct {
	kind              interactionKind
	runID             protocol.RunID
	toolCallID        protocol.ToolCallID
	requestedBy       protocol.ParticipantID
	respondedBy       protocol.ParticipantID
	request           *rpc.IncomingRequest
	resolved          bool
	questions         []protocol.InputQuestion
	optionLabels      map[string]map[string]string
	permissionChoices map[string]bool
}

func (session *session) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, adapter.EventStream, error) {
	session.opMu.Lock()
	defer session.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	if request.SessionID != session.state.SessionID || len(request.Messages) == 0 {
		return protocol.MessageSubmitResponse{}, nil, adapter.ErrInvalidSubmission
	}
	if request.Delivery != "" && request.Delivery != protocol.DeliveryAuto {
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: delivery %q", adapter.ErrInvalidSubmission, request.Delivery)
	}
	if request.Instructions != "" || len(request.ToolChoice) != 0 || len(request.OutputSchema) != 0 || len(request.AllowDegradedFeatures) != 0 || len(request.Metadata) != 0 {
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: instructions, tool choice, output schema, degraded-feature consent, and metadata are not supported", adapter.ErrInvalidSubmission)
	}
	input, messageIDs, err := session.nativeInput(request.Messages)
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, adapter.ErrSessionClosed
	}
	if session.active != nil && !session.active.terminal {
		session.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, adapter.ErrRunActive
	}
	session.mu.Unlock()

	params := native.TurnStartParams{ThreadID: session.threadID, Input: input, Model: request.ModelID}
	if params.Model == "" {
		params.Model = session.model
	}
	run := &runState{
		id: protocol.RunID(session.ids.NewID("run")), status: protocol.RunQueued,
		nextSequence: 1, messageID: protocol.MessageID(session.ids.NewID("message")),
	}
	stream := make(chan adapter.Result, liveStreamCapacity+1)
	run.subscribers = append(run.subscribers, &subscriber{stream: stream})
	// Reserve the one active-run slot before calling Codex. The dispatch loop uses
	// opMu too, so notifications cannot overtake response admission and mapping.
	session.mu.Lock()
	session.active = run
	session.state.Status = protocol.SessionQueued
	session.state.ActiveRunID = run.id
	session.state.CurrentModelID = params.Model
	session.state.UpdatedAtMS = session.clock.Now().UnixMilli()
	session.mu.Unlock()
	var nativeResponse native.TurnStartResponse
	if err := session.client.Call(ctx, native.MethodTurnStart, params, &nativeResponse); err != nil {
		session.mu.Lock()
		session.active = nil
		session.state.ActiveRunID = ""
		if session.client.Err() != nil {
			// The transport was retired while the request may already have been
			// written (a caller context that expires after the write retires the
			// client). Codex may be executing the turn, and no native settlement
			// can arrive on the dead transport, so reporting a clean idle would
			// invite another submit onto a session that can never settle it.
			// Retire the session instead.
			session.closed = true
			session.state.Status = protocol.SessionClosed
			close(session.stop)
		} else {
			// The request never reached Codex; the reservation is released.
			session.state.Status = protocol.SessionIdle
		}
		session.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("start Codex turn: %w", err)
	}
	if nativeResponse.Turn.ID == "" {
		// turn/start succeeded but named no turn, so Codex may already be
		// executing a turn that could never be correlated through session.turns.
		// Retire the session rather than release it for another submission.
		session.mu.Lock()
		session.active = nil
		session.closed = true
		session.state.Status = protocol.SessionClosed
		session.state.ActiveRunID = ""
		close(session.stop)
		session.mu.Unlock()
		return protocol.MessageSubmitResponse{}, nil, fmt.Errorf("%w: turn/start returned no turn id", ErrNativeProtocol)
	}
	run.turnID = nativeResponse.Turn.ID
	session.mu.Lock()
	session.runs[run.id] = run
	session.turns[run.turnID] = run.id
	session.mu.Unlock()

	return protocol.MessageSubmitResponse{
		SessionID: session.state.SessionID, Accepted: true,
		SubmissionID:      protocol.SubmissionID(session.ids.NewID("submission")),
		RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart,
		DeliveryResolution: "session_idle", Admission: protocol.AdmissionStarted,
		// A started admission reports the running status (decision 0002);
		// the run's internal state still promotes at run.started.
		RunID: run.id, Status: protocol.RunRunning, ModelID: params.Model, MessageIDs: messageIDs,
	}, stream, nil
}

func (session *session) nativeInput(messages []protocol.Message) ([]native.UserInput, []protocol.MessageID, error) {
	input := make([]native.UserInput, 0, len(messages))
	ids := make([]protocol.MessageID, len(messages))
	for index, message := range messages {
		if message.Role != protocol.RoleUser {
			return nil, nil, fmt.Errorf("%w: role %q; turn/start accepts new user input only", ErrUnsupportedInput, message.Role)
		}
		text, ok := message.Content.Text()
		if !ok {
			return nil, nil, fmt.Errorf("%w: only text messages are supported", ErrUnsupportedInput)
		}
		input = append(input, native.UserInput{Type: "text", Text: text, TextElements: []native.TextElement{}})
		ids[index] = message.ID
		if ids[index] == "" {
			ids[index] = protocol.MessageID(session.ids.NewID("message"))
		}
	}
	return input, ids, nil
}

func (session *session) State(ctx context.Context) (protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionState{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return session.state, adapter.ErrSessionClosed
	}
	return session.state, nil
}

func (session *session) Resolve(ctx context.Context, resolution adapter.InteractionResolution) error {
	session.opMu.Lock()
	defer session.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	session.mu.Lock()
	var id protocol.InteractionID
	if resolution.Permission != nil && resolution.Input == nil {
		id = resolution.Permission.InteractionID
	} else if resolution.Input != nil && resolution.Permission == nil {
		id = resolution.Input.InteractionID
	} else {
		session.mu.Unlock()
		return adapter.ErrInvalidResolution
	}
	binding := session.interactions[id]
	if binding == nil || binding.runID != resolution.RunID {
		session.mu.Unlock()
		return adapter.ErrInteractionNotFound
	}
	if binding.resolved {
		session.mu.Unlock()
		return adapter.ErrInteractionResolved
	}
	if resolution.RespondedBy != binding.respondedBy {
		session.mu.Unlock()
		return adapter.ErrWrongResponder
	}
	run := session.runs[binding.runID]
	if run == nil || run.terminal {
		session.mu.Unlock()
		return adapter.ErrInteractionResolved
	}
	session.mu.Unlock()

	if binding.kind == permissionInteraction {
		request := resolution.Permission
		if request == nil || request.SessionID != session.state.SessionID || request.RunID != run.id || request.RequestedBy != binding.requestedBy || request.RespondedBy != binding.respondedBy || len(request.UpdatedArgumentsJSON) != 0 {
			return adapter.ErrInvalidResolution
		}
		granted, outcome, valid := permissionDecision(request.ChoiceID, binding.permissionChoices)
		if !valid || request.Granted != granted {
			return adapter.ErrInvalidResolution
		}
		if err := binding.request.Respond(ctx, native.ApprovalResponse{Decision: native.ApprovalDecision(request.ChoiceID)}); err != nil {
			return err
		}
		session.mu.Lock()
		binding.resolved = true
		session.mu.Unlock()
		payload := protocol.PermissionResolvedPayload{InteractionID: id, RequestedBy: binding.requestedBy, RespondedBy: binding.respondedBy, SessionID: session.state.SessionID, RunID: run.id, ToolCallID: binding.toolCallID, Outcome: outcome, ChoiceID: request.ChoiceID, Granted: &granted}
		return session.emit(run, protocol.TypeActionPermissionResolved, payload, false)
	}

	request := resolution.Input
	if request == nil || request.SessionID != session.state.SessionID || request.RunID != run.id || request.RequestedBy != binding.requestedBy || request.RespondedBy != binding.respondedBy {
		return adapter.ErrInvalidResolution
	}
	answers, err := nativeAnswers(binding, request.Answers)
	if err != nil {
		return err
	}
	if err := binding.request.Respond(ctx, native.UserInputResponse{Answers: answers}); err != nil {
		return err
	}
	session.mu.Lock()
	binding.resolved = true
	run.status = protocol.RunRunning
	session.state.Status = protocol.SessionRunning
	session.state.UpdatedAtMS = session.clock.Now().UnixMilli()
	session.mu.Unlock()
	payload := protocol.UserInputResolvedPayload{InteractionID: id, RequestedBy: binding.requestedBy, RespondedBy: binding.respondedBy, SessionID: session.state.SessionID, RunID: run.id, Status: protocol.InputSubmitted, Answers: request.Answers}
	if err := session.emit(run, protocol.TypeUserInputResolved, payload, false); err != nil {
		return err
	}
	return session.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: session.state.SessionID, RunID: run.id, Status: protocol.RunRunning, UpdatedAtMS: session.clock.Now().UnixMilli()}, false)
}

func permissionDecision(choiceID string, available map[string]bool) (bool, protocol.InteractionOutcome, bool) {
	if !available[choiceID] {
		return false, "", false
	}
	switch choiceID {
	case "accept", "acceptForSession":
		return true, protocol.InteractionResolved, true
	case "decline":
		return false, protocol.InteractionRejected, true
	case "cancel":
		return false, protocol.InteractionCancelled, true
	default:
		return false, "", false
	}
}

func nativeAnswers(binding *interactionBinding, input []protocol.InputAnswer) (map[string]native.UserInputAnswer, error) {
	indexed, err := adapter.IndexInputAnswers(binding.questions, input)
	if err != nil {
		return nil, adapter.ErrInvalidResolution
	}
	// Codex requires every surfaced question to be answered.
	if len(indexed) != len(binding.questions) {
		return nil, adapter.ErrInvalidResolution
	}
	answers := make(map[string]native.UserInputAnswer, len(input))
	for _, question := range binding.questions {
		answer := indexed[question.ID]
		if len(question.Options) == 0 {
			answers[question.ID] = native.UserInputAnswer{Answers: []string{answer.Text}}
			continue
		}
		label, exists := binding.optionLabels[question.ID][answer.SelectedOptionIDs[0]]
		if !exists {
			return nil, adapter.ErrInvalidResolution
		}
		answers[question.ID] = native.UserInputAnswer{Answers: []string{label}}
	}
	return answers, nil
}

func (session *session) Cancel(ctx context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocol.RunCancelResponse{}, err
	}
	for {
		session.opMu.Lock()
		session.mu.Lock()
		run := session.runs[runID]
		if run == nil {
			session.mu.Unlock()
			session.opMu.Unlock()
			return protocol.RunCancelResponse{}, adapter.ErrRunNotFound
		}
		if run.terminal {
			status := run.status
			session.mu.Unlock()
			session.opMu.Unlock()
			if status == protocol.RunCancelled {
				return protocol.RunCancelResponse{SessionID: session.state.SessionID, RunID: runID, Accepted: true, Status: status}, nil
			}
			return protocol.RunCancelResponse{}, &adapter.RunTerminalError{RunID: runID, Status: status}
		}
		if run.cancelPending {
			session.mu.Unlock()
			session.opMu.Unlock()
			return protocol.RunCancelResponse{SessionID: session.state.SessionID, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
		}
		if inFlight := run.cancelInFlight; inFlight != nil {
			session.mu.Unlock()
			session.opMu.Unlock()
			select {
			case <-inFlight:
				continue
			case <-ctx.Done():
				return protocol.RunCancelResponse{}, ctx.Err()
			}
		}
		run.cancelInFlight = make(chan struct{})
		inFlight := run.cancelInFlight
		turnID := run.turnID
		session.mu.Unlock()
		session.opMu.Unlock()

		err := session.client.Call(ctx, native.MethodTurnInterrupt, native.TurnInterruptParams{ThreadID: session.threadID, TurnID: turnID}, &native.TurnInterruptResponse{})

		session.opMu.Lock()
		session.mu.Lock()
		close(inFlight)
		run.cancelInFlight = nil
		terminal, status := run.terminal, run.status
		if err == nil && !terminal {
			run.cancelPending = true
			run.status = protocol.RunCancelling
		}
		session.mu.Unlock()
		if err == nil && !terminal {
			_ = session.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: session.state.SessionID, RunID: run.id, Status: protocol.RunCancelling, UpdatedAtMS: session.clock.Now().UnixMilli()}, false)
		}
		session.opMu.Unlock()
		if err != nil {
			return protocol.RunCancelResponse{}, fmt.Errorf("interrupt Codex turn: %w", err)
		}
		if terminal {
			if status == protocol.RunCancelled {
				return protocol.RunCancelResponse{SessionID: session.state.SessionID, RunID: runID, Accepted: true, Status: status}, nil
			}
			return protocol.RunCancelResponse{}, &adapter.RunTerminalError{RunID: runID, Status: status}
		}
		return protocol.RunCancelResponse{SessionID: session.state.SessionID, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
	}
}

func (session *session) Resume(ctx context.Context, request adapter.ResumeRequest) (adapter.Recovery, adapter.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return adapter.Recovery{}, nil, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	run := session.runs[request.RunID]
	if run == nil {
		return adapter.Recovery{}, nil, adapter.ErrRunNotFound
	}
	latest := run.nextSequence - 1
	if request.AfterSequence > latest {
		return adapter.Recovery{}, nil, adapter.ErrReplayCursorFuture
	}
	var oldest uint64
	var suffix []protocol.Envelope
	for _, envelope := range session.journal {
		if envelope.RunID != run.id || envelope.Sequence == nil {
			continue
		}
		if oldest == 0 {
			oldest = *envelope.Sequence
		}
		if *envelope.Sequence > request.AfterSequence {
			suffix = append(suffix, envelope)
		}
	}
	recovery := adapter.Recovery{State: session.state, RunID: run.id, RequestedAfter: request.AfterSequence, ReplayedFrom: request.AfterSequence, ReplayedThrough: request.AfterSequence}
	stream := make(chan adapter.Result, len(suffix)+33)
	if request.AfterSequence < latest && (oldest == 0 || request.AfterSequence+1 < oldest) {
		recovery.ReplayGap = &adapter.ReplayGap{RequestedAfter: request.AfterSequence, OldestAvailable: oldest, LatestAvailable: latest}
		close(stream)
		return recovery, stream, recovery.ReplayGap
	}
	if len(suffix) > 0 {
		recovery.ReplayedFrom = *suffix[0].Sequence
		recovery.ReplayedThrough = *suffix[len(suffix)-1].Sequence
		for _, envelope := range suffix {
			stream <- adapter.Result{Envelope: cloneEnvelope(envelope)}
		}
	}
	if run.terminal {
		close(stream)
	} else {
		run.subscribers = append(run.subscribers, &subscriber{stream: stream})
	}
	return recovery, stream, nil
}

func (session *session) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	session.opMu.Lock()
	defer session.opMu.Unlock()
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	if session.active != nil && !session.active.terminal {
		session.mu.Unlock()
		return adapter.ErrRunActive
	}
	session.closed = true
	session.state.Status = protocol.SessionClosed
	session.state.ActiveRunID = ""
	close(session.stop)
	session.mu.Unlock()
	return session.client.Close()
}

func (session *session) dispatch() {
	for {
		select {
		case notification := <-session.client.Notifications():
			session.handleNotification(notification)
		case request := <-session.client.Requests():
			if request != nil {
				session.handleRequest(request)
			}
		case <-session.client.Done():
			session.opMu.Lock()
			session.failActive("native_transport_closed", errorString(session.client.Err()))
			session.opMu.Unlock()
			return
		case <-session.stop:
			return
		}
	}
}

func (session *session) handleRequest(request *rpc.IncomingRequest) {
	session.opMu.Lock()
	defer session.opMu.Unlock()
	switch request.Method {
	case native.MethodCommandApproval, native.MethodFileApproval:
		threadID, turnID, itemID, reason, decisions, err := approvalScope(request.Method, request.Params)
		if err != nil || threadID != session.threadID {
			_ = request.RespondError(context.Background(), -32602, "invalid approval request scope", nil)
			return
		}
		run := session.runFor(threadID, turnID)
		if run == nil || !session.prepareEvent(run) {
			_ = request.RespondError(context.Background(), -32602, "approval request does not target the active run", nil)
			return
		}
		session.mu.Lock()
		item, exists := session.items[itemID]
		if !exists || item.runID != run.id || !item.started || item.terminal {
			session.mu.Unlock()
			_ = request.RespondError(context.Background(), -32602, "approval request does not target an active action", nil)
			return
		}
		interactionID := protocol.InteractionID(session.ids.NewID("interaction"))
		binding := &interactionBinding{kind: permissionInteraction, runID: run.id, toolCallID: item.toolCallID, requestedBy: "agent", respondedBy: session.participant, request: request, permissionChoices: decisions}
		session.interactions[interactionID] = binding
		session.mu.Unlock()
		title := "Allow Codex action"
		if request.Method == native.MethodFileApproval {
			title = "Allow Codex file changes"
		}
		payload := protocol.PermissionRequestedPayload{InteractionID: interactionID, RequestedBy: binding.requestedBy, RespondedBy: binding.respondedBy, SessionID: session.state.SessionID, RunID: run.id, ToolCallID: binding.toolCallID, Title: title, Description: reason, Choices: permissionChoices(decisions), ArgumentsJSON: item.arguments}
		if err := session.emit(run, protocol.TypeActionPermissionRequested, payload, false); err != nil {
			session.mu.Lock()
			delete(session.interactions, interactionID)
			session.mu.Unlock()
			_ = request.RespondError(context.Background(), -32603, "failed to expose approval request", nil)
		}
	case native.MethodUserInput:
		var params native.UserInputRequestParams
		if json.Unmarshal(request.Params, &params) != nil || params.ThreadID != session.threadID || params.TurnID == "" || len(params.Questions) == 0 {
			_ = request.RespondError(context.Background(), -32602, "invalid user input request", nil)
			return
		}
		run := session.runFor(params.ThreadID, params.TurnID)
		if run == nil || !session.prepareEvent(run) {
			_ = request.RespondError(context.Background(), -32602, "user input does not target the active run", nil)
			return
		}
		questions := make([]protocol.InputQuestion, 0, len(params.Questions))
		optionLabels := make(map[string]map[string]string, len(params.Questions))
		seenQuestions := make(map[string]struct{}, len(params.Questions))
		for _, question := range params.Questions {
			if question.ID == "" || question.Question == "" || question.IsSecret {
				_ = request.RespondError(context.Background(), -32602, "invalid user input question", nil)
				return
			}
			if _, duplicate := seenQuestions[question.ID]; duplicate {
				_ = request.RespondError(context.Background(), -32602, "duplicate user input question id", nil)
				return
			}
			seenQuestions[question.ID] = struct{}{}
			kind := protocol.InputText
			var options []protocol.InputOption
			if question.Options != nil && len(*question.Options) > 0 {
				kind = protocol.InputSingleChoice
				labels := make(map[string]string, len(*question.Options))
				options = make([]protocol.InputOption, 0, len(*question.Options))
				for index, option := range *question.Options {
					optionID := fmt.Sprintf("option-%d", index+1)
					options = append(options, protocol.InputOption{ID: optionID, Label: option.Label, Description: option.Description})
					labels[optionID] = option.Label
				}
				// Codex's isOther permits an option plus custom text, but the OAP
				// answer oneOf allows either selected options or text, never both.
				// The custom-answer capability cannot be represented, so no synthetic
				// option is advertised; a text-bearing option answer is refused
				// rather than projected schema-invalid.
				optionLabels[question.ID] = labels
			}
			questions = append(questions, protocol.InputQuestion{ID: question.ID, Prompt: question.Question, Kind: kind, Required: true, Options: options})
		}
		session.mu.Lock()
		interactionID := protocol.InteractionID(session.ids.NewID("interaction"))
		binding := &interactionBinding{kind: inputInteraction, runID: run.id, toolCallID: protocol.ToolCallID(params.ItemID), requestedBy: "agent", respondedBy: session.participant, request: request, questions: questions, optionLabels: optionLabels}
		session.interactions[interactionID] = binding
		run.status = protocol.RunWaitingForInput
		session.state.Status = protocol.SessionWaitingForInput
		session.state.UpdatedAtMS = session.clock.Now().UnixMilli()
		session.mu.Unlock()
		payload := protocol.UserInputRequestedPayload{InteractionID: interactionID, RequestedBy: binding.requestedBy, RespondedBy: binding.respondedBy, SessionID: session.state.SessionID, RunID: run.id, Title: "Codex needs input", Questions: questions}
		if err := session.emit(run, protocol.TypeUserInputRequested, payload, false); err != nil {
			session.mu.Lock()
			delete(session.interactions, interactionID)
			session.mu.Unlock()
			_ = request.RespondError(context.Background(), -32603, "failed to expose user input request", nil)
			return
		}
		_ = session.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: session.state.SessionID, RunID: run.id, Status: protocol.RunWaitingForInput, PendingUserInputID: interactionID, UpdatedAtMS: session.clock.Now().UnixMilli()}, false)
	default:
		_ = request.RespondError(context.Background(), -32601, "unsupported Codex server request", map[string]string{"method": request.Method})
	}
}

func approvalScope(method string, raw json.RawMessage) (threadID, turnID, itemID, reason string, decisions map[string]bool, err error) {
	decisions = standardApprovalDecisions()
	switch method {
	case native.MethodCommandApproval:
		var params native.CommandApprovalParams
		if decodeErr := json.Unmarshal(raw, &params); decodeErr != nil || params.ThreadID == "" || params.TurnID == "" || params.ItemID == "" {
			return "", "", "", "", nil, adapter.ErrInvalidResolution
		}
		if params.Reason != nil {
			reason = *params.Reason
		}
		if len(params.AvailableDecisions) != 0 && string(params.AvailableDecisions) != "null" {
			var available []native.ApprovalDecision
			if decodeErr := json.Unmarshal(params.AvailableDecisions, &available); decodeErr != nil {
				return "", "", "", "", nil, adapter.ErrInvalidResolution
			}
			decisions = make(map[string]bool, len(available))
			for _, decision := range available {
				if _, _, valid := permissionDecision(string(decision), standardApprovalDecisions()); !valid {
					return "", "", "", "", nil, adapter.ErrInvalidResolution
				}
				decisions[string(decision)] = true
			}
			if len(decisions) == 0 {
				return "", "", "", "", nil, adapter.ErrInvalidResolution
			}
		}
		return params.ThreadID, params.TurnID, params.ItemID, reason, decisions, nil
	case native.MethodFileApproval:
		var params native.FileApprovalParams
		if decodeErr := json.Unmarshal(raw, &params); decodeErr != nil || params.ThreadID == "" || params.TurnID == "" || params.ItemID == "" {
			return "", "", "", "", nil, adapter.ErrInvalidResolution
		}
		if params.Reason != nil {
			reason = *params.Reason
		}
		return params.ThreadID, params.TurnID, params.ItemID, reason, decisions, nil
	default:
		return "", "", "", "", nil, adapter.ErrInvalidResolution
	}
}

func standardApprovalDecisions() map[string]bool {
	return map[string]bool{"accept": true, "acceptForSession": true, "decline": true, "cancel": true}
}

func permissionChoices(decisions map[string]bool) []protocol.PermissionChoice {
	definitions := []protocol.PermissionChoice{
		{ID: "accept", Label: "Approve once"},
		{ID: "acceptForSession", Label: "Approve for session"},
		{ID: "decline", Label: "Deny"},
		{ID: "cancel", Label: "Deny and cancel run"},
	}
	choices := make([]protocol.PermissionChoice, 0, len(decisions))
	for _, choice := range definitions {
		if decisions[choice.ID] {
			choices = append(choices, choice)
		}
	}
	return choices
}

func (session *session) handleNotification(notification rpc.NotificationMessage) {
	session.opMu.Lock()
	defer session.opMu.Unlock()
	switch notification.Method {
	case native.MethodTurnStarted:
		var value native.TurnStartedNotification
		if json.Unmarshal(notification.Params, &value) != nil {
			session.failActive("invalid_native_event", "invalid turn/started payload")
			return
		}
		session.onStarted(value)
	case native.MethodAgentDelta:
		var value native.AgentMessageDeltaNotification
		if json.Unmarshal(notification.Params, &value) != nil {
			session.failActive("invalid_native_event", "invalid agent message delta")
			return
		}
		session.onDelta(value)
	case native.MethodItemStarted, native.MethodItemCompleted:
		var value native.ItemNotification
		if json.Unmarshal(notification.Params, &value) != nil {
			session.failActive("invalid_native_event", "invalid item lifecycle payload")
			return
		}
		session.onItem(notification.Method, value)
	case native.MethodTurnCompleted:
		var value native.TurnCompletedNotification
		if json.Unmarshal(notification.Params, &value) != nil {
			session.failActive("invalid_native_event", "invalid turn/completed payload")
			return
		}
		session.onCompleted(value)
	}
}

func (session *session) runFor(threadID, turnID string) *runState {
	if threadID != session.threadID {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.runs[session.turns[turnID]]
}

func (session *session) prepareEvent(run *runState) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return !run.terminal
}

func (session *session) onStarted(value native.TurnStartedNotification) {
	run := session.runFor(value.ThreadID, value.Turn.ID)
	if run == nil {
		return
	}
	session.mu.Lock()
	if run.started || run.terminal {
		session.mu.Unlock()
		return
	}
	run.started = true
	run.status = protocol.RunRunning
	session.state.Status = protocol.SessionRunning
	session.state.UpdatedAtMS = session.clock.Now().UnixMilli()
	session.mu.Unlock()
	_ = session.emit(run, protocol.TypeRunStarted, protocol.RunStartedPayload{SessionID: session.state.SessionID, RunID: run.id, Status: protocol.RunRunning, ModelID: session.state.CurrentModelID, StartedAtMS: session.clock.Now().UnixMilli()}, false)
}

func (session *session) onDelta(value native.AgentMessageDeltaNotification) {
	run := session.runFor(value.ThreadID, value.TurnID)
	if run == nil || !run.started || run.terminal {
		return
	}
	session.mu.Lock()
	run.text += value.Delta
	session.mu.Unlock()
	_ = session.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: session.state.SessionID, RunID: run.id, MessageID: run.messageID, Part: protocol.ContentPart{Type: protocol.ContentText, Text: value.Delta}}, false)
}

func (session *session) onItem(method string, value native.ItemNotification) {
	run := session.runFor(value.ThreadID, value.TurnID)
	if run == nil || !run.started || run.terminal {
		return
	}
	name := itemName(value.Item)
	if name == "" || value.Item.ID == "" {
		return
	}
	session.mu.Lock()
	binding, exists := session.items[value.Item.ID]
	if method == native.MethodItemStarted {
		if exists || run.terminal {
			session.mu.Unlock()
			return
		}
		binding = itemBinding{runID: run.id, toolCallID: protocol.ToolCallID(session.ids.NewID("tool-call")), name: name, arguments: itemArguments(value.Item), started: true}
		session.items[value.Item.ID] = binding
		session.mu.Unlock()
		payload := actionPayload(session.state.SessionID, run.id, binding)
		_ = session.emit(run, protocol.TypeActionCallRequested, payload, false)
		_ = session.emit(run, protocol.TypeActionCallStarted, payload, false)
		return
	}
	if !exists || binding.runID != run.id || binding.terminal {
		session.mu.Unlock()
		session.failRun(run, "invalid_native_action", "item/completed arrived without one active item/started")
		return
	}
	binding.terminal = true
	session.items[value.Item.ID] = binding
	session.mu.Unlock()
	payload := actionTerminalPayload(session.state.SessionID, run.id, binding)
	if value.Item.Error != nil || value.Item.Status == "failed" {
		message := "native action failed"
		if value.Item.Error != nil && value.Item.Error.Message != "" {
			message = value.Item.Error.Message
		}
		payload.Error = &protocol.ProtocolError{Code: "native_action_failed", Message: message}
		_ = session.emit(run, protocol.TypeActionCallFailed, payload, false)
		return
	}
	payload.Result = value.Item.Result
	if len(payload.Result) == 0 {
		output := ""
		if value.Item.Output != nil {
			output = *value.Item.Output
		}
		payload.Result, _ = json.Marshal(map[string]string{"output": output})
	}
	_ = session.emit(run, protocol.TypeActionCallCompleted, payload, false)
}

func actionPayload(sessionID protocol.SessionID, runID protocol.RunID, binding itemBinding) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{
		SessionID: sessionID, RunID: runID, ToolCallID: binding.toolCallID,
		Name: binding.name, ArgumentsJSON: binding.arguments,
		RequestedBy: "agent", ExecutionOwner: "codex.app-server",
	}
}

func actionTerminalPayload(sessionID protocol.SessionID, runID protocol.RunID, binding itemBinding) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{
		SessionID: sessionID, RunID: runID, ToolCallID: binding.toolCallID,
		Name: binding.name, RequestedBy: "agent", ExecutionOwner: "codex.app-server",
	}
}

func itemArguments(item native.Item) json.RawMessage {
	if len(item.Arguments) != 0 {
		return append(json.RawMessage(nil), item.Arguments...)
	}
	var value any
	switch item.Type {
	case "commandExecution":
		value = map[string]string{"command": item.Command, "cwd": item.Cwd}
	case "fileChange":
		value = map[string]any{"changes": item.Changes}
	default:
		value = map[string]string{"server": item.Server, "tool": item.Tool}
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

func itemName(item native.Item) string {
	switch item.Type {
	case "commandExecution":
		return "codex.command"
	case "fileChange":
		return "codex.file_change"
	case "mcpToolCall":
		if item.Server != "" || item.Tool != "" {
			return "mcp." + item.Server + "." + item.Tool
		}
		return "mcp.tool"
	default:
		return ""
	}
}

func (session *session) onCompleted(value native.TurnCompletedNotification) {
	run := session.runFor(value.ThreadID, value.Turn.ID)
	if run == nil || !session.prepareEvent(run) {
		return
	}
	session.closePendingInteractions(run, value.Turn.Status)
	session.closePendingActions(run, value.Turn.Status)
	switch value.Turn.Status {
	case native.TurnCompleted:
		session.mu.Lock()
		text := run.text
		session.mu.Unlock()
		payload := protocol.RunCompletedPayload{SessionID: session.state.SessionID, RunID: run.id, FinalResponse: protocol.Message{ID: run.messageID, Role: protocol.RoleAssistant, Content: protocol.TextContent(text)}, StopReason: "end_turn"}
		_ = session.emit(run, protocol.TypeRunCompleted, payload, true)
	case native.TurnInterrupted:
		_ = session.emit(run, protocol.TypeRunCancelled, protocol.RunCancelledPayload{SessionID: session.state.SessionID, RunID: run.id, Reason: "native turn interrupted"}, true)
	case native.TurnFailed:
		message := "native turn failed"
		code := "native_turn_failed"
		if value.Turn.Error != nil && value.Turn.Error.Message != "" {
			message = value.Turn.Error.Message
		}
		_ = session.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: session.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}}, true)
	default:
		session.failRun(run, "invalid_native_terminal", "turn/completed did not contain a terminal status")
	}
}

func (session *session) closePendingInteractions(run *runState, status native.TurnStatus) {
	session.mu.Lock()
	type pendingInteraction struct {
		id      protocol.InteractionID
		binding *interactionBinding
	}
	var pending []pendingInteraction
	for id, binding := range session.interactions {
		if binding.runID == run.id && !binding.resolved {
			binding.resolved = true
			pending = append(pending, pendingInteraction{id: id, binding: binding})
		}
	}
	session.mu.Unlock()
	for _, entry := range pending {
		_ = entry.binding.request.RespondError(context.Background(), -32800, "OAP run terminated before interaction resolution", nil)
		if entry.binding.kind == permissionInteraction {
			outcome := protocol.InteractionFailed
			if status == native.TurnInterrupted {
				outcome = protocol.InteractionCancelled
			}
			reason := protocol.ProtocolError{Code: "run_terminated", Message: "run terminated before permission resolution"}
			_ = session.emit(run, protocol.TypeActionPermissionResolved, protocol.PermissionResolvedPayload{InteractionID: entry.id, RequestedBy: entry.binding.requestedBy, RespondedBy: entry.binding.respondedBy, SessionID: session.state.SessionID, RunID: run.id, ToolCallID: entry.binding.toolCallID, Outcome: outcome, Reason: &reason}, false)
		} else {
			_ = session.emit(run, protocol.TypeUserInputResolved, protocol.UserInputResolvedPayload{InteractionID: entry.id, RequestedBy: entry.binding.requestedBy, RespondedBy: entry.binding.respondedBy, SessionID: session.state.SessionID, RunID: run.id, Status: protocol.InputCancelled}, false)
		}
	}
}

func (session *session) closePendingActions(run *runState, status native.TurnStatus) {
	session.mu.Lock()
	var pending []itemBinding
	for id, binding := range session.items {
		if binding.runID == run.id && binding.started && !binding.terminal {
			binding.terminal = true
			session.items[id] = binding
			pending = append(pending, binding)
		}
	}
	session.mu.Unlock()
	for _, binding := range pending {
		payload := actionTerminalPayload(session.state.SessionID, run.id, binding)
		if status == native.TurnInterrupted {
			_ = session.emit(run, protocol.TypeActionCallCancelled, payload, false)
			continue
		}
		payload.Error = &protocol.ProtocolError{Code: "native_action_incomplete", Message: "native turn ended before the action completed"}
		_ = session.emit(run, protocol.TypeActionCallFailed, payload, false)
	}
}

func (session *session) failActive(code, message string) {
	session.mu.Lock()
	run := session.active
	session.mu.Unlock()
	if run != nil {
		session.failRun(run, code, message)
	}
}

func (session *session) failRun(run *runState, code, message string) {
	if message == "" {
		message = code
	}
	session.closePendingInteractions(run, native.TurnFailed)
	session.closePendingActions(run, native.TurnFailed)
	_ = session.emit(run, protocol.TypeRunFailed, protocol.RunFailedPayload{SessionID: session.state.SessionID, RunID: run.id, Error: protocol.ProtocolError{Code: code, Message: message}}, true)
}

func errorString(err error) string {
	if err == nil {
		return "Codex app-server transport closed before terminal settlement"
	}
	return err.Error()
}

// cloneEnvelope detaches an envelope handed to a consumer from the retained
// journal: the payload slice and the sequence/timestamp pointers must not be
// shared, or a consumer's edit would corrupt replayed history.
func cloneEnvelope(envelope protocol.Envelope) protocol.Envelope {
	cloned := envelope
	if envelope.Payload != nil {
		cloned.Payload = append(json.RawMessage(nil), envelope.Payload...)
	}
	if envelope.Sequence != nil {
		sequence := *envelope.Sequence
		cloned.Sequence = &sequence
	}
	if envelope.TimestampMS != nil {
		timestamp := *envelope.TimestampMS
		cloned.TimestampMS = &timestamp
	}
	return cloned
}

func (session *session) emit(run *runState, typ protocol.EnvelopeType, payload any, terminal bool) error {
	session.emitMu.Lock()
	defer session.emitMu.Unlock()
	session.mu.Lock()
	if run.terminal {
		session.mu.Unlock()
		return errors.New("codex app-server adapter: run is terminal")
	}
	sequence := run.nextSequence
	run.nextSequence++
	session.mu.Unlock()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(session.ids.NewID("event")), payload)
	if err != nil {
		return err
	}
	now := session.clock.Now().UnixMilli()
	envelope.SessionID = session.state.SessionID
	envelope.RunID = run.id
	if toolPayload, ok := payload.(protocol.ActionCallPayload); ok {
		envelope.ToolCallID = toolPayload.ToolCallID
	}
	switch interactionPayload := payload.(type) {
	case protocol.PermissionRequestedPayload:
		envelope.ToolCallID = interactionPayload.ToolCallID
	case protocol.PermissionResolvedPayload:
		envelope.ToolCallID = interactionPayload.ToolCallID
	}
	envelope.Sequence = &sequence
	envelope.TimestampMS = &now
	envelope.CapabilityRevision = CapabilityRevision

	session.mu.Lock()
	session.journal = append(session.journal, envelope)
	if len(session.journal) > session.capacity {
		session.journal = append([]protocol.Envelope(nil), session.journal[len(session.journal)-session.capacity:]...)
	}
	subscribers := append([]*subscriber(nil), run.subscribers...)
	if terminal {
		run.terminal = true
		run.status = terminalStatus(typ)
		session.state.Status = protocol.SessionIdle
		session.state.ActiveRunID = ""
		session.state.TranscriptCursor = strconv.FormatUint(sequence, 10)
		session.state.UpdatedAtMS = now
		if session.active == run {
			session.active = nil
		}
		run.subscribers = nil
	}
	session.mu.Unlock()
	for _, subscriber := range subscribers {
		if subscriber.detached {
			continue
		}
		if len(subscriber.stream) < cap(subscriber.stream)-1 {
			subscriber.stream <- adapter.Result{Envelope: cloneEnvelope(envelope)}
			if terminal {
				subscriber.detached = true
				close(subscriber.stream)
			}
			continue
		}
		// Every stream reserves one slot for this ordered detach signal. The
		// consumer receives a contiguous prefix, then an explicit instruction to
		// resume rather than a silent sequence gap or false normal close.
		subscriber.stream <- adapter.Result{Error: adapter.ErrEventStreamOverflow}
		subscriber.detached = true
		close(subscriber.stream)
	}
	return nil
}

func terminalStatus(typ protocol.EnvelopeType) protocol.RunStatus {
	switch typ {
	case protocol.TypeRunCompleted:
		return protocol.RunCompleted
	case protocol.TypeRunCancelled:
		return protocol.RunCancelled
	default:
		return protocol.RunFailed
	}
}

var _ adapter.Session = (*session)(nil)
