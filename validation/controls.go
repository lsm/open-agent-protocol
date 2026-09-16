package validation

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The per-submit run controls (unit `run-controls`). A control the endpoint
// has not affirmatively advertised is refused before admission; a control it
// advertises is applied or refused with a typed error, never dropped.
//
// Every gate here is judged on the correlated response rather than on the
// request, because the wire makes refusal the required behaviour and
// diagnosing the request would fail the conduct it mandates. The validator
// therefore retains what it expects from a submission and settles it when the
// response arrives: an admission that should have been refused is diagnosed on
// the admission, and a refusal that does not tell the caller what to change is
// diagnosed on the error.response.

// The refusal rungs, most permanent first. A request can fail several of these
// at once and one error.response carries one code, so the expectations are
// ranked rather than conjoined: the winner owns the response and every lower
// expectation on that request is discharged without diagnosis. The order is
// the order in which a caller can act — what it must stop sending outranks
// what it must send differently, which outranks what it may simply retry.
const (
	rungCapability = iota + 1
	rungDegradation
	rungUnsatisfiable
)

// The typed error codes a conforming refusal carries.
const (
	errorUnsupportedFeature = "unsupported_feature"
	errorCapabilityDegraded = "capability_degraded"
	errorModelNotFound      = "model_not_found"
)

// The two conditions unsupported_feature covers.
const (
	reasonUnadvertised  = "unadvertised"
	reasonUnsatisfiable = "unsatisfiable"
)

// controlExpectation is one retained refusal: what the endpoint owes the
// caller for this request, and what to diagnose when it does something else.
type controlExpectation struct {
	rung int
	// key is the capability key the refusal must name, and the tie-break
	// among peers on one rung: within a rung the expectation whose key sorts
	// first wins. The rule is arbitrary only in that some rule was needed; it
	// is total, stable, and needs no amendment when a later unit adds a key.
	key string
	// pointer is the offending member, which breaks ties among peers of one
	// rule in JSON Pointer order, so two encodings of one request owe the
	// same refusal. JSON object member order carries no meaning and a
	// decoder may reorder it, so serialized order cannot decide this.
	pointer string
	// code, reason, and detail are what a conforming error.response carries.
	code, reason string
	detailName   string
	detailValue  string
	// diagnostic is emitted when the endpoint admits instead of refusing, or
	// refuses under a shape that does not tell the caller what to change.
	diagnostic string
	message    string
}

// less orders two expectations by the refusal precedence: rung, then
// capability key, then the offending member's pointer.
func (e *controlExpectation) less(other *controlExpectation) bool {
	if e.rung != other.rung {
		return e.rung < other.rung
	}
	if e.key != other.key {
		return e.key < other.key
	}
	return pointerLess(e.pointer, other.pointer)
}

// pointerLess compares two JSON Pointers segment by segment: object member
// names lexicographically, array indices numerically, so /allowed/2 precedes
// /allowed/10 and /allowed/0 precedes /disallowed/0.
func pointerLess(a, b string) bool {
	left, right := splitPointer(a), splitPointer(b)
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] == right[i] {
			continue
		}
		leftIndex, leftNumeric := pointerIndex(left[i])
		rightIndex, rightNumeric := pointerIndex(right[i])
		if leftNumeric && rightNumeric {
			return leftIndex < rightIndex
		}
		return left[i] < right[i]
	}
	return len(left) < len(right)
}

func splitPointer(p string) []string {
	if p == "" {
		return nil
	}
	segments := []string{}
	for _, segment := range bytes.Split([]byte(p), []byte("/")) {
		if len(segment) == 0 {
			continue
		}
		segments = append(segments, string(segment))
	}
	return segments
}

func pointerIndex(segment string) (int, bool) {
	index := 0
	for _, r := range segment {
		if r < '0' || r > '9' {
			return 0, false
		}
		index = index*10 + int(r-'0')
	}
	return index, len(segment) > 0
}

