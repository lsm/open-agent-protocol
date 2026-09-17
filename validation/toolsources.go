package validation

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
)

var attachmentOnlyMembers = []string{"command", "args", "environment"}

type pendingList struct {
	index, line int
	session     protocol.SessionID
	scoped      bool
	expectation *controlExpectation

	honour bool
}

type pendingOpen struct {
	index, line int
	attachments []protocol.ToolSourceAttachment
	tools       []protocol.ToolDefinition
	expectation *controlExpectation

	honourDiagnostic string
	honourKey        string

	limitRefusal *controlExpectation
}

type sessionCatalog struct {
	revision string
	sources  map[string]bool
	tools    map[string]string

	owners map[string]protocol.ParticipantID
}

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

func descriptorSources(p protocol.CapabilitiesResponse) []protocol.ToolSourceDescriptor {
	return protocol.CapabilityDescriptor(p).EffectiveSources()
}

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

	s.descriptorAttribution = nil
	s.descriptorOwners = nil
	if !s.catalogAmbiguous {
		s.descriptorAttribution = make(map[string]string, len(tools))
		s.descriptorOwners = make(map[string]protocol.ParticipantID, len(tools))
		for _, tool := range tools {
			if tool.Source != "" {
				s.descriptorAttribution[tool.Name] = tool.Source
			}
			if tool.ExecutionOwner != "" {
				s.descriptorOwners[tool.Name] = tool.ExecutionOwner
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

	for _, id := range s.sessionIDsInOrder() {
		track := s.sessions[id]
		for _, attached := range track.attachedOrder {
			if _, ok := declared[attached]; ok {
				s.addExpected(CodeDuplicateToolSource, i, line, e, "/payload/sources", "a refreshed descriptor declares a source an open session already attached", "one source per id", attached, string(id))
			}
		}
	}

	nativeNames := make(map[string]bool, len(names))
	for _, name := range names {
		nativeNames[name] = true
	}
	s.checkRefreshAgainstProvided(i, line, e, declared, nativeNames)
}

func (s *state) sessionIDsInOrder() []protocol.SessionID {
	ids := make([]protocol.SessionID, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	return ids
}

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

func answeredScope(e protocol.Envelope, p protocol.ToolsListResponse) protocol.SessionID {
	if p.SessionID != "" {
		return p.SessionID
	}
	return e.SessionID
}

func (s *state) toolsListResponse(i, line int, e protocol.Envelope) {
	var p protocol.ToolsListResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, "")
	pending := s.pendingLists[e.InReplyTo]
	if pending != nil && pending.expectation != nil {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload", pending.expectation.message, "a typed refusal naming "+pending.expectation.key, "a served catalog", string(e.InReplyTo))
	}
	if pending != nil && pending.scoped && p.SessionID == pending.session && e.SessionID != pending.session {

		s.addExpected(CodeScopeMismatch, i, line, e, "/session_id", "a session-scoped catalog must name its session on the envelope", string(pending.session), string(e.SessionID), string(e.InReplyTo))
	}
	if pending != nil && !pending.scoped {

		if answered := answeredScope(e, p); answered != "" {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/session_id", "an unscoped catalog request was answered with one session's catalog", "no session", string(answered), string(e.InReplyTo))
		}
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

	s.checkCatalogProvided(i, line, e, p.SessionID, track, p.Tools)
	catalog := &sessionCatalog{revision: s.currentCapability, sources: map[string]bool{}, tools: map[string]string{}, owners: map[string]protocol.ParticipantID{}}
	for id := range declared {
		catalog.sources[id] = true
	}
	for _, tool := range p.Tools {
		catalog.tools[tool.Name] = tool.Source
		catalog.owners[tool.Name] = tool.ExecutionOwner
	}
	track.toolCatalog = catalog
}

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

func describeSource(source protocol.ToolSourceDescriptor) string {
	return fmt.Sprintf("%s kind=%s protocol=%s endpoint=%s display_name=%s", source.ID, source.Kind, source.Protocol, source.Endpoint, source.DisplayName)
}

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

func (s *state) checkRawSources(i, line int, e protocol.Envelope, pointer string, sources []map[string]json.RawMessage) {
	for index, source := range sources {
		for _, member := range attachmentOnlyMembers {
			if _, ok := source[member]; ok {
				s.addExpected(CodeAttachmentFieldInCatalog, i, line, e, fmt.Sprintf("%s/%d/%s", pointer, index, member), "a published tool source carries an attachment-only member", "no command, args, or environment", member)
			}
		}
	}
}

func escapePointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}

func (s *state) sessionOpenRequest(i, line int, e protocol.Envelope) {
	var p protocol.SessionOpenRequest
	_ = e.DecodePayload(&p)
	if len(p.ToolSources) == 0 && len(p.Tools) == 0 {
		return
	}
	pending := &pendingOpen{index: i, line: line, attachments: p.ToolSources, tools: p.Tools}
	defer func() { s.pendingOpens[e.ID] = pending }()

	if _, judged := s.controlDescriptor(i, line, e, protocol.FeatureToolSourcesAttach); !judged {
		return
	}
	var defects []*controlExpectation
	var limits []*controlExpectation
	honours := map[string]string{}
	if len(p.ToolSources) > 0 {
		attachDefects, attachLimit, honour := s.attachExpectations(p)
		defects = append(defects, attachDefects...)
		if attachLimit != nil {
			limits = append(limits, attachLimit)
		}
		if honour {
			honours[protocol.FeatureToolSourcesAttach] = CodeUndisclosedAttachLimit
		}
	}
	if len(p.Tools) > 0 {
		provideDefects, provideLimit, honour := s.provideExpectations(p)
		defects = append(defects, provideDefects...)
		if provideLimit != nil {
			limits = append(limits, provideLimit)
		}
		if honour {
			honours[protocol.FeatureToolsProvide] = CodeUndisclosedProvideLimit
		}
	}
	sort.SliceStable(defects, func(a, b int) bool { return defects[a].less(defects[b]) })
	if len(defects) > 0 {
		pending.expectation = defects[0]
		return
	}
	sort.SliceStable(limits, func(a, b int) bool { return limits[a].less(limits[b]) })
	if len(limits) > 0 {
		pending.limitRefusal = limits[0]
		return
	}

	for _, key := range sortedKeys(honours) {
		pending.honourKey, pending.honourDiagnostic = key, honours[key]
		break
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *state) attachExpectations(p protocol.SessionOpenRequest) (defects []*controlExpectation, limit *controlExpectation, honour bool) {
	level := s.features[protocol.FeatureToolSourcesAttach]
	support := s.featureDetail(protocol.FeatureToolSourcesAttach)

	if !affirmative(level) || !support.DisclosesMode(protocol.ModeSessionOpen) {

		return []*controlExpectation{{
			rung: rungCapability, key: protocol.FeatureToolSourcesAttach, pointer: "/payload/tool_sources",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: protocol.FeatureToolSourcesAttach,
			diagnostic: CodeUnavailableCapability,
			message:    "an open attaches tool sources to an endpoint that has not affirmatively advertised attachment",
		}}, nil, false
	}
	if level == protocol.SupportDegraded && !p.AllowsDegraded(protocol.FeatureToolSourcesAttach) {
		return []*controlExpectation{{
			rung: rungDegradation, key: protocol.FeatureToolSourcesAttach, pointer: "/payload/tool_sources",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureToolSourcesAttach,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "an open attaches tool sources under a degraded capability without the caller's opt-in",
		}}, nil, false
	}
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
	if len(defects) > 0 {
		return defects, nil, false
	}
	if violation := limitViolation(support, p.ToolSources); violation != nil {
		return nil, violation, false
	}
	return nil, nil, true
}

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

func (s *state) sessionOpenResponse(i, line int, e protocol.Envelope, p protocol.SessionOpenResponse) {
	s.checkPublishedSources(i, line, e)
	pending := s.pendingOpens[e.InReplyTo]
	if pending == nil {
		s.checkPublishedUnion(i, line, e, p.SessionID, p.Sources, true)
		return
	}
	if pending.expectation != nil {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload", pending.expectation.message, "a typed refusal naming "+pending.expectation.key, "an admitted open", string(e.InReplyTo))

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

	s.recordProvidedTools(track, pending.tools)
	s.checkPublishedUnion(i, line, e, p.SessionID, p.Sources, true)
}

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

func (s *state) settleToolSourceRefusal(i, line int, e protocol.Envelope) {
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	if pending := s.pendingLists[e.InReplyTo]; pending != nil {
		switch {
		case pending.expectation != nil:
			if !conformingRefusal(payload.Error, pending.expectation) {
				s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", pending.expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
			}
		case pending.honour:
			s.addExpected(CodeUnhonouredCapability, i, line, e, "/payload/error", "a catalog request within every disclosed constraint was refused by an endpoint advertising the catalog", "a served catalog", describeRefusal(payload.Error), string(e.InReplyTo))
		}
	}
	if pending := s.pendingOpens[e.InReplyTo]; pending != nil {
		attribution := s.attributeRefusal(e.InReplyTo, payload.Error)
		switch {
		case pending.expectation != nil:
			if attribution.owns(pending.expectation) {
				s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", pending.expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
			}
		case pending.limitRefusal != nil:

			if attribution.owns(pending.limitRefusal) {
				s.addExpected(pending.limitRefusal.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", pending.limitRefusal.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
			}
		case pending.honourDiagnostic != "":
			if !attribution.discharged() {
				s.addExpected(pending.honourDiagnostic, i, line, e, "/payload/error", "an open carrying no defect and violating no disclosed limit was refused by an endpoint advertising "+pending.honourKey, "an admitted open or a disclosed limit", describeRefusal(payload.Error), string(e.InReplyTo))
			}
		}
	}
}

func (s *state) attributionInForce(track *sessionTrack) (map[string]string, *sessionCatalog) {
	if track != nil && track.toolCatalog != nil && track.toolCatalog.revision == s.currentCapability {
		return track.toolCatalog.tools, track.toolCatalog
	}
	return s.descriptorAttribution, nil
}

func (s *state) checkAttachModes(i, line int, e protocol.Envelope, p protocol.CapabilitiesResponse) {
	support, ok := p.EffectiveSupport(protocol.FeatureToolSourcesAttach)
	if !ok || !affirmative(support.Level) || support.DisclosesMode(protocol.ModeSessionOpen) {
		return
	}
	s.addExpected(CodeUndisclosedAttachModes, i, line, e, "/payload/features/action.tool_sources.attach/modes", "action.tool_sources.attach is advertised without disclosing the session_open mode an open elects", protocol.ModeSessionOpen, describeModes(support.Modes))
}

func (s *state) checkCallAttributed(i, line int, e protocol.Envelope, p protocol.ActionCallPayload, track *sessionTrack) {
	if !affirmative(s.features[protocol.FeatureToolsList]) {
		return
	}

	mapping, _ := s.attributionInForce(track)
	listed, ok := mapping[p.Name]
	if !ok || listed == "" {
		return
	}
	s.addExpected(CodeUnattributedCall, i, line, e, "/payload/source", "a call names no source although the endpoint publishes a catalog that attributes the tool", listed, "none", p.Name)
}

func (s *state) checkCallSource(i, line int, e protocol.Envelope) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	track := s.sessions[p.SessionID]
	if p.Source == "" {
		s.checkCallAttributed(i, line, e, p, track)
		return
	}

	mapping, served := s.attributionInForce(track)
	if listed, ok := mapping[p.Name]; ok {
		if listed != p.Source {
			which := "the descriptor"
			if served != nil {
				which = "the session catalog"
			}
			s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/source", "a call attributes a tool to a source other than the one "+which+" records", listed, p.Source, p.Name)
		}
		return
	}
	if served != nil && served.sources[p.Source] {
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
