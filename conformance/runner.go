package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

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
}

type runner struct {
	client   *Client
	report   *Report
	session  protocol.SessionID
	revision string
	runID    protocol.RunID
	ids      int
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
	client, err := Spawn(ctx, options.Command[0], options.Command[1:], options.Stderr)
	if err != nil {
		return nil, err
	}
	r := &runner{client: client, report: &Report{}, session: session}

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
	capabilities, err := r.request(protocol.TypeCapabilitiesRequest, protocol.CapabilitiesRequest{}, "", "")
	if !r.record("capabilities.request is answered", err) {
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

	admitted, err := r.request(protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: r.session,
		Delivery:  protocol.DeliveryAuto,
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("drive one scripted run")}},
	}, "", r.revision)
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

	r.consumeRun()

	r.replayRun()

	if _, err := r.request(protocol.TypeSessionStateRequest, protocol.SessionStateRequest{SessionID: r.session}, "", r.revision); err != nil {
		r.fail("session.state.request is answered after the run settles", err.Error())
	} else {
		r.pass("session.state.request is answered after the run settles")
	}
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
			if event.Type == protocol.TypeRunCompleted {
				r.pass("the run reaches a terminal event")
			} else {
				r.fail("the run reaches run.completed", fmt.Sprintf("the run settled %s", event.Type))
			}
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
	client, err := Spawn(ctx, options.Command[0], options.Command[1:], io.Discard)
	if err != nil {
		return Check{Name: name, Detail: err.Error()}
	}
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