// admittedControls is the control set one run was admitted with, together with
// the descriptor facts as the revision active at admission declared them. Every
// per-run judgement reads these rather than the current descriptor: the
// effective capability revision is fixed for an admitted run, so a later
// capabilities.updated changes how later admissions are judged and never how
// this one is.
type admittedControls struct {
	present      bool
	modelPresent bool
	model        string
	instructions bool
	choice       *protocol.ToolChoice
	catalog      []string
	catalogKnown bool
	schema       *OutputSchema
	schemaRaw    json.RawMessage
	fixedResult  json.RawMessage
	mode         string
	calls        map[string]bool
}

// pendingSubmit is what one submit request left for its response to settle.
type pendingSubmit struct {
	expectation *controlExpectation
	// satisfiable names the control keys the validator judged advertised and
	// within every constraint the endpoint disclosed. A refusal citing one of
	// them claims a condition the validator can see does not hold, which is
	// the direction that would otherwise bind the reference adapter alone.
	satisfiable map[string]bool
	controls    admittedControls
	index, line int
	// session and revision are what the retained model rules are keyed by: a
	// catalog is one session's, and a selection is judged against the catalog
	// served under the revision the submission was made under.
	session  protocol.SessionID
	revision string
	// modelUnjudged marks a selection the trace cannot yet judge, because no
	// catalog under the active revision has been served; modelListed marks one
	// the active catalog does list, so a refusal reporting it missing
	// contradicts the endpoint's own catalog.
	modelUnjudged bool
	modelListed   bool
}

// submitControls judges every control a submission carries and retains what
// the correlated response owes. Nothing is diagnosed on the request itself:
// the endpoint is required to refuse, and a conforming refusal must validate.
func (s *state) submitControls(i, line int, e protocol.Envelope, p protocol.MessageSubmitRequest) {
	pending := &pendingSubmit{satisfiable: map[string]bool{}, index: i, line: line, session: p.SessionID, revision: s.currentCapability}
	defer func() {
		if s.pendingControls == nil {
			s.pendingControls = map[protocol.EnvelopeID]*pendingSubmit{}
		}
		s.pendingControls[e.ID] = pending
	}()
	controls := []struct {
		key     string
		present bool
	}{
		{protocol.FeatureModelSelection, p.ModelID != nil},
		{protocol.FeatureInstructions, p.Instructions != nil},
		{protocol.FeatureToolSelection, len(p.ToolChoice) > 0},
		{protocol.FeatureStructuredOutput, len(p.OutputSchema) > 0},
	}
	any := false
	for _, control := range controls {
		if control.present {
			any = true
		}
	}
	if !any {
		return
	}
	pending.controls.present = true
	pending.controls.mode = s.featureDetail(protocol.FeatureModelSelection).Mode
	var expectations []*controlExpectation
	for _, control := range controls {
		if !control.present {
			continue
		}
		level, judged := s.controlDescriptor(i, line, e, control.key)
		if !judged {
			// The descriptor is missing or stale, or the envelope cites the
			// wrong revision. Those branches are diagnosed on the request, as
			// for every optional envelope, and nothing is retained: which
			// level the key has cannot be read at all.
			continue
		}
		if !affirmative(level) {
			expectations = append(expectations, &controlExpectation{
				rung: rungCapability, key: control.key, pointer: controlPointer(control.key),
				code: errorUnsupportedFeature, reason: reasonUnadvertised,
				detailName: "feature", detailValue: control.key,
				diagnostic: CodeUnavailableCapability,
				message:    "submission carries a control the endpoint has not affirmatively advertised",
			})
			continue
		}
		if level == protocol.SupportDegraded && !p.AllowsDegraded(control.key) {
			expectations = append(expectations, &controlExpectation{
				rung: rungDegradation, key: control.key, pointer: controlPointer(control.key),
				code: errorCapabilityDegraded, detailName: "feature", detailValue: control.key,
				diagnostic: CodeDegradedWithoutOptin,
				message:    "submission carries a degraded control without the caller's opt-in",
			})
			continue
		}
		if control.key == protocol.FeatureToolSelection {
			s.duplicateToolNames(i, line, e)
		}
		defect, satisfiable := s.unsatisfiable(control.key, p, pending)
		if defect != nil {
			expectations = append(expectations, defect)
			continue
		}
		if satisfiable {
			pending.satisfiable[control.key] = true
		}
	}
	// The delivery an explicit non-auto request elects is gated like a
	// control. The mandatory auto delivery is exempt: its degraded level is
	// disclosure a caller reads from the descriptor, not a consent gate, and
	// a caller refused auto could not submit at all.
	if p.Delivery != "" && p.Delivery != protocol.DeliveryAuto {
		key := "session.message.delivery." + string(p.Delivery)
		if level, judged := s.controlDescriptor(i, line, e, key); judged && level == protocol.SupportDegraded && !p.AllowsDegraded(key) {
			expectations = append(expectations, &controlExpectation{
				rung: rungDegradation, key: key, pointer: "/payload/delivery",
				code: errorCapabilityDegraded, detailName: "feature", detailValue: key,
				diagnostic: CodeDegradedWithoutOptin,
				message:    "submission elects a degraded delivery without the caller's opt-in",
			})
		}
	}
	sort.SliceStable(expectations, func(a, b int) bool { return expectations[a].less(expectations[b]) })
	if len(expectations) > 0 {
		pending.expectation = expectations[0]
	}
}

