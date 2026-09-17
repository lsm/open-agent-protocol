package validation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/lsm/open-agent-protocol/protocol"
)

const interactionKindToolCall = "tool_call"

type pendingResolve struct {
	index, line int
	interaction protocol.InteractionID
	run         protocol.RunID
	arm         string
	reason      protocol.ResolveReason

	opaque bool
}

func resolveReasonDiagnostic(reason protocol.ResolveReason) string {
	switch reason {
	case protocol.ReasonWrongResponder:
		return CodeWrongInteractionResponder
	case protocol.ReasonAlreadyResolved, protocol.ReasonRepeatedAcknowledgement:
		return CodeDuplicateInteraction
	default:
		return CodeUnmatchedInteraction
	}
}

func (s *state) controlCallRequested(i, line int, e protocol.Envelope, r *runState) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	if !s.controlOwned(p.ExecutionOwner) {
		return
	}

	if p.InteractionID == "" || p.RespondedBy == "" {
		s.addExpected(CodeIllegalToolTransition, i, line, e, "/payload/interaction_id", "a call the control participant executes must open an interaction and name its responder", "interaction_id and responded_by", describeCallBinding(p), string(p.ToolCallID))
		return
	}
	s.participant(i, line, e, p.RespondedBy, "/payload/responded_by")
	if _, ok := r.interactions[p.InteractionID]; ok {
		s.add(CodeDuplicateInteraction, i, line, e, "/payload/interaction_id", "interaction id was requested more than once")
		return
	}
	opened := uint64(0)
	if e.Sequence != nil {
		opened = *e.Sequence
	}
	r.interactions[p.InteractionID] = &interactionState{
		kind: interactionKindToolCall, requestedBy: p.RequestedBy, respondedBy: p.RespondedBy,
		toolCallID: p.ToolCallID, openedAt: opened,
		resolveRequests: map[protocol.EnvelopeID]bool{}, settlements: map[protocol.EnvelopeID]bool{},
	}
	track := r.tools[p.ToolCallID]
	track.interaction = p.InteractionID
	r.tools[p.ToolCallID] = track
}

func describeCallBinding(p protocol.ActionCallPayload) string {
	return fmt.Sprintf("interaction_id=%s responded_by=%s", p.InteractionID, p.RespondedBy)
}

func (s *state) controlOwned(owner protocol.ParticipantID) bool {
	return s.controlParticipant != "" && owner == s.controlParticipant
}

func (s *state) controlCallEvent(i, line int, e protocol.Envelope, r *runState, next string) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	x := s.callInteraction(r, p)
	if x == nil {
		return
	}
	if x.opaque {

		if s.controlOwned(p.ExecutionOwner) && toolTerminal(next) {
			s.settleControlCall(x, e.ID, e.Sequence)
		}
		return
	}
	if !x.controlCall() {
		return
	}
	switch next {
	case "started":
		if !x.acked && x.acceptedArm == "" {
			s.addExpected(CodeIllegalToolTransition, i, line, e, "/type", "a control-owned call cannot start before an accepted resolution evidences it", "an accepted action.call.resolve.response", "no accepted resolution", string(p.ToolCallID))
			return
		}
		s.checkDerivedRequestID(i, line, e, p, x)
	case "completed", "failed":
		arm := protocol.ResolveArmResult
		if next == "failed" {
			arm = protocol.ResolveArmError
		}

		defer s.settleControlCall(x, e.ID, e.Sequence)
		if x.acceptedArm != arm {
			s.addExpected(CodeIllegalToolTransition, i, line, e, "/type", "a control-owned call's terminal must derive from an accepted resolution of the matching arm", "an accepted "+arm+" resolution", describeAcceptedArm(x), string(p.ToolCallID))
			return
		}
		s.checkDerivedRequestID(i, line, e, p, x)
		s.checkResolutionPayload(i, line, e, p, x, arm)
	case "cancelled":
		s.settleControlCall(x, e.ID, e.Sequence)
	}
}

func describeAcceptedArm(x *interactionState) string {
	if x.acceptedArm == "" {
		return "no accepted resolution"
	}
	return "an accepted " + x.acceptedArm + " resolution"
}

func (s *state) settleControlCall(x *interactionState, id protocol.EnvelopeID, sequence *uint64) {
	x.settled = true
	x.resolved = true
	if x.settlements == nil {
		x.settlements = map[protocol.EnvelopeID]bool{}
	}
	x.settlements[id] = true
	if sequence != nil && x.resolvedAt == 0 {
		x.resolvedAt = *sequence
	}
}

