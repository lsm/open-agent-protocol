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

	CodeUnappliedControl          = "unapplied_control"
	CodeUnsatisfiableControl      = "unsatisfiable_control"
	CodeDegradedWithoutOptin      = "degraded_without_optin"
	CodeDuplicateToolName         = "duplicate_tool_name"
	CodeUndisclosedSelectionScope = "undisclosed_selection_scope"

	CodeQueueOrderViolation      = "queue_order_violation"
	CodeQueueLimitExceeded       = "queue_limit_exceeded"
	CodePrematureSessionMutation = "premature_session_mutation"
	CodeUndisclosedQueueLimit    = "undisclosed_queue_limit"

	CodeUnmatchedProvider        = "unmatched_provider"
	CodeDuplicateProvider        = "duplicate_provider"
	CodeUnmatchedToolSource      = "unmatched_tool_source"
	CodeDuplicateToolSource      = "duplicate_tool_source"
	CodeCatalogMismatch          = "catalog_mismatch"
	CodeAttachmentFieldInCatalog = "attachment_field_in_catalog"
	CodeUndisclosedAttachModes   = "undisclosed_attach_modes"
	CodeUndisclosedAttachLimit   = "undisclosed_attach_limit"
	CodeUnattributedCall         = "unattributed_call"

	CodeWrongToolOwner            = "wrong_tool_owner"
	CodeUndisclosedProvideLimit   = "undisclosed_provide_limit"
	CodeResolutionPayloadMismatch = "resolution_payload_mismatch"

	CodeModelNotInCatalog        = "model_not_in_catalog"
	CodeAmbiguousDefaultModel    = "ambiguous_default_model"
	CodeDuplicateModelID         = "duplicate_model_id"
	CodeUnannouncedCatalogChange = "unannounced_catalog_change"

	CodeAuthFlowOrder       = "auth_flow_order"
	CodeMissingAuthTerminal = "missing_auth_terminal"

	CodePresentationRevisionRegression    = "presentation_revision_regression"
	CodePresentationUpdateWithoutSnapshot = "presentation_update_without_snapshot"
	CodePresentationBaseRevisionMismatch  = "presentation_base_revision_mismatch"
	CodePresentationRevisionGap           = "presentation_revision_gap"
	CodePresentationDeliveryUnresolved    = "presentation_delivery_unresolved"
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
