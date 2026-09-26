package validation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type PresentationValidator struct {
	schema *jsonschema.Schema
	mode   Mode
}

type presentationEnvelope struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Sequence  int             `json:"sequence"`
	Payload   json.RawMessage `json:"payload"`
}

type presentationSnapshotPayload struct {
	Snapshot struct {
		Target struct {
			Kind      string `json:"kind"`
			SessionID string `json:"session_id"`
		} `json:"target"`
		Revision int `json:"revision"`
	} `json:"snapshot"`
}

type presentationUpdatedPayload struct {
	Target struct {
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
	} `json:"target"`
	BaseRevision int `json:"base_revision"`
	Revision     int `json:"revision"`
}

type presentationSubmitResponsePayload struct {
	IntentID           string `json:"intent_id"`
	Accepted           bool   `json:"accepted"`
	RequestedDelivery  string `json:"requested_delivery"`
	EffectiveDelivery  string `json:"effective_delivery"`
	DeliveryResolution string `json:"delivery_resolution"`
	Admission          string `json:"admission"`
}

type presentationTarget struct {
	kind      string
	sessionID string
}

func NewPresentationValidator() (*PresentationValidator, error) {
	return NewPresentationValidatorWith(ModeStrict)
}

func NewPresentationValidatorWith(mode Mode) (*PresentationValidator, error) {
	if mode == "" {
		mode = ModeStrict
	}
	bundle, err := compileBundle(CompileOptions{Mode: mode})
	if err != nil {
		return nil, err
	}
	if bundle.presentation == nil {
		return nil, fmt.Errorf("compile schema bundle: presentation envelope schema not found")
	}
	return &PresentationValidator{schema: bundle.presentation, mode: mode}, nil
}

func (v *PresentationValidator) ValidateBytes(data []byte, fixture string) Result {
	return v.Validate(bytes.NewReader(data), fixture)
}