func (s *state) checkDerivedRequestID(i, line int, e protocol.Envelope, p protocol.ActionCallPayload, x *interactionState) {
	if p.RequestID == "" {
		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/request_id", "a resolve-derived event must name the resolve request it came from", "a resolve request for the interaction", "absent", string(p.ToolCallID))
		return
	}
	if !x.resolveRequests[p.RequestID] {
		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/request_id", "a resolve-derived event names no resolve request the trace carries for its interaction", "a resolve request for the interaction", string(p.RequestID), string(p.ToolCallID))
	}
}

func (s *state) checkResolutionPayload(i, line int, e protocol.Envelope, p protocol.ActionCallPayload, x *interactionState, arm string) {
	if arm == protocol.ResolveArmResult {
		want, got := canonicalJSON(x.acceptedResult), canonicalJSON(p.Result)
		if want != got {
			s.addExpected(CodeResolutionPayloadMismatch, i, line, e, "/payload/result", "a control-owned call's completion carries a result the accepted resolution did not state", want, got, string(p.ToolCallID))
		}
		return
	}
	want, got := canonicalError(x.acceptedError), canonicalError(p.Error)
	if want != got {
		s.addExpected(CodeResolutionPayloadMismatch, i, line, e, "/payload/error", "a control-owned call's failure carries an error the accepted resolution did not state", want, got, string(p.ToolCallID))
	}
}

func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "absent"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

func canonicalError(err *protocol.ProtocolError) string {
	if err == nil {
		return "absent"
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		return "unencodable error"
	}
	return canonicalJSON(encoded)
}

func (s *state) callInteraction(r *runState, p protocol.ActionCallPayload) *interactionState {
	if r == nil {
		return nil
	}
	if track, ok := r.tools[p.ToolCallID]; ok && track.interaction != "" {
		return r.interactions[track.interaction]
	}
	if p.InteractionID == "" {
		return nil
	}
	return r.interactions[p.InteractionID]
}

func (s *state) controlResolveRequest(i, line int, e protocol.Envelope) {
	var p protocol.ActionCallResolveRequest
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, p.RunID)
	if e.ToolCallID != "" && e.ToolCallID != p.ToolCallID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "envelope and payload tool_call_id differ", string(e.ToolCallID), string(p.ToolCallID))
	}
	s.feature(i, line, e, "tools")
	run := p.RunID
	if run == "" {
		run = e.RunID
	}
	pending := &pendingResolve{index: i, line: line, interaction: p.InteractionID, run: run, arm: p.Arm()}
	defer func() { s.pendingResolves[e.ID] = pending }()
	x := s.lookupInteraction(run, p.InteractionID)
	if x == nil {
		if r := s.runs[run]; r != nil && r.priorUnknown {

			r.interactions[p.InteractionID] = &interactionState{opaque: true}
			pending.opaque = true
			return
		}
		pending.reason = protocol.ReasonUnknownInteraction
		return
	}
	if x.opaque {
		pending.opaque = true
		return
	}
	if !x.controlCall() {

		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/interaction_id", "a call resolution answers an interaction of another kind", interactionKindToolCall, x.kind, string(p.InteractionID))
		pending.opaque = true
		return
	}
	if x.resolveRequests == nil {
		x.resolveRequests = map[protocol.EnvelopeID]bool{}
	}
	x.resolveRequests[e.ID] = true
	if p.ToolCallID != x.toolCallID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "a call resolution names a tool call the interaction is not bound to", string(x.toolCallID), string(p.ToolCallID), string(p.InteractionID))
	}
	pending.reason = s.resolveLadder(p, x)
}

func (s *state) resolveLadder(p protocol.ActionCallResolveRequest, x *interactionState) protocol.ResolveReason {
	var conditions []protocol.ResolveReason
	if p.RespondedBy != x.respondedBy || (p.RequestedBy != "" && p.RequestedBy != x.requestedBy) {
		conditions = append(conditions, protocol.ReasonWrongResponder)
	}
	acknowledgement := p.Arm() == protocol.ResolveArmAcknowledge
	switch {
	case x.settled:
		conditions = append(conditions, protocol.ReasonAlreadyResolved)
	case x.acceptedArm == "":
	case acknowledgement:
		conditions = append(conditions, protocol.ReasonLateAcknowledgement)
	default:

		conditions = append(conditions, protocol.ReasonAlreadyResolved)
	}
	if acknowledgement && x.acked {
		conditions = append(conditions, protocol.ReasonRepeatedAcknowledgement)
	}
	return protocol.HighestResolveReason(conditions...)
}