// controlPointer is the submit payload member one capability key governs.
func controlPointer(key string) string {
	switch key {
	case protocol.FeatureModelSelection:
		return "/payload/model_id"
	case protocol.FeatureInstructions:
		return "/payload/instructions"
	case protocol.FeatureToolSelection:
		return "/payload/tool_choice"
	case protocol.FeatureStructuredOutput:
		return "/payload/output_schema"
	}
	return "/payload"
}

func affirmative(level protocol.SupportLevel) bool {
	switch level {
	case protocol.SupportNative, protocol.SupportEmulated, protocol.SupportDegraded:
		return true
	}
	return false
}

// controlDescriptor reports the level a capability key is advertised at. The
// missing-descriptor and stale-revision branches are judged on the request, as
// they are for every optional envelope, and reported as not judged so no
// expectation is retained for a key whose level could not be read.
func (s *state) controlDescriptor(i, line int, e protocol.Envelope, key string) (protocol.SupportLevel, bool) {
	if s.currentCapability == "" || s.capabilitiesStale {
		s.add(CodeUnavailableCapability, i, line, e, "/type", "optional feature requires a current capability descriptor")
		return "", false
	}
	if string(e.CapabilityRevision) != s.currentCapability {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "optional feature must cite the active capability descriptor", s.currentCapability, string(e.CapabilityRevision))
		return "", false
	}
	if level, ok := s.features[key]; ok {
		return level, true
	}
	return protocol.SupportUnavailable, true
}

// featureDetail reports one key's full disclosure from the active descriptor.
func (s *state) featureDetail(key string) protocol.FeatureSupport {
	return s.featureSupports[key]
}