func (v *PresentationValidator) Validate(r io.Reader, fixture string) Result {
	raw, parseDiag := parseTrace(r, fixture)
	if parseDiag != nil {
		return Result{Diagnostics: []Diagnostic{*parseDiag}}
	}
	result := Result{}
	revisions := map[presentationTarget]int{}
	sequences := map[presentationTarget]int{}
	for i, entry := range raw {
		var document any
		if err := json.Unmarshal(entry.raw, &document); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Index: i, Line: entry.line, Message: err.Error()})
			continue
		}
		if key, duplicate := DuplicateKey(entry.raw); duplicate {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeDuplicateKey, Index: i, Line: entry.line, Message: fmt.Sprintf("duplicate object key %q", key)})
			continue
		}
		var e presentationEnvelope
		if err := json.Unmarshal(entry.raw, &e); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodePayloadDecode, Index: i, Line: entry.line, Message: err.Error()})
			continue
		}
		if err := v.schema.Validate(document); err != nil {
			result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: i, Line: entry.line, EnvelopeID: e.ID, Type: e.Type, Message: err.Error()})
			continue
		}
		switch e.Type {
		case "presentation.snapshot.response":
			var p presentationSnapshotPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodePayloadDecode, Index: i, Line: entry.line, EnvelopeID: e.ID, Type: e.Type, Message: err.Error()})
				continue
			}
			target := presentationTarget{kind: p.Snapshot.Target.Kind, sessionID: p.Snapshot.Target.SessionID}
			if held, seen := revisions[target]; seen && p.Snapshot.Revision < held {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Fixture: fixture, Phase: PhaseSemantic, Code: CodePresentationRevisionRegression, Index: i, Line: entry.line,
					EnvelopeID: e.ID, Type: e.Type, Pointer: "/payload/snapshot/revision",
					Expected: "a revision at or above " + strconv.Itoa(held), Actual: strconv.Itoa(p.Snapshot.Revision),
					Message: "a snapshot went back on the revision its target had already published, so a consumer cannot order what it is told",
				})
				continue
			}
			revisions[target] = p.Snapshot.Revision
		case "presentation.updated":
			var p presentationUpdatedPayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodePayloadDecode, Index: i, Line: entry.line, EnvelopeID: e.ID, Type: e.Type, Message: err.Error()})
				continue
			}
			target := presentationTarget{kind: p.Target.Kind, sessionID: p.Target.SessionID}
			last, seen := sequences[target]
			switch {
			case !seen && e.Sequence != 1:
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Fixture: fixture, Phase: PhaseSemantic, Code: CodeSequenceGap, Index: i, Line: entry.line,
					EnvelopeID: e.ID, Type: e.Type, Pointer: "/sequence",
					Expected: "1", Actual: strconv.Itoa(e.Sequence),
					Message: "the first update of a target does not open its sequence at 1",
				})
			case seen && e.Sequence <= last:
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Fixture: fixture, Phase: PhaseSemantic, Code: CodeSequenceRegression, Index: i, Line: entry.line,
					EnvelopeID: e.ID, Type: e.Type, Pointer: "/sequence",
					Expected: strconv.Itoa(last + 1), Actual: strconv.Itoa(e.Sequence),
					Message: "an update repeats or goes back on the sequence its target had reached, so a consumer cannot tell a replay from a gap",
				})
			case seen && e.Sequence != last+1:
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Fixture: fixture, Phase: PhaseSemantic, Code: CodeSequenceGap, Index: i, Line: entry.line,
					EnvelopeID: e.ID, Type: e.Type, Pointer: "/sequence",
					Expected: strconv.Itoa(last + 1), Actual: strconv.Itoa(e.Sequence),
					Message: "an update skipped a sequence, so a consumer cannot tell a gap from a lost frame",
				})
			}
			if !seen || e.Sequence > last {
				sequences[target] = e.Sequence
			}
			held, seen := revisions[target]
			if !seen {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Fixture: fixture, Phase: PhaseSemantic, Code: CodePresentationUpdateWithoutSnapshot, Index: i, Line: entry.line,
					EnvelopeID: e.ID, Type: e.Type, Pointer: "/payload/base_revision",
					Expected: "a snapshot of this target first", Actual: "an update against a revision no snapshot established",
					Message: "an update applies to a revision the trace never published a snapshot for, so its base_revision cannot refer to anything a consumer holds",
				})
				continue
			}
			if p.BaseRevision != held {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Fixture: fixture, Phase: PhaseSemantic, Code: CodePresentationBaseRevisionMismatch, Index: i, Line: entry.line,
					EnvelopeID: e.ID, Type: e.Type, Pointer: "/payload/base_revision",
					Expected: strconv.Itoa(held), Actual: strconv.Itoa(p.BaseRevision),
					Message: "an update names a base_revision the target had not reached, so the change set cannot be applied to the state a consumer holds",
				})
				continue
			}
			if p.Revision != held+1 {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Fixture: fixture, Phase: PhaseSemantic, Code: CodePresentationRevisionGap, Index: i, Line: entry.line,
					EnvelopeID: e.ID, Type: e.Type, Pointer: "/payload/revision",
					Expected: strconv.Itoa(held + 1), Actual: strconv.Itoa(p.Revision),
					Message: "an update does not advance its target's revision by one, so a consumer cannot tell a contiguous change set from a lost one",
				})
				continue
			}
			revisions[target] = p.Revision
		case "intent.message.submit.response":
			var p presentationSubmitResponsePayload
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodePayloadDecode, Index: i, Line: entry.line, EnvelopeID: e.ID, Type: e.Type, Message: err.Error()})
				continue
			}
			if p.Accepted && p.EffectiveDelivery == "" {
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Fixture: fixture, Phase: PhaseSemantic, Code: CodePresentationDeliveryUnresolved, Index: i, Line: entry.line,
					EnvelopeID: e.ID, Type: e.Type, Pointer: "/payload/effective_delivery",
					Expected: "the delivery control resolved", Actual: "an accepted submit reporting no effective delivery",
					Message: "an accepted submit that reports no effective delivery leaves a presentation layer to infer it from local run state, which may be stale",
				})
			}
		}
	}
	sortDiagnostics(result.Diagnostics)
	return result
}