func (s *state) controlResolveResponse(i, line int, e protocol.Envelope) {
	var p protocol.ActionCallResolveResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, p.RunID)
	if e.ToolCallID != "" && e.ToolCallID != p.ToolCallID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "envelope and payload tool_call_id differ", string(e.ToolCallID), string(p.ToolCallID))
	}
	s.feature(i, line, e, "tools")
	pending := s.pendingResolves[e.InReplyTo]
	if pending == nil {
		return
	}
	x := s.lookupInteraction(pending.run, pending.interaction)
	if x != nil && x.opaque {

		if p.Accepted && pending.arm == protocol.ResolveArmAcknowledge {
			x.acked = true
		}
		return
	}
	if pending.opaque {

		return
	}
	if x == nil {

		switch {
		case p.Accepted:
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/accepted", "a resolution of no pending interaction was accepted", "a refusal reporting "+string(protocol.ReasonUnknownInteraction), "accepted", string(e.InReplyTo))
		case p.Reason != protocol.ReasonUnknownInteraction:
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/reason", "a refusal names a condition other than the highest one the request satisfies", string(protocol.ReasonUnknownInteraction), string(p.Reason), string(e.InReplyTo))
		}
		return
	}
	if p.Accepted {
		if pending.reason != "" {
			s.addExpected(resolveReasonDiagnostic(pending.reason), i, line, e, "/payload/accepted", "a resolution the interaction's state forbids was accepted", "a refusal reporting "+string(pending.reason), "accepted", string(e.InReplyTo))
		}

		s.acceptResolution(e, pending, x)
		return
	}
	if pending.reason == "" {

		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/accepted", "a valid resolution was refused", "accepted", "refused with "+string(p.Reason), string(e.InReplyTo))
		return
	}
	if p.Reason != pending.reason {
		s.addExpected(resolveReasonDiagnostic(pending.reason), i, line, e, "/payload/reason", "a refusal names a condition other than the highest one the request satisfies", string(pending.reason), string(p.Reason), string(e.InReplyTo))
		return
	}
	if p.Reason == protocol.ReasonAlreadyResolved {
		settlement := protocol.EnvelopeID("")
		if p.Details != nil {
			settlement = p.Details.SettlementID
		}
		if !x.settlements[settlement] {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/details/settlement_id", "an already_resolved refusal names an envelope that settled nothing for its interaction", describeIDStrings(settlementIDs(x)), string(settlement), string(e.InReplyTo))
		}
	}
}

func (s *state) acceptResolution(e protocol.Envelope, pending *pendingResolve, x *interactionState) {
	var p protocol.ActionCallResolveRequest
	if req := s.requests[e.InReplyTo]; req != nil {
		_ = req.envelope.DecodePayload(&p)
	}
	if x.settlements == nil {
		x.settlements = map[protocol.EnvelopeID]bool{}
	}
	switch pending.arm {
	case protocol.ResolveArmAcknowledge:
		x.acked = true
	case protocol.ResolveArmResult, protocol.ResolveArmError:
		if pending.arm == protocol.ResolveArmResult {
			x.acceptedArm, x.acceptedResult = protocol.ResolveArmResult, p.Result
		} else {
			x.acceptedArm, x.acceptedError = protocol.ResolveArmError, p.Error
		}

		x.settlements[e.ID] = true
	}
}

func settlementIDs(x *interactionState) []string {
	ids := make([]string, 0, len(x.settlements))
	for id := range x.settlements {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	return ids
}

func describeIDStrings(ids []string) string {
	if len(ids) == 0 {
		return "no settlement"
	}
	joined := ids[0]
	for _, id := range ids[1:] {
		joined += "," + id
	}
	return joined
}

func (s *state) checkCallOwner(i, line int, e protocol.Envelope) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	if p.Name == "" {
		return
	}
	owner, known := s.ownerInForce(s.sessions[p.SessionID], p.Name)
	if !known || owner == p.ExecutionOwner {
		return
	}
	s.addExpected(CodeWrongToolOwner, i, line, e, "/payload/execution_owner", "a call names an execution owner other than the one the catalog in force records for the tool", string(owner), string(p.ExecutionOwner), p.Name)
}

func (s *state) ownerInForce(track *sessionTrack, name string) (protocol.ParticipantID, bool) {
	if track != nil {
		if tool, ok := track.provided[name]; ok {
			return tool.ExecutionOwner, true
		}
		if track.toolCatalog != nil && track.toolCatalog.revision == s.currentCapability {
			owner, ok := track.toolCatalog.owners[name]
			return owner, ok
		}
	}
	owner, ok := s.descriptorOwners[name]
	return owner, ok
}

