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

	// The tool-sources unit. Each names one way a catalog can stop resolving
	// a tool to one source, one owner, and one endpoint.
	//
	// CodeUnmatchedToolSource: a tool or a call names a source nothing
	// declares, or one that disagrees with the source the session's catalog
	// records for that tool, or — in a served catalog, whose whole point is
	// attribution — names no source at all. Either way the attribution resolves
	// nowhere, or somewhere else.
	//
	// CodeDuplicateToolSource: two descriptors share an id, so a tool's
	// `source` and a call's `source` no longer resolve to one endpoint.
	//
	// CodeCatalogMismatch: a session-scoped catalog does not reflect the
	// attachment it is required to. Attachment is for the session's lifetime,
	// so an attached source never drops out of a later list and never changes
	// the members it was attached with.
	//
	// CodeAttachmentFieldInCatalog: a published source carries `command`,
	// `args`, or `environment` — the attachment-only members, one of which can
	// hold a literal credential. The descriptor shape excludes them, so this
	// catches a tolerant or hand-rolled serializer reflecting the open-time
	// value straight back to clients.
	//
	// CodeUndisclosedAttachModes: action.tool_sources.attach is advertised
	// affirmatively while disclosing no session_open mode, so the key names an
	// application no open can elect. Diagnosed on the descriptor that
	// publishes it, as undisclosed_selection_modes is.
	//
	// CodeUndisclosedAttachLimit: an attachment carrying no defect any rule
	// names, and violating no limit the endpoint disclosed, was refused. The
	// refusal is itself the evidence that a constraint exists which the caller
	// was never told about.
	//
	// CodeUnattributedCall: an endpoint that advertises action.tools.list
	// emitted a call naming no source for a tool its own published catalog
	// attributes. `source` is optional on the wire for every endpoint, because
	// one outside this unit has no catalog to attribute against — but an
	// endpoint that publishes the mapping and then omits it on the call leaves a
	// consumer parsing the tool name again, which is the inference the member
	// exists to remove. It is distinct from unmatched_tool_source: that one says
	// the attribution resolves somewhere wrong, this one that an endpoint which
	// could attribute did not.
	CodeUnmatchedToolSource      = "unmatched_tool_source"
	CodeDuplicateToolSource      = "duplicate_tool_source"
	CodeCatalogMismatch          = "catalog_mismatch"
	CodeAttachmentFieldInCatalog = "attachment_field_in_catalog"
	CodeUndisclosedAttachModes   = "undisclosed_attach_modes"
	CodeUndisclosedAttachLimit   = "undisclosed_attach_limit"
	CodeUnattributedCall         = "unattributed_call"
	// The models unit. A catalog is a promise that its ids are selectable and
	// that nothing else is, so each of these names one way the published
	// catalog and the endpoint's own behaviour disagree.
	//
	// CodeModelNotInCatalog: a selection and the catalog contradict each other
	// in either direction — an admitted model the catalog does not list, a
	// listed model refused as missing, a refusal that names no id the caller
	// can act on, or a catalog whose own current_model_id it does not describe.
	//
	// CodeAmbiguousDefaultModel: more than one descriptor claims to be the
	// default, so the catalog names no default at all.
	//
	// CodeDuplicateModelID: two descriptors share an id, so an accepted
	// model_id denotes no single descriptor's metadata.
	//
	// CodeUnannouncedCatalogChange: the catalog changed under one capability
	// revision. The catalog is part of the capability snapshot, so a change
	// that no capabilities.updated announced is one no consumer can observe.
	CodeModelNotInCatalog        = "model_not_in_catalog"
	CodeAmbiguousDefaultModel    = "ambiguous_default_model"
	CodeDuplicateModelID         = "duplicate_model_id"
	CodeUnannouncedCatalogChange = "unannounced_catalog_change"
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