// unsatisfiable judges whether this request's value of one control can be
// honoured at all, and records what an admitted control binds for the run. The
// second result reports whether the validator can say the control is within
// every constraint the endpoint disclosed: a control it can neither refuse nor
// vouch for — a tool_choice mode the endpoint never said it enforces — is
// neither, so the endpoint may refuse it and is not held to admitting it.
func (s *state) unsatisfiable(key string, p protocol.MessageSubmitRequest, pending *pendingSubmit) (*controlExpectation, bool) {
	controls := &pending.controls
	unsatisfiableAs := func(pointer, detailName, detailValue, message string) *controlExpectation {
		if detailValue == "" {
			// A defect with no offending value to name — a policy that is
			// contradictory in itself rather than about one tool — owes no
			// detail, and demanding an empty one would fail a refusal that
			// says everything it can.
			detailName = ""
		}
		return &controlExpectation{
			rung: rungUnsatisfiable, key: key, pointer: pointer,
			code: errorUnsupportedFeature, reason: reasonUnsatisfiable,
			detailName: detailName, detailValue: detailValue,
			diagnostic: CodeUnsatisfiableControl, message: message,
		}
	}
	switch key {
	case protocol.FeatureModelSelection:
		controls.modelPresent, controls.model = true, protocol.Control(p.ModelID)
		if controls.model == "" {
			// No catalog can list an empty id and none is needed to decide
			// it, so this condition is T1's outright. Its conforming refusal
			// is the catalog miss's, not unsupported_feature: the endpoint
			// understood the request and cannot serve the id.
			return &controlExpectation{
				rung: rungUnsatisfiable, key: key, pointer: "/payload/model_id",
				code: errorModelNotFound, detailName: "model_id", detailValue: "",
				diagnostic: CodeUnsatisfiableControl,
				message:    "submission carries an empty model_id, which no catalog can list",
			}, false
		}
		// Whether a non-empty id is one the endpoint serves is decidable only
		// against a catalog. Where one has been served under the active
		// revision at native or emulated, the miss is decided here; where none
		// has, the selection is retained and the first catalog under that
		// revision settles it.
		//
		// Either way unsupported_feature is wrong for a model id: the wire
		// assigns every catalog miss to model_not_found, so either the
		// endpoint serves the id and owed an admission, or it does not and
		// owed model_not_found naming it.
		catalog := s.track(p.SessionID).catalog
		switch {
		case catalog.binds(s.currentCapability) && !catalog.ids[controls.model]:
			return &controlExpectation{
				rung: rungUnsatisfiable, key: key, pointer: "/payload/model_id",
				code: errorModelNotFound, detailName: "model_id", detailValue: controls.model,
				diagnostic: CodeModelNotInCatalog,
				message:    "submission selects a model the catalog served under this revision does not list",
			}, false
		case catalog.binds(s.currentCapability):
			pending.modelListed = true
		default:
			pending.modelUnjudged = true
		}
		return nil, true
	case protocol.FeatureInstructions:
		// instructions has no unsatisfiability condition at all: an endpoint
		// advertising the key and refusing them is refusing something it said
		// it accepts.
		controls.instructions = true
		return nil, true
	case protocol.FeatureToolSelection:
		policy, err := p.ToolChoicePolicy()
		if err != nil {
			return unsatisfiableAs("/payload/tool_choice", "", "", "tool_choice is not the typed policy: "+err.Error()), false
		}
		if policy == nil {
			// Unreachable while the accessor reports every present value as a
			// policy or an error, and a defect rather than a panic if that
			// ever changes: the schema admits any JSON value here.
			return unsatisfiableAs("/payload/tool_choice", "", "", "tool_choice carries no typed policy"), false
		}
		catalog, known := s.toolCatalog()
		// A catalog carrying one name twice cannot judge a policy at all: the
		// ambiguity is diagnosed where the catalog is read, and the policy
		// itself is left alone rather than refused for someone else's defect.
		known = known && duplicateToolName(catalog) == ""
		controls.catalog, controls.catalogKnown = catalog, known
		controls.choice = policy
		if defect := policy.Unsatisfiable(catalog, known); defect != nil {
			return unsatisfiableAs(defect.Pointer, "tool", defect.Tool, defect.Reason), false
		}
		// A mode the endpoint never disclosed is one it may refuse: the key
		// would otherwise promise nothing, since an endpoint could advertise
		// it, refuse every required and named policy, and pass.
		return nil, disclosedMode(s.featureDetail(key), policy.Mode)
	case protocol.FeatureStructuredOutput:
		compiled, err := CompileOutputSchema(p.OutputSchema)
		if err != nil {
			return unsatisfiableAs("/payload/output_schema", "field", "output_schema", err.Error()), false
		}
		controls.schema, controls.schemaRaw = compiled, append(json.RawMessage(nil), p.OutputSchema...)
		// A disclosed fixed result binds the admission in both directions: a
		// schema that object cannot satisfy could only complete
		// nonconforming, so admitting it is the defect.
		// Only an object is a fixed result. The schema requires one, so a
		// trace carrying anything else never reaches this phase; the guard is
		// here because the failure direction matters if it ever did. A null or
		// a scalar satisfies no object-rooted schema, so reading it as a
		// constraint would make every structured-output request unsatisfiable
		// and let an endpoint refuse them all while looking conformant.
		// Ignoring it refuses nothing that was satisfiable.
		if fixed, ok := s.featureDetail(key).Constraints[protocol.ConstraintFixedResult]; ok && isJSONObject(fixed) {
			controls.fixedResult = fixed
			if err := compiled.Validate(json.RawMessage(fixed)); err != nil {
				return unsatisfiableAs("/payload/output_schema", "field", "output_schema", "the disclosed fixed result cannot satisfy the requested schema"), false
			}
		}
		return nil, true
	}
	return nil, false
}