func (s *state) provideExpectations(p protocol.SessionOpenRequest) (defects []*controlExpectation, limit *controlExpectation, honour bool) {
	key := protocol.FeatureToolsProvide
	support := s.featureDetail(key)
	level := s.features[key]
	unsatisfiable := func(pointer, detailName, detailValue, diagnostic, message string) *controlExpectation {
		return &controlExpectation{
			rung: rungUnsatisfiable, key: key, pointer: pointer,
			code: errorUnsupportedFeature, reason: reasonUnsatisfiable,
			detailName: detailName, detailValue: detailValue,
			diagnostic: diagnostic, message: message,
		}
	}
	if !affirmative(level) {
		return []*controlExpectation{{
			rung: rungCapability, key: key, pointer: "/payload/tools",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: key,
			diagnostic: CodeUnavailableCapability,
			message:    "an open supplies control-layer tools to an endpoint that has not affirmatively advertised provisioning",
		}}, nil, false
	}
	if level == protocol.SupportDegraded && !p.AllowsDegraded(key) {
		return []*controlExpectation{{
			rung: rungDegradation, key: key, pointer: "/payload/tools",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: key,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "an open supplies control-layer tools under a degraded capability without the caller's opt-in",
		}}, nil, false
	}

	attached := map[string]bool{}
	for _, attachment := range p.ToolSources {
		attached[attachment.ID] = true
	}
	native := map[string]bool{}
	if s.catalogKnown {
		for _, name := range s.catalog {
			native[name] = true
		}
	}
	seen := map[string]bool{}
	for index, tool := range p.Tools {
		pointer := fmt.Sprintf("/payload/tools/%d", index)
		if !s.ownedByOpener(tool.ExecutionOwner) {
			defects = append(defects, unsatisfiable(pointer+"/execution_owner", "tool", tool.Name, CodeWrongToolOwner,
				"an open supplies a tool whose execution owner is not the opening participant"))
		}
		if seen[tool.Name] || native[tool.Name] {
			defects = append(defects, unsatisfiable(pointer+"/name", "tool", tool.Name, CodeDuplicateToolName,
				"an open supplies a tool under a name the session's catalog already resolves"))
		}
		seen[tool.Name] = true
		if tool.Source == "" {
			continue
		}
		if _, declared := s.declaredSources[tool.Source]; !declared && !attached[tool.Source] {
			defects = append(defects, unsatisfiable(pointer+"/source", "source", tool.Source, CodeUnmatchedToolSource,
				"an open supplies a tool naming a source neither the descriptor nor the same open declares"))
		}
	}
	if len(defects) > 0 {
		return defects, nil, false
	}
	if violation := provideLimitViolation(support, p.Tools); violation != nil {
		return nil, violation, false
	}
	return nil, nil, true
}

func (s *state) ownedByOpener(owner protocol.ParticipantID) bool {
	return s.controlParticipant == "" || owner == s.controlParticipant
}

