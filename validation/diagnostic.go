package validation

import (
	"fmt"
	"sort"
	"strings"
)

type Phase string

const (
	PhaseDecode   Phase = "decode"
	PhaseSchema   Phase = "schema"
	PhaseSemantic Phase = "semantic"
	// PhaseLoad is the phase of a fixture whose expectation is that loading a
	// resource — an extension pack — fails before any trace is read. It is
	// what a `load-invalid` manifest entry asserts.
	PhaseLoad Phase = "load"
)

const (
	CodeMalformedJSON                = "malformed_json"
	CodeDuplicateKey                 = "duplicate_key"
	CodeSchemaInvalid                = "schema_invalid"
	CodePayloadDecode                = "payload_decode"
	CodeDuplicateEnvelopeID          = "duplicate_envelope_id"
	CodeScopeMismatch                = "scope_mismatch"
	CodeUnmatchedResponse            = "unmatched_response"
	CodeMissingResponse              = "missing_response"
	CodeDuplicateResponse            = "duplicate_response"
	CodeSequenceGap                  = "sequence_gap"
	CodeSequenceRegression           = "sequence_regression"
	CodeIllegalRunTransition         = "illegal_run_transition"
	CodeMissingRunStarted            = "missing_run_started"
	CodeMissingRunTerminal           = "missing_run_terminal"
	CodeDuplicateRunTerminal         = "duplicate_run_terminal"
	CodeEventAfterTerminal           = "event_after_terminal"
	CodePendingToolAtTerminal        = "pending_tool_at_terminal"
	CodePendingInteractionAtTerminal = "pending_interaction_at_terminal"
	CodeUnmatchedTool                = "unmatched_tool"
	CodeIllegalToolTransition        = "illegal_tool_transition"
	CodeDuplicateInteraction         = "duplicate_interaction"
	CodeUnmatchedInteraction         = "unmatched_interaction"
	CodeWrongInteractionResponder    = "wrong_interaction_responder"
	CodeUnavailableCapability        = "unavailable_capability"
	CodeUnhonouredCapability         = "unhonoured_capability"
	CodeStaleCapabilityRevision      = "stale_capability_revision"
	CodeCancelNotSettled             = "cancel_not_settled"
	CodeUndeclaredReplayGap          = "undeclared_replay_gap"
	CodeUnknownParticipant           = "unknown_participant"
	CodeSessionStateMismatch         = "session_state_mismatch"

	// The run-controls unit. Each names one way a per-submit control can be
	// betrayed rather than applied or refused.
	//
	// CodeUnappliedControl: the control was admitted and then not applied —
	// a substituted model, a missing or nonconforming structured result, a
	// call the policy excludes or a required call never made, or a per_run
	// selection written into the session default.
	//
	// CodeUnsatisfiableControl: a control the endpoint could not honour was
	// admitted anyway, or one it could honour was refused as unsatisfiable.
	//
	// CodeDegradedWithoutOptin: a degraded control was executed without the
	// caller's consent, or refused under a code that does not ask for it.
	//
	// CodeDuplicateToolName: the catalog a tool_choice is judged against
	// lists two tools with one name, so no entry in it can be unambiguous.
	//
	// CodeUndisclosedSelectionModes: a selection capability is advertised
	// without the disclosure that makes it checkable — run.tool_selection
	// without the tool_choice modes the endpoint enforces, or
	// run.model_selection without how a selection is applied. Either would
	// let the key promise nothing: every rule that binds the capability keys
	// on the disclosure.
	CodeUnappliedControl          = "unapplied_control"
	CodeUnsatisfiableControl      = "unsatisfiable_control"
	CodeDegradedWithoutOptin      = "degraded_without_optin"
	CodeDuplicateToolName         = "duplicate_tool_name"
	CodeUndisclosedSelectionModes = "undisclosed_selection_modes"

	// The queue-delivery unit. Each names one way admitting a second
	// nonterminal run per session can go wrong.
	//
	// CodeQueueOrderViolation: a later-admitted run published a sequenced
	// event while an earlier-admitted run of the session was nonterminal.
	// One run executes at a time, in admission order; the pre-start terminal
	// of a run that never started is exempt, because it has no execution to
	// interleave and its release of a slot is capacity the trace must show at
	// the moment it occurs.
	//
	// CodeQueueLimitExceeded: a reservation put the session's nonterminal set
	// or its queued subset above a disclosed bound, or a refusal reported a
	// bound that the window shows was never reached — a caller told to wait
	// for capacity it never lacked.
	//
	// CodePrematureSessionMutation: a snapshot reported a session default
	// other than the one in force at the position it states it was captured
	// at, which is how a reservation's session_mutation applied before its
	// promotion is caught.
	//
	// CodeUndisclosedQueueLimit: the queue capability is advertised without a
	// bound a submission could ever reach — absent, nonpositive, or walled
	// off by an active bound that leaves no room for it beside a started run.
	// Without one the limit validation has nothing to test and the key
	// promises nothing.
	CodeQueueOrderViolation      = "queue_order_violation"
	CodeQueueLimitExceeded       = "queue_limit_exceeded"
	CodePrematureSessionMutation = "premature_session_mutation"
	CodeUndisclosedQueueLimit    = "undisclosed_queue_limit"
)

type Diagnostic struct {
	Fixture    string   `json:"fixture,omitempty"`
	Phase      Phase    `json:"phase"`
	Code       string   `json:"code"`
	Index      int      `json:"index,omitempty"`
	Line       int      `json:"line,omitempty"`
	EnvelopeID string   `json:"envelope_id,omitempty"`
	Type       string   `json:"type,omitempty"`
	Pointer    string   `json:"pointer,omitempty"`
	RelatedIDs []string `json:"related_ids,omitempty"`
	Expected   string   `json:"expected,omitempty"`
	Actual     string   `json:"actual,omitempty"`
	Message    string   `json:"message"`
}

func (d Diagnostic) Error() string {
	where := d.Fixture
	if d.Line > 0 {
		where += fmt.Sprintf(":%d", d.Line)
	} else {
		where += fmt.Sprintf("[%d]", d.Index)
	}
	if d.Pointer != "" {
		where += d.Pointer
	}
	return fmt.Sprintf("%s: %s: %s", where, d.Code, d.Message)
}

func sortDiagnostics(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.Index != b.Index {
			return a.Index < b.Index
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Pointer != b.Pointer {
			return a.Pointer < b.Pointer
		}
		if a.EnvelopeID != b.EnvelopeID {
			return a.EnvelopeID < b.EnvelopeID
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Expected != b.Expected {
			return a.Expected < b.Expected
		}
		if a.Actual != b.Actual {
			return a.Actual < b.Actual
		}
		if a.Message != b.Message {
			return a.Message < b.Message
		}
		return strings.Join(a.RelatedIDs, "\x00") < strings.Join(b.RelatedIDs, "\x00")
	})
}

type Result struct {
	Diagnostics []Diagnostic `json:"diagnostics"`
}

func (r Result) Valid() bool { return len(r.Diagnostics) == 0 }
func (r Result) HasCode(code string) bool {
	for _, d := range r.Diagnostics {
		if d.Code == code {
			return true
		}
	}
	return false
}
func (r Result) PrimaryPhase() Phase {
	if len(r.Diagnostics) == 0 {
		return ""
	}
	return r.Diagnostics[0].Phase
}