// isJSONObject reports whether an encoded document is a JSON object. Only an
// object is a structured result, so only an object is a fixed_result.
func isJSONObject(document json.RawMessage) bool {
	trimmed := bytes.TrimSpace(document)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var value map[string]any
	return json.Unmarshal(trimmed, &value) == nil
}

// knownToolChoiceModes are the tool_choice modes this phase rules on. Every
// policy the typed shape admits carries one of them, so a descriptor that
// names none enforces nothing a caller can ask for.
var knownToolChoiceModes = []string{protocol.ToolChoiceAuto, protocol.ToolChoiceNone, protocol.ToolChoiceRequired, protocol.ToolChoiceNamed}

// enforcesAKnownMode reports whether a disclosure names at least one mode a
// caller can actually send. Unknown names beside a known one are additive
// vocabulary, not a defect.
func enforcesAKnownMode(modes []string) bool {
	for _, mode := range modes {
		if slices.Contains(knownToolChoiceModes, mode) {
			return true
		}
	}
	return false
}

// describeModes renders a disclosure for a diagnostic's observed value.
func describeModes(modes []string) string {
	if len(modes) == 0 {
		return "none"
	}
	return strings.Join(modes, ", ")
}

// disclosedMode reports whether the endpoint said it enforces one tool_choice
// mode. A descriptor with no modes at all is diagnosed where it is published
// (undisclosed_selection_modes), and nothing it refuses is conforming here.
func disclosedMode(support protocol.FeatureSupport, mode string) bool {
	for _, disclosed := range support.Modes {
		if disclosed == mode {
			return true
		}
	}
	return false
}

// toolCatalog reports the effective catalog a tool_choice is judged against:
// the descriptor's top-level tools and every layer's, normalized into one
// list, since a valid descriptor may publish its catalog under a layer alone.
func (s *state) toolCatalog() ([]string, bool) {
	if !s.catalogKnown {
		return nil, false
	}
	return s.catalog, true
}

// duplicateToolName reports the first name a catalog carries twice, or "".
func duplicateToolName(catalog []string) string {
	seen := map[string]bool{}
	for _, name := range catalog {
		if seen[name] {
			return name
		}
		seen[name] = true
	}
	return ""
}

// settleSubmitAdmission judges an admission against what the request's
// controls owed. An admission where a refusal was owed is the defect the
// retained expectation names.
func (s *state) settleSubmitAdmission(i, line int, e protocol.Envelope, p protocol.MessageSubmitResponse) admittedControls {
	pending := s.pendingControls[e.InReplyTo]
	if pending == nil {
		return admittedControls{}
	}
	if pending.expectation != nil {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/admission", pending.expectation.message, "a typed refusal naming "+pending.expectation.key, string(p.Admission), string(e.InReplyTo))
		// The admission itself is the defect. Nothing is retained for the
		// run: judging the execution of a control that should never have been
		// admitted would pile consequences onto one fault.
		return admittedControls{}
	}
	if pending.modelUnjudged {
		// No catalog under this revision has been served, so whether the
		// endpoint may serve this model is undecided. The first catalog under
		// the revision settles it, and an endpoint cannot use the gap to
		// admit a model it does not serve.
		s.retainModelSelection(pending.session, unjudgedModel{
			model: pending.controls.model, revision: pending.revision, admitted: true,
			index: i, line: line, envelope: e, submission: e.InReplyTo,
		})
	}
	// The admitted model is authoritative for the run, so the response must
	// repeat what the request asked for rather than quietly substituting one.
	if pending.controls.modelPresent && p.ModelID != pending.controls.model {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "admission reports a model other than the one it admitted", pending.controls.model, p.ModelID, string(e.InReplyTo))
	}
	return pending.controls
}

