package validation

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The tool-sources unit (T3a catalog with sources, T3b attachment at session
// open). A source is described, not managed: the harness runs the client, OAP
// says what the source is, attaches it at open, and observes its calls.
//
// Every gate here is judged on the correlated response, never on the request,
// exactly as the run-controls gate is and for the same reason: an endpoint
// that advertises neither key may still be asked for a catalog, and answering
// with a typed refusal is the correct behaviour — diagnosing the request would
// fail the conduct the wire mandates.

// attachmentOnlyMembers are the members ToolSourceAttachment carries and
// ToolSourceDescriptor does not. One of them can hold a literal credential, so
// a published source carrying any of them is a leak whatever serializer
// produced it.
var attachmentOnlyMembers = []string{"command", "args", "environment"}

// pendingList is what one action.tools.list.request left for its correlated
// response to settle.
type pendingList struct {
	index, line int
	session     protocol.SessionID
	scoped      bool
	expectation *controlExpectation
	// honour marks a request the endpoint advertises the catalog for and that
	// carries no defect any rule names. Refusing it is unhonoured_capability:
	// an endpoint that advertises a catalog and refuses every request for one
	// honours nothing.
	honour bool
}

// pendingOpen is what one session.open.request carrying tool_sources left for
// its correlated response to settle.
type pendingOpen struct {
	index, line int
	attachments []protocol.ToolSourceAttachment
	expectation *controlExpectation
	// withinLimits marks an attachment array that violates no limit the
	// endpoint disclosed, so refusing it is undisclosed_attach_limit.
	withinLimits bool
	// limitRefusal is the shape a refusal must take when the array does
	// violate a disclosed limit. It is not an expectation: exceeding a limit
	// permits a refusal without requiring one, since a limit is the endpoint's
	// own disclosure and admitting more than it promised breaks nothing a
	// caller relied on. So an admitted open owes nothing here, while a refused
	// one still owes a refusal that says which source to drop.
	limitRefusal *controlExpectation
}

// sessionCatalog is the last catalog one session was served under the active
// capability revision: the source a call's attribution is checked against.
type sessionCatalog struct {
	revision string
	sources  map[string]bool
	tools    map[string]string
}

// toolSourceMap indexes descriptors by id, reporting the first duplicate.
func toolSourceMap(sources []protocol.ToolSourceDescriptor) (map[string]protocol.ToolSourceDescriptor, string) {
	indexed := make(map[string]protocol.ToolSourceDescriptor, len(sources))
	duplicate := ""
	for _, source := range sources {
		if _, seen := indexed[source.ID]; seen && duplicate == "" {
			duplicate = source.ID
			continue
		}
		indexed[source.ID] = source
	}
	return indexed, duplicate
}

// descriptorSources normalizes a capability descriptor's declared sources: its
// top-level `sources` and every layer's, since a valid descriptor may declare
// them under a layer alone, exactly as its catalog may be published there.
func descriptorSources(p protocol.CapabilitiesResponse) []protocol.ToolSourceDescriptor {
	sources := append([]protocol.ToolSourceDescriptor(nil), p.Sources...)
	layers := make([]string, 0, len(p.Layers))
	for name := range p.Layers {
		layers = append(layers, name)
	}
	sort.Strings(layers)
	for _, name := range layers {
		sources = append(sources, p.Layers[name].Sources...)
	}
	return sources
}

// descriptorTools normalizes a descriptor's effective catalog entries, the way
// collectCatalog normalizes their names.
func descriptorTools(p protocol.CapabilitiesResponse) []protocol.ToolDefinition {
	tools := append([]protocol.ToolDefinition(nil), p.Tools...)
	layers := make([]string, 0, len(p.Layers))
	for name := range p.Layers {
		layers = append(layers, name)
	}
	sort.Strings(layers)
	for _, name := range layers {
		tools = append(tools, p.Layers[name].Tools...)
	}
	return tools
}

// checkDescriptorSources judges a capability descriptor's own catalog before
// any list, open, or call is judged against it: a descriptor that is ambiguous
// or dangling on its own cannot resolve a tool to one source or one owner, and
// a call that agrees with a dangling entry is not excused by that agreement.
func (s *state) checkDescriptorSources(i, line int, e protocol.Envelope, p protocol.CapabilitiesResponse) {
	s.checkPublishedSources(i, line, e)
	sources := descriptorSources(p)
	declared, duplicate := toolSourceMap(sources)
	s.declaredSources = declared
	if duplicate != "" {
		s.addExpected(CodeDuplicateToolSource, i, line, e, "/payload/sources", "the descriptor declares two tool sources with one id", "one source per id", duplicate)
	}
	tools := descriptorTools(p)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	s.catalogAmbiguous = false
	if name := duplicateToolName(names); name != "" {
		s.catalogAmbiguous = true
		s.addExpected(CodeDuplicateToolName, i, line, e, "/payload/tools", "the descriptor's effective catalog lists two tools with one name", "one tool per name", name)
	}
	// The attribution the descriptor publishes is retained with its sources. A
	// duplicate name makes the mapping meaningless, and the descriptor already
	// carries its own diagnostic for that, so an ambiguous catalog attributes
	// nothing rather than attributing arbitrarily.
	s.descriptorAttribution = nil
	if !s.catalogAmbiguous {
		s.descriptorAttribution = make(map[string]string, len(tools))
		for _, tool := range tools {
			if tool.Source != "" {
				s.descriptorAttribution[tool.Name] = tool.Source
			}
		}
	}
	for _, tool := range tools {
		if tool.Source == "" {
			continue
		}
		if _, ok := declared[tool.Source]; !ok {
			s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/tools", "a descriptor tool names a source the descriptor does not declare", "a declared source", tool.Source, tool.Name)
		}
	}
	// A refresh must keep every attached source resolvable: a new descriptor
	// that declares an id one of the open sessions attached makes that id
	// ambiguous, and a post-refresh list is not mandatory, so it would
	// otherwise go unnoticed until a call resolved to the wrong endpoint.
	for _, id := range s.sessionIDsInOrder() {
		track := s.sessions[id]
		for _, attached := range track.attachedOrder {
			if _, ok := declared[attached]; ok {
				s.addExpected(CodeDuplicateToolSource, i, line, e, "/payload/sources", "a refreshed descriptor declares a source an open session already attached", "one source per id", attached, string(id))
			}
		}
	}
}

// sessionIDsInOrder gives the tracked sessions a stable order, so a descriptor
// naming two sessions' attachments diagnoses them in one order every run.
func (s *state) sessionIDsInOrder() []protocol.SessionID {
	ids := make([]protocol.SessionID, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	return ids
}

// toolsListRequest retains what one catalog request owes its response. The
// missing-descriptor and stale-revision branches are diagnosed on the request,
// as they are for every optional envelope, and nothing is retained for a key
// whose level could not be read at all.
func (s *state) toolsListRequest(i, line int, e protocol.Envelope) {
	var p protocol.ToolsListRequest
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	session := p.SessionID
	if session == "" {
		session = e.SessionID
	}
	pending := &pendingList{index: i, line: line, session: session, scoped: session != ""}
	defer func() { s.pendingLists[e.ID] = pending }()
	level, judged := s.controlDescriptor(i, line, e, protocol.FeatureToolsList)
	if !judged {
		return
	}
	switch {
	case !affirmative(level):
		pending.expectation = &controlExpectation{
			rung: rungCapability, key: protocol.FeatureToolsList, pointer: "/payload",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: protocol.FeatureToolsList,
			diagnostic: CodeUnavailableCapability,
			message:    "a catalog was requested from an endpoint that has not affirmatively advertised one",
		}
	case level == protocol.SupportDegraded && !p.AllowsDegraded(protocol.FeatureToolsList):
		pending.expectation = &controlExpectation{
			rung: rungDegradation, key: protocol.FeatureToolsList, pointer: "/payload",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureToolsList,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "a degraded catalog was requested without the caller's opt-in",
		}
	default:
		pending.honour = true
	}
}

// toolsListResponse judges one served catalog: the gate it was owed, the scope
// it must answer in, and the catalog's own resolvability.
func (s *state) toolsListResponse(i, line int, e protocol.Envelope) {
	var p protocol.ToolsListResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	pending := s.pendingLists[e.InReplyTo]
	if pending != nil && pending.expectation != nil {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload", pending.expectation.message, "a typed refusal naming "+pending.expectation.key, "a served catalog", string(e.InReplyTo))
	}
	if pending != nil && pending.scoped && p.SessionID == pending.session && e.SessionID != pending.session {
		// A session-scoped catalog must name its session on the envelope as
		// well as in the payload, or a consumer routing by envelope scope
		// cannot tie the catalog to the attachment it reflects.
		s.addExpected(CodeScopeMismatch, i, line, e, "/session_id", "a session-scoped catalog must name its session on the envelope", string(pending.session), string(e.SessionID), string(e.InReplyTo))
	}
	s.checkPublishedSources(i, line, e)
	declared, duplicate := toolSourceMap(p.Sources)
	if duplicate != "" {
		s.addExpected(CodeDuplicateToolSource, i, line, e, "/payload/sources", "a catalog declares two tool sources with one id", "one source per id", duplicate)
	}
	names := make([]string, 0, len(p.Tools))
	for _, tool := range p.Tools {
		names = append(names, tool.Name)
	}
	if name := duplicateToolName(names); name != "" {
		s.addExpected(CodeDuplicateToolName, i, line, e, "/payload/tools", "a catalog lists two tools with one name", "one tool per name", name)
	}
	// A served catalog attributes every tool it lists. `source` stays optional
	// in the schema, because a descriptor published by an endpoint outside this
	// unit carries tools with no attribution and must keep validating — but an
	// action.tools.list.response is this unit's own envelope, served only by an
	// endpoint advertising action.tools.list, and attribution is the whole of
	// what that key adds. A catalog whose tools name no source is the flat list
	// the unit exists to replace, and a consumer cannot resolve any of it.
	//
	// A catalog the endpoint owed a refusal for is exempt: the serving is the
	// defect, and judging the contents of a response that should not exist piles
	// consequences onto one fault.
	gated := pending != nil && pending.expectation != nil
	for _, tool := range p.Tools {
		switch {
		case tool.Source == "":
			if !gated {
				s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/tools", "a served catalog lists a tool that names no source", "a declared source", "none", tool.Name)
			}
		default:
			if _, ok := declared[tool.Source]; !ok {
				s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/tools", "a listed tool names a source the same response does not declare", "a declared source", tool.Source, tool.Name)
			}
		}
	}
	if p.SessionID == "" {
		return
	}
	track := s.sessions[p.SessionID]
	if track == nil {
		track = &sessionTrack{}
		s.sessions[p.SessionID] = track
	}
	// Attachment is for the session's lifetime, so an attached source is
	// listed with the members it was attached with in every later catalog.
	// Native entries may differ between lists — a harness refreshes its own
	// catalog — but the open-time entries never drop out and never change.
	for _, id := range track.attachedOrder {
		attached := track.attached[id]
		listed, ok := declared[id]
		switch {
		case !ok:
			s.addExpected(CodeCatalogMismatch, i, line, e, "/payload/sources", "a session catalog omits a source the open attached", id, "absent", string(p.SessionID))
		case listed != attached:
			s.addExpected(CodeCatalogMismatch, i, line, e, "/payload/sources", "a session catalog describes an attached source differently", describeSource(attached), describeSource(listed), string(p.SessionID))
		}
	}
	catalog := &sessionCatalog{revision: s.currentCapability, sources: map[string]bool{}, tools: map[string]string{}}
	for id := range declared {
		catalog.sources[id] = true
	}
	for _, tool := range p.Tools {
		catalog.tools[tool.Name] = tool.Source
	}
	track.catalog = catalog
}

// describesSource reports whether a published descriptor is a description of
// the same source an attachment stated: the id and the kind agree, and every
// optional member the attachment named is repeated. A member the attachment
// left blank may be supplied, because an attachment is a request to attach and
// not a claim to have described the source completely.
func describesSource(attached, published protocol.ToolSourceDescriptor) bool {
	if attached.ID != published.ID || attached.Kind != published.Kind {
		return false
	}
	for _, member := range [][2]string{
		{attached.DisplayName, published.DisplayName},
		{attached.Protocol, published.Protocol},
		{attached.Endpoint, published.Endpoint},
	} {
		if member[0] != "" && member[0] != member[1] {
			return false
		}
	}
	return true
}

// describeSource renders one descriptor for a diagnostic's expected/actual.
func describeSource(source protocol.ToolSourceDescriptor) string {
	return fmt.Sprintf("%s kind=%s protocol=%s endpoint=%s display_name=%s", source.ID, source.Kind, source.Protocol, source.Endpoint, source.DisplayName)
}

// checkPublishedSources refuses an attachment-only member on a published
// source. The descriptor shape excludes them, so a strict bundle rejects this
// in the schema phase; the rule exists for the tolerant bundle and for a
// hand-rolled serializer, where an implementation reflecting the open-time
// value straight into its catalog would leak a credential and still validate.
//
// It reads the payload's raw JSON rather than a decoded descriptor, because
// decoding is exactly what hides the defect: an attachment-only member
// unmarshals into no field of ToolSourceDescriptor and is gone before any
// semantic check could see it.
//
// Every carrier of a published source runs it, and each runs it over every
// place that carrier may publish one. A capability descriptor may declare its
// sources under a layer alone, as it may its catalog, so a layer's array is
// checked with the top-level array and under its own pointer. Holding the
// descriptor to a weaker rule than the list, open, and state responses would
// leave the hole exactly where a source is first published.
func (s *state) checkPublishedSources(i, line int, e protocol.Envelope) {
	var raw struct {
		Sources []map[string]json.RawMessage `json:"sources"`
		Layers  map[string]struct {
			Sources []map[string]json.RawMessage `json:"sources"`
		} `json:"layers"`
	}
	if e.DecodePayload(&raw) != nil {
		return
	}
	s.checkRawSources(i, line, e, "/payload/sources", raw.Sources)
	layers := make([]string, 0, len(raw.Layers))
	for name := range raw.Layers {
		layers = append(layers, name)
	}
	sort.Strings(layers)
	for _, name := range layers {
		s.checkRawSources(i, line, e, "/payload/layers/"+escapePointerToken(name)+"/sources", raw.Layers[name].Sources)
	}
}

// checkRawSources judges one published `sources` array, whatever carries it.
func (s *state) checkRawSources(i, line int, e protocol.Envelope, pointer string, sources []map[string]json.RawMessage) {
	for index, source := range sources {
		for _, member := range attachmentOnlyMembers {
			if _, ok := source[member]; ok {
				s.addExpected(CodeAttachmentFieldInCatalog, i, line, e, fmt.Sprintf("%s/%d/%s", pointer, index, member), "a published tool source carries an attachment-only member", "no command, args, or environment", member)
			}
		}
	}
}

// escapePointerToken encodes one JSON Pointer reference token (RFC 6901): a
// layer name is an arbitrary object key, so a diagnostic pointing at it must
// escape the two characters a pointer reserves.
func escapePointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

// sessionOpenRequest retains what an open carrying tool_sources owes its
// response: the capability it elects, the consent it needed, and every defect
// the validator can already see in the array.
func (s *state) sessionOpenRequest(i, line int, e protocol.Envelope) {
	var p protocol.SessionOpenRequest
	_ = e.DecodePayload(&p)
	if len(p.ToolSources) == 0 {
		return
	}
	pending := &pendingOpen{index: i, line: line, attachments: p.ToolSources}
	defer func() { s.pendingOpens[e.ID] = pending }()
	level, judged := s.controlDescriptor(i, line, e, protocol.FeatureToolSourcesAttach)
	if !judged {
		return
	}
	support := s.featureDetail(protocol.FeatureToolSourcesAttach)
	// An attach capability that discloses no session-open mode cannot admit an
	// attachment at session open, whatever its level says: the mode is the
	// disclosure that makes the key usable, so a descriptor without it is one
	// no caller can attach against, and the capability rung owns the answer.
	// This is also what keeps `remote` expressible — the modes are a set, so
	// disclosing remote never erases session_open.
	if !affirmative(level) || !support.DisclosesMode(protocol.ModeSessionOpen) {
		// The capability rung owns the response: a caller told the capability
		// is missing has no use for a detail about one of its modes, and a
		// single error.response cannot carry both `unadvertised` and
		// `unsatisfiable`. Every rung-3 expectation below is discharged.
		pending.expectation = &controlExpectation{
			rung: rungCapability, key: protocol.FeatureToolSourcesAttach, pointer: "/payload/tool_sources",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: protocol.FeatureToolSourcesAttach,
			diagnostic: CodeUnavailableCapability,
			message:    "an open attaches tool sources to an endpoint that has not affirmatively advertised attachment",
		}
		return
	}
	if level == protocol.SupportDegraded && !p.AllowsDegraded(protocol.FeatureToolSourcesAttach) {
		pending.expectation = &controlExpectation{
			rung: rungDegradation, key: protocol.FeatureToolSourcesAttach, pointer: "/payload/tool_sources",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureToolSourcesAttach,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "an open attaches tool sources under a degraded capability without the caller's opt-in",
		}
		return
	}
	var defects []*controlExpectation
	seen := map[string]bool{}
	for index, attachment := range p.ToolSources {
		_, declared := s.declaredSources[attachment.ID]
		if seen[attachment.ID] || declared {
			defects = append(defects, &controlExpectation{
				rung: rungUnsatisfiable, key: protocol.FeatureToolSourcesAttach,
				pointer: fmt.Sprintf("/payload/tool_sources/%d/id", index),
				code:    errorUnsupportedFeature, reason: reasonUnsatisfiable,
				detailName: "source", detailValue: attachment.ID,
				diagnostic: CodeDuplicateToolSource,
				message:    "an open attaches a source under an id the session's catalog already resolves",
			})
		}
		seen[attachment.ID] = true
		if attachment.Kind == protocol.ToolSourceRemote && !support.DisclosesMode(protocol.ModeRemote) {
			// A remote source reaches an endpoint the operator never
			// configured, so it is gated on its own disclosed mode rather
			// than on attachment alone.
			defects = append(defects, &controlExpectation{
				rung: rungUnsatisfiable, key: protocol.FeatureToolSourcesAttach,
				pointer: fmt.Sprintf("/payload/tool_sources/%d/kind", index),
				code:    errorUnsupportedFeature, reason: reasonUnsatisfiable,
				detailName: "source", detailValue: attachment.ID,
				diagnostic: CodeUnavailableCapability,
				message:    "an open attaches a remote source to an endpoint whose attach capability does not disclose the remote mode",
			})
		}
	}
	sort.SliceStable(defects, func(a, b int) bool { return defects[a].less(defects[b]) })
	if len(defects) > 0 {
		pending.expectation = defects[0]
		return
	}
	if violation := limitViolation(support, p.ToolSources); violation != nil {
		pending.limitRefusal = violation
		return
	}
	pending.withinLimits = true
}

// limitViolation names the first limit an attachment array puts outside what
// the endpoint disclosed, and nil when it violates none. An endpoint that
// discloses nothing is held to accepting every well-formed array, because a
// refusal is then the evidence that a constraint exists which the caller was
// never told about.
//
// It returns the refusal such an array is owed rather than a bare bool. Being
// outside a disclosed limit is an unsatisfiability — the capability is
// advertised and usable, and this request's value is the thing that cannot be
// honoured — so the refusal takes the same shape every other unsatisfiable
// attachment defect takes: `unsupported_feature`, `details.feature` naming the
// attach key, `details.reason: "unsatisfiable"`, and `details.source` naming
// the entry to drop. Without that, being over the limit was the one branch
// where an endpoint could refuse with any code at all and pass, which is
// exactly the outcome disclosing a limit is supposed to prevent.
//
// Two violations in one array are ordered by the refusal precedence, which on
// one rung and one key is the lower JSON Pointer, so two encodings of one
// request owe the same refusal.
func limitViolation(support protocol.FeatureSupport, attachments []protocol.ToolSourceAttachment) *controlExpectation {
	refusal := func(pointer, source, message string) *controlExpectation {
		return &controlExpectation{
			rung: rungUnsatisfiable, key: protocol.FeatureToolSourcesAttach, pointer: pointer,
			code: errorUnsupportedFeature, reason: reasonUnsatisfiable,
			detailName: "source", detailValue: source,
			diagnostic: CodeUnavailableCapability, message: message,
		}
	}
	var violations []*controlExpectation
	if max, ok := support.MaxSources(); ok && len(attachments) > max {
		// The entry that carries the array past the ceiling is the one a
		// caller drops to get under it.
		violations = append(violations, refusal(
			fmt.Sprintf("/payload/tool_sources/%d", max), attachments[max].ID,
			"an open attaches more sources than the endpoint disclosed it accepts",
		))
	}
	if transports, ok := support.Transports(); ok {
		for index, attachment := range attachments {
			if !slices.Contains(transports, attachment.Kind) {
				violations = append(violations, refusal(
					fmt.Sprintf("/payload/tool_sources/%d/kind", index), attachment.ID,
					"an open attaches a source whose kind is outside the transports the endpoint disclosed",
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

// sessionOpenResponse settles the attachment gate and records the session's
// attached sources, which are session-lifetime facts every later catalog and
// snapshot is held to.
func (s *state) sessionOpenResponse(i, line int, e protocol.Envelope, p protocol.SessionOpenResponse) {
	s.checkPublishedSources(i, line, e)
	pending := s.pendingOpens[e.InReplyTo]
	if pending == nil {
		s.checkPublishedUnion(i, line, e, p.SessionID, p.Sources, true)
		return
	}
	if pending.expectation != nil {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload", pending.expectation.message, "a typed refusal naming "+pending.expectation.key, "an admitted open", string(e.InReplyTo))
		// The admission itself is the defect; recording an attachment the
		// endpoint should have refused would pile consequences onto one fault.
		return
	}
	track := s.sessions[p.SessionID]
	if track == nil {
		track = &sessionTrack{}
		s.sessions[p.SessionID] = track
	}
	if track.attached == nil {
		track.attached = map[string]protocol.ToolSourceDescriptor{}
	}
	for _, attachment := range pending.attachments {
		if _, ok := track.attached[attachment.ID]; !ok {
			track.attachedOrder = append(track.attachedOrder, attachment.ID)
		}
		track.attached[attachment.ID] = attachment.Descriptor()
	}
	s.checkPublishedUnion(i, line, e, p.SessionID, p.Sources, true)
}

// checkPublishedUnion holds a session snapshot's sources to the union of the
// open's attachments and the descriptor's declared sources, compared by id and
// by each descriptor's published members. The check runs only for a session
// whose open attached sources: without an attachment a snapshot can hide
// nothing the descriptor does not already publish.
//
// adopt is set for the open response alone, and it is what lets an endpoint
// know more about a source than the caller did. An attachment states an id and
// a kind and may state nothing else — over the daemon a caller names an
// operator-configured source by id, and the display name, protocol, and
// endpoint come from the operator's registry, which is the only copy a wire
// caller is allowed to influence. So the open response is held to agreeing
// with what the attachment *stated* and may fill what it left blank; the
// descriptor it publishes is then adopted as the session's, and every later
// snapshot and catalog is held to that, exactly. Both halves of the lifetime
// rule survive: an endpoint cannot contradict what the caller asked for, and
// once it has described a source it cannot redescribe it.
func (s *state) checkPublishedUnion(i, line int, e protocol.Envelope, session protocol.SessionID, published []protocol.ToolSourceDescriptor, adopt bool) {
	track := s.sessions[session]
	if track == nil || len(track.attachedOrder) == 0 {
		return
	}
	expected := map[string]protocol.ToolSourceDescriptor{}
	for id, source := range s.declaredSources {
		expected[id] = source
	}
	for _, id := range track.attachedOrder {
		expected[id] = track.attached[id]
	}
	reported, duplicate := toolSourceMap(published)
	if duplicate != "" {
		// One id resolves to one source. Two entries under one id leave the
		// union ambiguous whatever else agrees: the loops below would compare
		// the first and never see the second, so a snapshot could list an
		// attached source twice with different members and pass.
		s.addExpected(CodeDuplicateToolSource, i, line, e, "/payload/sources", "a session snapshot declares two tool sources with one id", "one source per id", duplicate)
	}
	ids := make([]string, 0, len(expected))
	for id := range expected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		got, ok := reported[id]
		switch {
		case !ok:
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/sources", "session state omits an attached or declared tool source", id, "absent", string(session))
		case adopt && !describesSource(expected[id], got):
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/sources", "session state contradicts a member the attachment stated", describeSource(expected[id]), describeSource(got), string(session))
		case !adopt && got != expected[id]:
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/sources", "session state describes a tool source differently from the attachment it reflects", describeSource(expected[id]), describeSource(got), string(session))
		case adopt:
			// Accepted as published: this is now the session's description of
			// the source, and every later snapshot and catalog is held to it.
			if track.attached != nil {
				if _, attached := track.attached[id]; attached {
					track.attached[id] = got
				}
			}
		}
	}
	for _, source := range published {
		if _, ok := expected[source.ID]; !ok {
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/sources", "session state reports a tool source that was never attached or declared", "an attached or declared source", source.ID, string(session))
		}
	}
}

// settleToolSourceRefusal judges a correlated error.response against what a
// catalog request or an attaching open owed. A refusal under a code, feature,
// reason, or detail that does not tell the caller what to change is the same
// defect as no refusal at all; and a request the endpoint advertises and the
// validator finds defect-free, refused anyway, is the endpoint honouring
// nothing it advertised.
func (s *state) settleToolSourceRefusal(i, line int, e protocol.Envelope) {
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	if pending := s.pendingLists[e.InReplyTo]; pending != nil {
		switch {
		case pending.expectation != nil:
			if !refusalConforms(payload.Error, pending.expectation) {
				s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", pending.expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
			}
		case pending.honour:
			s.addExpected(CodeUnhonouredCapability, i, line, e, "/payload/error", "a catalog request within every disclosed constraint was refused by an endpoint advertising the catalog", "a served catalog", describeRefusal(payload.Error), string(e.InReplyTo))
		}
	}
	if pending := s.pendingOpens[e.InReplyTo]; pending != nil {
		switch {
		case pending.expectation != nil:
			if !refusalConforms(payload.Error, pending.expectation) {
				s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", pending.expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
			}
		case pending.limitRefusal != nil:
			// Refusing is permitted here and admitting is too, but a refusal
			// still has to say which source to drop: "over the limit" is only
			// actionable when the caller is told which entry put it there.
			if !refusalConforms(payload.Error, pending.limitRefusal) {
				s.addExpected(pending.limitRefusal.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", pending.limitRefusal.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
			}
		case pending.withinLimits:
			s.addExpected(CodeUndisclosedAttachLimit, i, line, e, "/payload/error", "an attachment carrying no defect and violating no disclosed limit was refused", "an admitted open or a disclosed limit", describeRefusal(payload.Error), string(e.InReplyTo))
		}
	}
}

// checkCallSource judges one requested call's attribution against the catalog.
// A call carrying `source` must name the source the session's catalog records
// for that tool, or a declared source when no catalog lists the tool at all;
// otherwise the call is attributed to the wrong endpoint, which is exactly what
// `source` exists to prevent a consumer from having to infer from a name.
//
// Three mappings are consulted in order, and the order is which one supersedes
// which. The session's own catalog wins where it has one under the active
// revision. Otherwise the descriptor's catalog answers, because a descriptor
// that publishes a tool under a source has published that attribution and
// nothing has replaced it. Only a tool neither mapping lists falls back to "any
// source this session resolves", which is all that can be said about a tool no
// published catalog names.
//
// It runs on `action.call.requested` alone. The later events of the same call
// carry an optional `name`, so a catalog lookup there could be evaded by
// omitting it; they are held instead to the source this call was requested
// under, which state.tool() retains in the call's own track.
func (s *state) checkCallSource(i, line int, e protocol.Envelope) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	if p.Source == "" {
		return
	}
	track := s.sessions[p.SessionID]
	if track != nil && track.catalog != nil && track.catalog.revision == s.currentCapability {
		if listed, ok := track.catalog.tools[p.Name]; ok {
			if listed != p.Source {
				s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/source", "a call attributes a tool to a source other than the one the session catalog records", listed, p.Source, p.Name)
			}
			return
		}
		if track.catalog.sources[p.Source] {
			return
		}
	}
	// No session catalog has superseded the descriptor, so the descriptor's own
	// attribution is the published one and the call is judged against it. Without
	// this a call before the first list could attribute any tool to any declared
	// source, which is the inference `source` exists to remove: the descriptor
	// said where that tool comes from, and a call may not say otherwise.
	if listed, ok := s.descriptorAttribution[p.Name]; ok {
		if listed != p.Source {
			s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/source", "a call attributes a tool to a source other than the one the descriptor records", listed, p.Source, p.Name)
		}
		return
	}
	if track != nil {
		if _, ok := track.attached[p.Source]; ok {
			return
		}
	}
	if _, ok := s.declaredSources[p.Source]; ok {
		return
	}
	s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/source", "a call names a tool source nothing declares", "a declared source", p.Source, p.Name)
}
