package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/validation"
)

// Check is one named obligation of the binding and whether the endpoint met
// it. Naming them individually is the point: "not conformant" is not
// actionable, and an implementer needs to know which obligation failed.
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
	// Skipped marks an obligation this endpoint does not carry, as opposed to
	// one it failed. The distinction is the difference between a report that
	// is actionable and one that reads as a failure for declining something
	// optional — cursor replay is not among the Core Profile Requirements,
	// and an endpoint that answers unsupported_control is doing what the
	// binding tells it to.
	Skipped bool `json:"skipped,omitempty"`
}

// Report is the outcome of one conformance run.
type Report struct {
	Checks      []Check                 `json:"checks"`
	Diagnostics []validation.Diagnostic `json:"diagnostics,omitempty"`
	Trace       []protocol.Envelope     `json:"-"`
	Passed      bool                    `json:"passed"`
}

// Options configures one run.
type Options struct {
	Command   []string
	SessionID protocol.SessionID
	Stderr    io.Writer
	// LineDeadline bounds the wait for each line the endpoint writes. Zero
	// takes DefaultLineDeadline.
	LineDeadline time.Duration
	// Model is the model id the scripted submission names. Empty lets the
	// endpoint's own catalog decide, and falls back to naming none.
	Model string
}

type runner struct {
	client   *Client
	report   *Report
	session  protocol.SessionID
	revision string
	runID    protocol.RunID
	ids      int
	// descriptor is what the endpoint said about itself. The script reads it
	// rather than assuming: an obligation that is conditional on a declared
	// capability cannot be judged without one.
	descriptor protocol.CapabilityDescriptor
	// model is the operator's chosen model id, or "" to let the catalog
	// decide. See electModel.
	model string
}

func (r *runner) next(kind string) protocol.EnvelopeID {
	r.ids++
	return protocol.EnvelopeID(fmt.Sprintf("conformance-%s-%d", kind, r.ids))
}

func (r *runner) pass(name string) {
	r.report.Checks = append(r.report.Checks, Check{Name: name, Passed: true})
}
func (r *runner) fail(name, detail string) {
	r.report.Checks = append(r.report.Checks, Check{Name: name, Detail: detail})
}

// skip records an obligation this endpoint does not carry. A skipped check
// never fails the report, and the detail says why it was not asked.
func (r *runner) skip(name, detail string) {
	r.report.Checks = append(r.report.Checks, Check{Name: name, Passed: true, Skipped: true, Detail: detail})
}

func (r *runner) record(name string, err error) bool {
	if err != nil {
		r.fail(name, err.Error())
		return false
	}
	r.pass(name)
	return true
}

// request sends one request envelope and waits for its correlated answer,
// refusing an answer that is an error.response where one was not expected.
func (r *runner) request(typ protocol.EnvelopeType, payload any, runID protocol.RunID, revision string) (protocol.Envelope, error) {
	envelope, err := protocol.NewEnvelope(typ, r.next("request"), payload)
	if err != nil {
		return protocol.Envelope{}, err
	}
	envelope.SessionID = r.session
	envelope.RunID = runID
	envelope.CapabilityRevision = revision
	if err := r.client.Send(envelope); err != nil {
		return protocol.Envelope{}, err
	}
	answer, err := r.client.Response(envelope.ID)
	if err != nil {
		return protocol.Envelope{}, err
	}
	if answer.Type == protocol.TypeErrorResponse {
		var failure protocol.ErrorResponse
		_ = answer.DecodePayload(&failure)
		return answer, fmt.Errorf("%s was refused %s: %s", typ, failure.Error.Code, failure.Error.Message)
	}
	return answer, nil
}

// Run drives the scripted session and returns the report.
//
// The script is the smallest one that exercises every obligation the binding
// states: discovery, a session, an admitted run, both scripted interaction
// gates answered from the stream, a terminal, reconciliation, and the exit
// contract. Everything it observes goes into one trace, and the validator
// decides whether that trace is legal OAP.
func Run(ctx context.Context, options Options) (*Report, error) {
	if len(options.Command) == 0 {
		return nil, fmt.Errorf("conformance: a command to run is required")
	}
	session := options.SessionID
	if session == "" {
		session = "conformance"
	}
	client, err := SpawnWithDeadline(ctx, options.Command[0], options.Command[1:], options.Stderr, options.LineDeadline)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	r := &runner{client: client, report: &Report{}, session: session, model: options.Model}

	r.drive()

	if err := client.CloseInput(); err != nil {
		r.fail("stdin close is accepted", err.Error())
	}
	code, waitErr := client.Wait()
	switch {
	case waitErr != nil:
		r.fail("endpoint exits after stdin EOF", waitErr.Error())
	case code != 0:
		r.fail("endpoint exits 0 after stdin EOF", fmt.Sprintf("exit code %d", code))
	default:
		r.pass("endpoint exits 0 after stdin EOF")
	}

	r.report.Trace = client.Transcript()
	r.validate()
	r.report.Checks = append(r.report.Checks, framingContract(ctx, options))

	r.report.Passed = true
	for _, check := range r.report.Checks {
		if !check.Passed {
			r.report.Passed = false
		}
	}
	return r.report, nil
}

