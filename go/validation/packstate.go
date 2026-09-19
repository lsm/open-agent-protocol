package validation

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func (s *state) isRequestType(t protocol.EnvelopeType) bool {
	if packed := s.packs.Type(string(t)); packed != nil {
		return packed.Role == PackRoleRequest
	}
	return isRequest(t)
}

func (s *state) isResponseType(t protocol.EnvelopeType) bool {
	if packed := s.packs.Type(string(t)); packed != nil {
		return packed.Role == PackRoleResponse
	}
	return isResponse(t)
}

func (s *state) envelopeScoped(t protocol.EnvelopeType) bool {
	return !isKnownType(t) && (s.tolerant || s.packs.Type(string(t)) != nil)
}

func (s *state) expectedResponse(t protocol.EnvelopeType) protocol.EnvelopeType {
	if packed := s.packs.Type(string(t)); packed != nil {
		return protocol.EnvelopeType(packed.Response)
	}
	return expectedResponse(t)
}

type packGate struct {
	key        string
	refusals   map[string]bool
	typed      bool
	advertised bool
}

func (s *state) packEnvelope(i, line int, e protocol.Envelope) {
	if s.packs == nil {
		return
	}
	packed := s.packs.Type(string(e.Type))
	role := ""
	switch {
	case packed != nil:
		role = packed.Role
	case isRequest(e.Type):
		role = PackRoleRequest
	case isResponse(e.Type):
		role = PackRoleResponse
	default:
		role = PackRoleEvent
	}
	if packed != nil && role == PackRoleEvent && packed.Capability != "" {
		s.packFeature(i, line, e, packed.Capability)
	}
	if role != PackRoleRequest {

		for _, gate := range s.memberGates(e.Type, e.Payload) {
			s.packFeature(i, line, e, gate.key)
		}
	}
	if role == PackRoleRequest {

		if request := s.requests[e.ID]; request != nil {
			request.gates = s.requestGates(request)
		}
		return
	}
	if role != PackRoleResponse && e.Type != protocol.TypeErrorResponse {
		return
	}
	request := s.requests[e.InReplyTo]
	if request == nil {
		return
	}
	gates := request.gates
	if e.Type == protocol.TypeErrorResponse {
		s.settleRefusal(i, line, e, gates)
		return
	}
	for _, gate := range gates {
		if !gate.advertised {

			s.addExpected(CodeUnavailableCapability, i, line, e, "/type",
				"response admits a request made under a capability that was not advertised",
				fmt.Sprintf("%s with details.feature %q and details.reason %q", errorUnsupportedFeature, gate.key, reasonUnadvertised),
				string(e.Type), string(e.InReplyTo))
		}
	}
}

func (s *state) requestGates(request *requestState) []packGate {
	var gates []packGate
	if packed := s.packs.Type(string(request.typ)); packed != nil && packed.Capability != "" {
		gates = append(gates, packGate{key: packed.Capability, refusals: packed.Refusals, typed: true})
	}
	gates = append(gates, s.memberGates(request.typ, request.envelope.Payload)...)
	for i := range gates {
		gates[i].advertised = s.advertised(gates[i].key)
	}
	return gates
}

func (s *state) memberGates(t protocol.EnvelopeType, payload json.RawMessage) []packGate {
	declared := s.packs.Members(string(t))
	if len(declared) == 0 || len(payload) == 0 {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(payload, &members); err != nil {
		return nil
	}
	var gates []packGate
	for name := range members {
		if member, ok := declared[name]; ok && member.Capability != "" {
			gates = append(gates, packGate{key: member.Capability})
		}
	}
	return gates
}

func (s *state) settleRefusal(i, line int, e protocol.Envelope, gates []packGate) {
	var payload protocol.ErrorResponse
	if err := e.DecodePayload(&payload); err != nil {
		return
	}
	var unadvertised []string
	var typed *packGate
	for idx := range gates {
		if !gates[idx].advertised {
			unadvertised = append(unadvertised, gates[idx].key)
		} else if gates[idx].typed {
			typed = &gates[idx]
		}
	}
	if len(unadvertised) > 0 {
		sort.Strings(unadvertised)
		feature, _ := payload.Error.Details["feature"].(string)
		reason, _ := payload.Error.Details["reason"].(string)
		if payload.Error.Code == errorUnsupportedFeature && reason == reasonUnadvertised && slices.Contains(unadvertised, feature) {
			return
		}
		named := fmt.Sprintf("details.feature %q", unadvertised[0])
		if len(unadvertised) > 1 {
			named = fmt.Sprintf("details.feature one of %q", unadvertised)
		}
		s.addExpected(CodeUnavailableCapability, i, line, e, "/payload/error/code",
			"an unadvertised capability must be refused with the typed unsupported-feature error naming it",
			fmt.Sprintf("%s with %s and details.reason %q", errorUnsupportedFeature, named, reasonUnadvertised),
			payload.Error.Code, string(e.InReplyTo))
		return
	}
	if typed == nil || typed.refusals[payload.Error.Code] {
		return
	}
	s.addExpected(CodeUnhonouredCapability, i, line, e, "/payload/error/code",
		"an endpoint advertising this capability refused a well-formed request under a code its pack never declared",
		declaredRefusals(typed.refusals), payload.Error.Code, string(e.InReplyTo))
}

func (s *state) packFeature(i, line int, e protocol.Envelope, key string) {
	s.featureKeys(i, line, e, []string{key})
}

func (s *state) advertised(key string) bool {
	if s.currentCapability == "" || s.capabilitiesStale {
		return false
	}
	switch s.features[key] {
	case protocol.SupportNative, protocol.SupportEmulated, protocol.SupportDegraded:
		return true
	}
	return false
}

func declaredRefusals(refusals map[string]bool) string {
	if len(refusals) == 0 {
		return "no declared refusal"
	}
	names := make([]string, 0, len(refusals))
	for name := range refusals {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