func provideLimitViolation(support protocol.FeatureSupport, tools []protocol.ToolDefinition) *controlExpectation {
	refusal := func(pointer, tool, message string) *controlExpectation {
		return &controlExpectation{
			rung: rungUnsatisfiable, key: protocol.FeatureToolsProvide, pointer: pointer,
			code: errorUnsupportedFeature, reason: reasonUnsatisfiable,
			detailName: "tool", detailValue: tool,
			diagnostic: CodeUnavailableCapability, message: message,
		}
	}
	var violations []*controlExpectation
	if max, ok := support.MaxTools(); ok && len(tools) > max {

		violations = append(violations, refusal(
			fmt.Sprintf("/payload/tools/%d", max), tools[max].Name,
			"an open supplies more tools than the endpoint disclosed it accepts",
		))
	}
	if pattern, ok := support.NamePattern(); ok {
		if expression, err := regexp.Compile(pattern); err == nil {
			for index, tool := range tools {
				if !expression.MatchString(tool.Name) {
					violations = append(violations, refusal(
						fmt.Sprintf("/payload/tools/%d/name", index), tool.Name,
						"an open supplies a tool whose name is outside the shape the endpoint disclosed",
					))
				}
			}
		}
	}
	if dialect, ok := support.SchemaDialect(); ok {
		for index, tool := range tools {
			declared, stated := schemaDialect(tool.InputSchema)
			if stated && declared != dialect {
				violations = append(violations, refusal(
					fmt.Sprintf("/payload/tools/%d/input_schema", index), tool.Name,
					"an open supplies a tool whose input schema declares a dialect the endpoint did not disclose",
				))
			}
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.SliceStable(violations, func(a, b int) bool { return violations[a].less(violations[b]) })
	return violations[0]
}

func schemaDialect(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var document struct {
		Schema string `json:"$schema"`
	}
	if json.Unmarshal(raw, &document) != nil || document.Schema == "" {
		return "", false
	}
	return document.Schema, true
}

func (s *state) recordProvidedTools(track *sessionTrack, tools []protocol.ToolDefinition) {
	if len(tools) == 0 {
		return
	}
	if track.provided == nil {
		track.provided = map[string]protocol.ToolDefinition{}
	}
	for _, tool := range tools {
		if _, ok := track.provided[tool.Name]; !ok {
			track.providedOrder = append(track.providedOrder, tool.Name)
		}
		track.provided[tool.Name] = tool
	}
}

func (s *state) checkCatalogProvided(i, line int, e protocol.Envelope, session protocol.SessionID, track *sessionTrack, listed []protocol.ToolDefinition) {
	if track == nil || len(track.providedOrder) == 0 {
		return
	}
	published := make(map[string]protocol.ToolDefinition, len(listed))
	for _, tool := range listed {
		if _, seen := published[tool.Name]; !seen {
			published[tool.Name] = tool
		}
	}
	for _, name := range track.providedOrder {
		supplied := track.provided[name]
		got, ok := published[name]
		switch {
		case !ok:
			s.addExpected(CodeCatalogMismatch, i, line, e, "/payload/tools", "a session catalog omits a tool the open provided", name, "absent", string(session))
		case !describesProvidedTool(supplied, got):
			s.addExpected(CodeCatalogMismatch, i, line, e, "/payload/tools", "a session catalog describes a provided tool differently", describeTool(supplied), describeTool(got), string(session))
		}
	}
}

func describesProvidedTool(supplied, listed protocol.ToolDefinition) bool {
	return supplied.Description == listed.Description &&
		supplied.ExecutionOwner == listed.ExecutionOwner &&
		supplied.Source == listed.Source &&
		canonicalJSON(supplied.InputSchema) == canonicalJSON(listed.InputSchema)
}

func describeTool(tool protocol.ToolDefinition) string {
	return fmt.Sprintf("%s owner=%s source=%s input_schema=%s", tool.Name, tool.ExecutionOwner, tool.Source, canonicalJSON(tool.InputSchema))
}

func (s *state) checkRefreshAgainstProvided(i, line int, e protocol.Envelope, declared map[string]protocol.ToolSourceDescriptor, native map[string]bool) {
	for _, id := range s.sessionIDsInOrder() {
		track := s.sessions[id]
		for _, name := range track.providedOrder {
			if native[name] {
				s.addExpected(CodeDuplicateToolName, i, line, e, "/payload/tools", "a refreshed descriptor declares a native tool under a name an open session provided", "one tool per name", name, string(id))
			}
			source := track.provided[name].Source
			if source == "" {
				continue
			}
			if _, ok := declared[source]; ok {
				continue
			}
			if _, attached := track.attached[source]; attached {
				continue
			}
			s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/sources", "a refreshed descriptor no longer declares a source a provided tool references", source, "absent", string(id), name)
		}
	}
}

func (s *state) checkEntryAcknowledged(i, line int, e protocol.Envelope, pointer string, entry protocol.ActiveRun, r *runState, want map[protocol.InteractionID]bool) {
	listed := map[protocol.InteractionID]bool{}
	for _, id := range entry.PendingInteractions {
		listed[id] = true
	}
	acknowledged := map[protocol.InteractionID]bool{}
	for _, id := range entry.AcknowledgedInteractions {
		acknowledged[id] = true
		if !listed[id] {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/acknowledged_interactions", "an acknowledged interaction is not among the entry's pending interactions", describeIDs(listed), string(id), string(r.id))
			continue
		}
		if x := r.interactions[id]; x == nil || (!x.opaque && !x.acked) {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/acknowledged_interactions", "an entry reports an acknowledgement the trace never saw accepted", "an accepted started acknowledgement", string(id), string(r.id))
		}
	}
	if want == nil {
		return
	}
	for id := range want {
		x := r.interactions[id]

		if x == nil || !x.acked || acknowledged[id] {
			continue
		}
		s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/acknowledged_interactions", "an entry omits an interaction whose acknowledgement the endpoint accepted", string(id), describeIDs(acknowledged), string(r.id))
	}
}