// drive walks the script, stopping at the first step whose failure makes the
// rest meaningless — there is nothing to learn from submitting to a session
// that never opened.
func (r *runner) drive() {
	// Core requirement 2. It is first because it is the first thing a host
	// sends, and because an endpoint that cannot answer it has told a host
	// nothing about which version it speaks.
	if initialized, err := r.request(protocol.TypeProtocolInitializeRequest, protocol.InitializeRequest{
		Participant:      &protocol.Participant{ID: "conformance", Name: "OAP conformance runner"},
		ProtocolVersions: []string{protocol.Version},
		Profiles:         []string{protocol.Profile},
	}, "", ""); err != nil {
		r.fail("protocol.initialize.request is answered", err.Error())
	} else {
		var answer protocol.InitializeResponse
		if err := initialized.DecodePayload(&answer); err != nil {
			r.fail("protocol.initialize.response decodes", err.Error())
		} else {
			r.pass("protocol.initialize.request is answered")
		}
	}

	capabilities, err := r.request(protocol.TypeCapabilitiesRequest, protocol.CapabilitiesRequest{}, "", "")
	if !r.record("capabilities.request is answered", err) {
		return
	}
	if err := capabilities.DecodePayload(&r.descriptor); err != nil {
		r.fail("capabilities.response decodes as a descriptor", err.Error())
		return
	}
	r.revision = capabilities.CapabilityRevision
	if r.revision == "" {
		r.fail("capabilities.response carries a capability revision", "the response set no capability_revision, so nothing can be bound to this descriptor")
	} else {
		r.pass("capabilities.response carries a capability revision")
	}

	opened, err := r.request(protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: r.session}, "", r.revision)
	if !r.record("session.open.request is answered", err) {
		return
	}
	var state protocol.SessionState
	if err := opened.DecodePayload(&state); err != nil {
		r.fail("session.open.response decodes as a session state", err.Error())
		return
	}
	if state.SessionID != r.session {
		r.fail("the open names the session it was asked for", fmt.Sprintf("opened %q, asked for %q", state.SessionID, r.session))
	} else {
		r.pass("the open names the session it was asked for")
	}

	submission := protocol.MessageSubmitRequest{
		SessionID: r.session,
		Delivery:  protocol.DeliveryAuto,
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("drive one scripted run")}},
	}
	if model := r.electModel(); model != "" {
		submission.ModelID = protocol.ControlValue(model)
	}
	admitted, err := r.request(protocol.TypeSessionMessageSubmitRequest, submission, "", r.revision)
	if !r.record("session.message.submit.request is answered", err) {
		return
	}
	var admission protocol.MessageSubmitResponse
	if err := admitted.DecodePayload(&admission); err != nil {
		r.fail("the admission decodes", err.Error())
		return
	}
	if !admission.Accepted || admission.RunID == "" {
		r.fail("the submission is admitted and names its run", fmt.Sprintf("accepted=%v run=%q", admission.Accepted, admission.RunID))
		return
	}
	r.pass("the submission is admitted and names its run")
	r.runID = admission.RunID

	// Core requirement 6's second half, which the first half's check cannot
	// see: an accepted auto submission repeats requested_delivery: auto and
	// reports a concrete effective_delivery. Reporting auto back, or omitting
	// the field, leaves the host unable to tell whether its message started or
	// was queued — the one thing the response exists to say.
	const delivery = "the admission repeats requested_delivery and reports a concrete effective_delivery"
	switch {
	case admission.RequestedDelivery != protocol.DeliveryAuto:
		r.fail(delivery, fmt.Sprintf("requested_delivery came back %q, want %q", admission.RequestedDelivery, protocol.DeliveryAuto))
	case admission.EffectiveDelivery == "" || string(admission.EffectiveDelivery) == string(protocol.DeliveryAuto):
		r.fail(delivery, fmt.Sprintf("effective_delivery is %q; auto must resolve to a concrete mode", admission.EffectiveDelivery))
	default:
		r.pass(delivery)
	}

	r.consumeRun()

	r.replayRun()

	if _, err := r.request(protocol.TypeSessionStateRequest, protocol.SessionStateRequest{SessionID: r.session}, "", r.revision); err != nil {
		r.fail("session.state.request is answered after the run settles", err.Error())
	} else {
		r.pass("session.state.request is answered after the run settles")
	}

	r.refuseStaleRevision()
	r.answerCancel()
}