// settleControlRefusal judges a correlated error.response against what the
// request owed: a refusal under a code, feature, or reason that does not tell
// the caller what to stop sending is the same defect as no refusal at all.
func (s *state) settleControlRefusal(i, line int, e protocol.Envelope) {
	pending := s.pendingControls[e.InReplyTo]
	if pending == nil {
		return
	}
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	detail := func(name string) (string, bool) {
		value, ok := payload.Error.Details[name]
		text, isText := value.(string)
		return text, ok && isText
	}
	if expectation := pending.expectation; expectation != nil {
		if !conformingRefusal(payload.Error, expectation) {
			s.addExpected(expectation.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
		}
		return
	}
	// A selection the trace cannot judge yet — no catalog under the active
	// revision — is retained and settled by the first catalog that arrives,
	// because deferring the judgement must not weaken it.
	if pending.modelUnjudged {
		s.retainModelSelection(pending.session, unjudgedModel{
			model: pending.controls.model, revision: pending.revision,
			index: i, line: line, envelope: e, submission: e.InReplyTo,
		})
	} else if pending.modelListed {
		s.diagnoseFalseMiss(i, line, e, pending.controls.model)
	}
	// The other direction: a control the validator finds advertised and
	// within every disclosed constraint, refused as unsupported. The endpoint
	// has claimed a condition the validator can see does not hold, and the
	// caller discards a request that was valid.
	if payload.Error.Code != errorUnsupportedFeature {
		return
	}
	feature, ok := detail("feature")
	if !ok || !pending.satisfiable[feature] {
		return
	}
	s.addExpected(CodeUnsatisfiableControl, i, line, e, "/payload/error", "refusal names a control the endpoint advertises and this request satisfies", "admission or a defect the refusal names", describeRefusal(payload.Error), string(e.InReplyTo))
}

// conformingRefusal reports whether one error.response says what the retained
// expectation requires: the code, the capability it answers about, the reason
// that distinguishes the two conditions unsupported_feature covers, and the
// detail that names the offending member.
func conformingRefusal(err protocol.ProtocolError, expectation *controlExpectation) bool {
	detail := func(name string) (string, bool) {
		value, ok := err.Details[name]
		text, isText := value.(string)
		return text, ok && isText
	}
	if err.Code != expectation.code {
		return false
	}
	if expectation.code == errorUnsupportedFeature {
		// unsupported_feature answers about one capability, so the key is the
		// refusal's subject: omitted, or naming another key, it tells the
		// caller no more than that something was unsupported. The
		// unsatisfiability rung carries the offending member in details.tool
		// or details.field, so without this the feature on those refusals
		// would go unjudged.
		feature, ok := detail("feature")
		if !ok || feature != expectation.key {
			return false
		}
	}
	if expectation.reason != "" {
		reason, ok := detail("reason")
		if !ok || reason != expectation.reason {
			return false
		}
	}
	if expectation.detailName != "" {
		value, ok := detail(expectation.detailName)
		if !ok || value != expectation.detailValue {
			return false
		}
	}
	return true
}

func (e *controlExpectation) describe() string {
	description := e.code
	if e.reason != "" {
		description += "/" + e.reason
	}
	if e.code == errorUnsupportedFeature && e.detailName != "feature" {
		description += " feature=" + e.key
	}
	if e.detailName != "" {
		description += " " + e.detailName + "=" + e.detailValue
	}
	return description
}

func describeRefusal(err protocol.ProtocolError) string {
	description := err.Code
	if reason, ok := err.Details["reason"].(string); ok {
		description += "/" + reason
	}
	for _, name := range []string{"feature", "model_id", "tool", "field", "source"} {
		if value, ok := err.Details[name].(string); ok {
			description += " " + name + "=" + value
		}
	}
	return description
}

// duplicateToolNames diagnoses a catalog that cannot judge a policy at all:
// two tools with one name, whatever their owners, leave every named or listed
// entry ambiguous. It is judged where the policy is judged, since that is
// where the catalog is read.
func (s *state) duplicateToolNames(i, line int, e protocol.Envelope) {
	if s.catalogAmbiguous {
		// The tool-sources unit judges every accepted descriptor's effective
		// catalog where it is published, which is where the ambiguity is. One
		// fault gets one diagnosis: repeating it on every submission the
		// descriptor governs would blame each policy for the descriptor's
		// defect. This check still owns a catalog the descriptor did not
		// publish.
		return
	}
	catalog, known := s.toolCatalog()
	if !known {
		return
	}
	if name := duplicateToolName(catalog); name != "" {
		s.addExpected(CodeDuplicateToolName, i, line, e, "/payload/tool_choice", "the catalog this tool_choice is judged against lists two tools with one name", "one tool per name", name)
	}
}

// checkCompletedControls judges a completion against the controls its run was
// admitted with: the structured result, the disclosed fixed result, the tool
// policy's positive requirements, and the model attribution.
func (s *state) checkCompletedControls(i, line int, e protocol.Envelope, r *runState) {
	controls := r.controls
	if !controls.present {
		return
	}
	var p protocol.RunCompletedPayload
	_ = e.DecodePayload(&p)
	if p.ModelID != "" && controls.modelPresent && p.ModelID != controls.model {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "completion attributes the run to a model other than the admitted one", controls.model, p.ModelID, string(r.id))
	}
	if controls.schema != nil {
		switch {
		case len(p.Result) == 0:
			s.addExpected(CodeUnappliedControl, i, line, e, "/payload/result", "completion under an admitted output_schema carries no result", "a result conforming to the admitted schema", "absent", string(r.id))
		default:
			if err := controls.schema.Validate(p.Result); err != nil {
				s.addExpected(CodeUnappliedControl, i, line, e, "/payload/result", "completion result does not conform to the admitted output_schema", "a conforming result", string(p.Result), string(r.id))
			} else if len(controls.fixedResult) > 0 && !sameJSON(controls.fixedResult, p.Result) {
				// A declared fixed result is a promise a consumer plans
				// against; a permissive schema would otherwise admit many
				// objects and let the endpoint break it unnoticed.
				s.addExpected(CodeUnappliedControl, i, line, e, "/payload/result", "completion does not carry the result the endpoint declared it would", string(controls.fixedResult), string(p.Result), string(r.id))
			}
		}
	}
	if choice := controls.choice; choice != nil {
		switch choice.Mode {
		case protocol.ToolChoiceRequired:
			if len(controls.calls) == 0 {
				s.addExpected(CodeUnappliedControl, i, line, e, "/payload", "run admitted with tool_choice required completed without requesting a tool", "at least one action.call.requested", "none", string(r.id))
			}
		case protocol.ToolChoiceNamed:
			if !controls.calls[choice.Name] {
				s.addExpected(CodeUnappliedControl, i, line, e, "/payload", "run admitted with a named tool_choice completed without requesting that tool", choice.Name, "none", string(r.id))
			}
		}
	}
}