// refuseStaleRevision drives core requirement 13. A revision the endpoint
// never issued must be refused with the typed stale_capabilities code, because
// a host that reads a success there has bound its request to a descriptor
// snapshot the endpoint has moved past — the exact confusion the revision
// exists to prevent.
//
// The request is one every conformant endpoint answers when the revision is
// current, so a refusal here can only be about the revision.
func (r *runner) refuseStaleRevision() {
	const name = "a stale capability_revision is refused with stale_capabilities"
	if r.revision == "" {
		r.skip(name, "the endpoint issued no revision, so none can be stale")
		return
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeSessionStateRequest, r.next("request"), protocol.SessionStateRequest{SessionID: r.session})
	if err != nil {
		r.fail(name, err.Error())
		return
	}
	envelope.SessionID = r.session
	envelope.CapabilityRevision = r.revision + "-stale"
	if err := r.client.Probe(envelope); err != nil {
		r.fail(name, err.Error())
		return
	}
	answer, err := r.client.Response(envelope.ID)
	if err != nil {
		r.fail(name, err.Error())
		return
	}
	if answer.Type != protocol.TypeErrorResponse {
		r.fail(name, fmt.Sprintf("a request citing a revision this endpoint never issued was answered %s", answer.Type))
		return
	}
	var failure protocol.ErrorResponse
	if err := answer.DecodePayload(&failure); err != nil {
		r.fail(name, err.Error())
		return
	}
	if failure.Error.Code != "stale_capabilities" {
		r.fail(name, fmt.Sprintf("refused %q, want %q", failure.Error.Code, "stale_capabilities"))
		return
	}
	r.pass(name)
}

// answerCancel drives core requirement 10, which is a disjunction rather than
// a single obligation: an endpoint either supports run.cancel.request or
// declares cancellation unavailable and refuses the call with a typed
// unsupported-feature error. Which one applies is read from the descriptor,
// so an endpoint is judged against what it claimed rather than against what
// this runner would prefer.
//
// The run has already settled, so a supporting endpoint may legitimately
// refuse this particular cancel as too late. What is being checked is that
// the call is answered at all and that a declared-unavailable endpoint refuses
// it for the declared reason — silence is the failure either way.
func (r *runner) answerCancel() {
	const name = "run.cancel.request is supported, or refused as unavailable"
	if r.runID == "" {
		r.skip(name, "no run was admitted, so there is nothing to cancel")
		return
	}
	declared := r.descriptor.Features["run.cancel"]
	envelope, err := protocol.NewEnvelope(protocol.TypeRunCancelRequest, r.next("request"), protocol.RunCancelRequest{
		SessionID: r.session, RunID: r.runID,
	})
	if err != nil {
		r.fail(name, err.Error())
		return
	}
	envelope.SessionID = r.session
	envelope.RunID = r.runID
	envelope.CapabilityRevision = r.revision
	if err := r.client.Probe(envelope); err != nil {
		r.fail(name, err.Error())
		return
	}
	answer, err := r.client.Response(envelope.ID)
	if err != nil {
		r.fail(name, err.Error())
		return
	}
	unavailable := declared.Level == "" || declared.Level == protocol.SupportUnavailable
	if !unavailable {
		if answer.Type != protocol.TypeRunCancelResponse && answer.Type != protocol.TypeErrorResponse {
			r.fail(name, fmt.Sprintf("an endpoint declaring run.cancel %q answered %s", declared.Level, answer.Type))
			return
		}
		r.pass(name)
		return
	}
	if answer.Type != protocol.TypeErrorResponse {
		r.fail(name, "an endpoint declaring cancellation unavailable answered the call instead of refusing it")
		return
	}
	var failure protocol.ErrorResponse
	if err := answer.DecodePayload(&failure); err != nil {
		r.fail(name, err.Error())
		return
	}
	if failure.Error.Code != "unsupported_feature" {
		r.fail(name, fmt.Sprintf("refused %q, want the typed %q", failure.Error.Code, "unsupported_feature"))
		return
	}
	r.pass(name)
}

// electModel picks the model the scripted submission names, or "" to name
// none.
//
// An endpoint is entitled to have no default model, and one that does refuses
// a submission that names none — correctly, and at the first step that
// actually exercises the lifecycle. Requiring a default would be a rule in the
// harness that is in no document, and hard-coding an id would be the harness
// guessing at a catalog it has not read. So the catalog is what answers it:
// the operator's --model if given, otherwise the endpoint's own models.list
// when it advertises one, preferring the entry that declares itself default.
//
// An endpoint advertising no catalog and holding no default cannot be driven
// past submit by anyone, which is a fact about that endpoint rather than a
// failure this runner can attribute, and the refusal says so in its own words.
func (r *runner) electModel() string {
	if r.model != "" {
		return r.model
	}
	if support, ok := r.descriptor.Features[protocol.FeatureModelsList]; !ok || support.Level == "" || support.Level == protocol.SupportUnavailable {
		return ""
	}
	answer, err := r.request(protocol.TypeModelsRequest, protocol.ModelsRequest{SessionID: r.session}, "", r.revision)
	if err != nil {
		return ""
	}
	var catalog protocol.ModelsResponse
	if err := answer.DecodePayload(&catalog); err != nil || len(catalog.Models) == 0 {
		return ""
	}
	for _, model := range catalog.Models {
		if model.Default {
			return model.ID
		}
	}
	return catalog.Models[0].ID
}

// consumeRun reads the run's events, answering each scripted gate from what
// the gate itself offers rather than from anything this runner knows about a
// particular implementation: the first choice a permission lists, and the
// first option of each question an input asks. An endpoint whose gates differ
// is still driven correctly.
func (r *runner) consumeRun() {
	var lastSequence uint64
	for {
		event, err := r.client.Event()
		if err != nil {
			r.fail("the run reaches a terminal event", err.Error())
			return
		}
		if event.Sequence != nil {
			if *event.Sequence <= lastSequence {
				r.fail("run events carry an advancing per-run sequence",
					fmt.Sprintf("sequence %d did not advance past %d", *event.Sequence, lastSequence))
				return
			}
			lastSequence = *event.Sequence
		}
		switch event.Type {
		case protocol.TypeActionPermissionRequested:
			if err := r.answerPermission(event); err != nil {
				r.fail("a permission gate is resolvable from the stream", err.Error())
				return
			}
			r.pass("a permission gate is resolvable from the stream")
		case protocol.TypeUserInputRequested:
			if err := r.answerInput(event); err != nil {
				r.fail("a user input gate is resolvable from the stream", err.Error())
				return
			}
			r.pass("a user input gate is resolvable from the stream")
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			// Core requirement 9 is exactly one terminal, not a successful
			// one. Demanding run.completed would make conformance depend on
			// the endpoint having a working model and credentials for it,
			// which is not a protocol property: an endpoint that admits a
			// run and settles it as failed has done everything the lifecycle
			// asks. The terminal is named in the detail so a reader can still
			// see which one arrived.
			r.report.Checks = append(r.report.Checks, Check{
				Name: "the run reaches a terminal event", Passed: true,
				Detail: "settled " + string(event.Type),
			})
			return
		}
	}
}

func (r *runner) answerPermission(event protocol.Envelope) error {
	var gate protocol.PermissionRequestedPayload
	if err := event.DecodePayload(&gate); err != nil {
		return err
	}
	if len(gate.Choices) == 0 {
		return fmt.Errorf("the permission gate offers no choices, so it cannot be answered")
	}
	_, err := r.request(protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
		InteractionID: gate.InteractionID, RequestedBy: gate.RequestedBy, RespondedBy: gate.RespondedBy,
		SessionID: gate.SessionID, RunID: gate.RunID, ChoiceID: gate.Choices[0].ID, Granted: true,
	}, gate.RunID, r.revision)
	return err
}

func (r *runner) answerInput(event protocol.Envelope) error {
	var gate protocol.UserInputRequestedPayload
	if err := event.DecodePayload(&gate); err != nil {
		return err
	}
	answers := make([]protocol.InputAnswer, 0, len(gate.Questions))
	for _, question := range gate.Questions {
		answer := protocol.InputAnswer{QuestionID: question.ID}
		if len(question.Options) > 0 {
			answer.SelectedOptionIDs = []string{question.Options[0].ID}
		} else {
			answer.Text = "conformance"
		}
		answers = append(answers, answer)
	}
	_, err := r.request(protocol.TypeUserInputResolveRequest, protocol.UserInputResolveRequest{
		InteractionID: gate.InteractionID, RequestedBy: gate.RequestedBy, RespondedBy: gate.RespondedBy,
		SessionID: gate.SessionID, RunID: gate.RunID, Answers: answers,
	}, gate.RunID, r.revision)
	return err
}