// checkCallAgainstChoice judges one requested call against the run's admitted
// policy, whichever participant owns the call.
func (s *state) checkCallAgainstChoice(i, line int, e protocol.Envelope, r *runState) {
	if !r.controls.present {
		return
	}
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	if p.Name == "" {
		return
	}
	if r.controls.calls == nil {
		r.controls.calls = map[string]bool{}
	}
	r.controls.calls[p.Name] = true
	if choice := r.controls.choice; choice != nil && !choice.Permits(p.Name, r.controls.catalog, r.controls.catalogKnown) {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/name", "run requested a tool its admitted tool_choice excludes", "a tool the policy permits", p.Name, string(r.id))
	}
}

// sameJSON compares two encoded documents by value, so member order and
// insignificant whitespace do not decide whether a promise was kept.
//
// Numbers are decoded as json.Number rather than float64: a declared
// fixed_result is an exact promise, and float64 makes 9007199254740993 equal
// to 9007199254740992, so an endpoint could break the commitment on any
// integer past 2^53 and pass.
func sameJSON(a, b json.RawMessage) bool {
	left, leftErr := decodeExact(a)
	right, rightErr := decodeExact(b)
	if leftErr != nil || rightErr != nil {
		return false
	}
	// Compared structurally rather than through a rendering: a canonical
	// number is its own type, and a rendering would flatten it back onto the
	// string that spells it, so `{"n":1}` would equal `{"n":"1"}`.
	return reflect.DeepEqual(canonical(left), canonical(right))
}