// validate hands the assembled exchange to the real validator. This is the
// check the rest of the run exists to make possible.
func (r *runner) validate() {
	trace, err := json.Marshal(r.report.Trace)
	if err != nil {
		r.fail("the exchange validates as an OAP trace", err.Error())
		return
	}
	result := validation.MustNew().Validate(bytes.NewReader(trace), "endpoint-conformance")
	r.report.Diagnostics = result.Diagnostics
	if result.Valid() {
		r.pass("the exchange validates as an OAP trace")
		return
	}
	r.fail("the exchange validates as an OAP trace", fmt.Sprintf("%d diagnostic(s)", len(result.Diagnostics)))
}

// framingContract spawns a second endpoint and hands it a line that is not an
// OAP envelope.
//
// The binding makes this an exit code rather than a message, so a host piping
// an endpoint can tell a clean end from a framing fault without parsing
// stderr. It needs its own process because the fault is terminal by
// definition: an endpoint that kept reading after a broken frame would be
// guessing where the next one starts.
func framingContract(ctx context.Context, options Options) Check {
	const name = "a malformed line ends the endpoint non-zero"
	client, err := SpawnWithDeadline(ctx, options.Command[0], options.Command[1:], io.Discard, options.LineDeadline)
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
	defer client.Close()
	// A write failure here is not a test failure: an endpoint may already
	// have refused the frame and exited, which is the behaviour being
	// checked. The exit code is what decides.
	_, _ = client.stdin.Write([]byte("this is not an envelope\n"))
	_ = client.CloseInput()
	code, waitErr := client.Wait()
	switch {
	case waitErr != nil:
		return Check{Name: name, Detail: waitErr.Error()}
	case code == 0:
		return Check{Name: name, Detail: "the endpoint exited 0 after a line that is not an OAP envelope"}
	}
	return Check{Name: name, Passed: true}
}

// replayRun asks for the settled run again from its first event.
//
// Replaying after the terminal rather than mid-run is deliberate: it is the
// case with a known answer. The runner holds every envelope the run produced,
// so it can check that what comes back is the same run from its own
// beginning, which a mid-run replay racing live delivery could not pin.
//
// The cursor names its run. Sequences are per-run, so an unqualified cursor
// means something different once a newer run has been admitted, and a
// conformance runner should model the shape hosts ought to use.
func (r *runner) replayRun() {
	const accepted = "a cursor replay is accepted and re-delivers the run"
	if r.runID == "" {
		return
	}
	from := uint64(0)
	frame := ControlFrame{Control: "replay", ID: "conformance-replay-1", SessionID: r.session, RunID: r.runID, After: &from}
	if err := r.client.SendControl(frame); err != nil {
		r.fail(accepted, err.Error())
		return
	}
	answer, err := r.client.Control(frame.ID)
	if err != nil {
		r.fail(accepted, err.Error())
		return
	}
	switch answer.Control {
	case "replay.accepted":
	case "replay.gap":
		r.fail(accepted, fmt.Sprintf("the endpoint retains nothing at %d; its window is %d..%d",
			answer.RequestedAfter, answer.OldestAvailable, answer.LatestAvailable))
		return
	default:
		if answer.Code == "unsupported_control" {
			// The binding says an endpoint that does not recognise a control
			// answers it and keeps going, and cursor replay is not among the
			// Core Profile Requirements. Failing here would convict an
			// endpoint for doing exactly what the binding tells it to, and
			// would put a requirement in the harness that is in no document.
			// The answer itself is the thing worth checking, and it arrived.
			r.skip(accepted, "the endpoint does not implement the replay control, which the binding permits")
			return
		}
		r.fail(accepted, fmt.Sprintf("%s: %s", answer.Code, answer.Message))
		return
	}

	// The replayed stream ends at the run's terminal, exactly as the live one
	// did, so the terminal is what this waits for rather than a count.
	var first uint64
	for {
		event, err := r.client.Event()
		if err != nil {
			r.fail(accepted, err.Error())
			return
		}
		if event.RunID != r.runID {
			continue
		}
		if first == 0 && event.Sequence != nil {
			first = *event.Sequence
		}
		switch event.Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			if first != 1 {
				r.fail(accepted, fmt.Sprintf("a replay from 0 began at sequence %d, not the run's first event", first))
				return
			}
			r.pass(accepted)
			return
		}
	}
}