// decodeExact decodes one document without converting its numbers.
func decodeExact(document json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// exactNumber is one JSON number in a canonical, value-based form. It is its
// own type so that a number never compares equal to the string that spells it.
type exactNumber string

// canonicalNumber rewrites one JSON number into a form that depends on its
// value and not on the token that spelled it, so `1`, `1.0`, and `1e0` compare
// equal while 9007199254740993 stays distinct from 9007199254740992. A rat is
// exact for every finite decimal, which float64 is not, and canonical, which
// the token text is not. A number no rat can hold (an exponent past the
// package's limit) keeps its token, which is what the comparison did for every
// number before.
func canonicalNumber(number json.Number) exactNumber {
	if rat, ok := new(big.Rat).SetString(number.String()); ok {
		return exactNumber(rat.RatString())
	}
	return exactNumber(number.String())
}

// canonical rewrites a decoded document into a form whose Go rendering is
// stable: maps become sorted key/value pairs, numbers their exact value.
func canonical(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return canonicalNumber(typed)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pairs := make([][2]any, 0, len(keys))
		for _, key := range keys {
			pairs = append(pairs, [2]any{key, canonical(typed[key])})
		}
		return pairs
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = canonical(item)
		}
		return items
	}
	return value
}

// checkSelectionModes judges a descriptor's own disclosure: a key advertised
// with no enforceable modes promises nothing, since every policy could be
// refused as unsatisfiable and pass.
func (s *state) checkSelectionModes(i, line int, e protocol.Envelope, p protocol.CapabilitiesResponse) {
	if support, ok := p.EffectiveSupport(protocol.FeatureToolSelection); ok && affirmative(support.Level) && !enforcesAKnownMode(support.Modes) {
		// A list of names this phase rules on nothing is the empty list in a
		// costume: every policy the typed shape admits carries one of the four
		// modes, so a descriptor listing none of them refuses every one of
		// them as unsatisfiable and passes — the empty advertisement the
		// disclosure exists to prevent. Unknown names alongside a recognised
		// one are tolerated: the vocabulary is additive, and a descriptor
		// naming a mode a later unit defines still enforces the one it names
		// here.
		s.addExpected(CodeUndisclosedSelectionModes, i, line, e, "/payload/features/run.tool_selection/modes", "run.tool_selection is advertised without disclosing a tool_choice mode the endpoint enforces", "at least one of "+strings.Join(knownToolChoiceModes, ", "), describeModes(support.Modes))
	}
	// The same rule for how a model selection is applied. Without it the key
	// promises nothing a validator can check: the per_run rule and the
	// session_mutation rule both key on the mode, so with neither in force a
	// session default could move under a per-run selection, or stay put under
	// a mutation, and nothing would say so. The defect is the descriptor's, so
	// it is diagnosed where it is published rather than on every admission the
	// descriptor governs — one fault, one diagnosis.
	//
	// `restart` is a defined mode this phase gives no rules, so advertising it
	// here leaves the same hole; it discloses something checkable when the
	// unit that rules on it graduates.
	support, ok := p.EffectiveSupport(protocol.FeatureModelSelection)
	if !ok || !affirmative(support.Level) {
		return
	}
	if support.Mode != protocol.ModePerRun && support.Mode != protocol.ModeSessionMutation {
		s.addExpected(CodeUndisclosedSelectionModes, i, line, e, "/payload/features/run.model_selection/mode", "run.model_selection is advertised without disclosing how a selection is applied", protocol.ModePerRun+" or "+protocol.ModeSessionMutation, support.Mode)
	}
}

// collectCatalog normalizes a descriptor's effective tool catalog: its
// top-level tools and every layer's, since a valid descriptor may publish its
// catalog under a layer alone.
func collectCatalog(p protocol.CapabilitiesResponse) []string {
	names := make([]string, 0, len(p.Tools))
	for _, tool := range p.Tools {
		names = append(names, tool.Name)
	}
	layers := make([]string, 0, len(p.Layers))
	for name := range p.Layers {
		layers = append(layers, name)
	}
	sort.Strings(layers)
	for _, name := range layers {
		for _, tool := range p.Layers[name].Tools {
			names = append(names, tool.Name)
		}
	}
	return names
}
