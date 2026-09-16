package validation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The refusal an unadvertised capability is owed. These are wire vocabulary the
// endpoint emits, not validator diagnostics: the two do not correspond, and a
// pack can add to the first and never to the second.
const (
	errorUnsupportedFeature = "unsupported_feature"
	reasonUnadvertised      = "unadvertised"
)

// The stateful rules for packed vocabulary. They add no new machinery: a packed
// type resolves its feature key through the loaded pack's `gates` instead of a
// hard-coded string, and the existing gate then applies unchanged — unadvertised
// means the typed refusal is owed, an admission is `unavailable_capability`, and
// the refusal is itself validated. An extension capability is fail-closed in the
// same machinery as a core one, which is the claim the unit makes, and what
// makes it implementable generically rather than per pack.

// isRequestType and isResponseType prefer a loaded pack's declared role over the
// core naming convention. A packed request and a packed event both omit
// `in_reply_to` and the format has no naming convention to lean on, so the role
// is the only thing that can tell them apart — and they need opposite treatment
// under an unadvertised key: a request is permissible and is retained until its
// correlated response says whether the endpoint refused it, while an event is
// already the endpoint acting on a capability it does not have.
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

// expectedResponse is the response type a request is answered by. A packed
// request names it through the `replies_to` of the response that declares it,
// which is what correlation is for a vocabulary with no naming convention.
func (s *state) expectedResponse(t protocol.EnvelopeType) protocol.EnvelopeType {
	if packed := s.packs.Type(string(t)); packed != nil {
		return protocol.EnvelopeType(packed.Response)
	}
	return expectedResponse(t)
}

// packGate is one capability key an envelope's admission is judged against,
// with the refusals its pack promised it could answer under.
type packGate struct {
	key      string
	refusals map[string]bool
	typed    bool // the gate of a packed request, which the honour rule bounds
}

// packEnvelope applies the pack rules to one envelope. Where a gate settles
// follows from the role of what carries it, and that is defined for every
// permitted target rather than for submit requests alone: an event or a
// response is the endpoint acting on the capability and is judged on arrival,
// while a request is retained and settles on its correlated response.
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
		s.feature(i, line, e, packed.Capability)
	}
	if role != PackRoleRequest {
		// A member added to a core response or event is the endpoint acting on
		// the capability, so it is judged where it appears. A member on a
		// request is not: it settles with that request's response, below.
		for _, gate := range s.memberGates(e.Type, e.Payload) {
			s.feature(i, line, e, gate.key)
		}
	}
	if role != PackRoleResponse && e.Type != protocol.TypeErrorResponse {
		return
	}
	request := s.requests[e.InReplyTo]
	if request == nil {
		return
	}
	for _, gate := range s.requestGates(request) {
		s.settleGate(i, line, e, gate)
	}
}

// requestGates lists the keys a retained request's admission is judged against:
// the gate of a packed request type, and the gate of every packed member the
// request carries.
func (s *state) requestGates(request *requestState) []packGate {
	var gates []packGate
	if packed := s.packs.Type(string(request.typ)); packed != nil && packed.Capability != "" {
		gates = append(gates, packGate{key: packed.Capability, refusals: packed.Refusals, typed: true})
	}
	return append(gates, s.memberGates(request.typ, request.envelope.Payload)...)
}

// memberGates lists the gates of the declared pack members one payload carries.
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

// settleGate judges one correlated response against the gate its request
// carried.
//
// While the key is unadvertised the ladder is the one every capability-rung
// refusal uses: a success response is the endpoint acting on a capability it
// does not have, and a refusal must be the typed `unsupported_feature` naming
// the key. Once the key is advertised the gate has nothing left to say, and the
// honour rule takes over for a packed request: a refusal under a code the pack
// declared is a domain refusal the validator cannot evaluate and is conforming,
// while a refusal under any other code convicts an endpoint that advertised a
// capability and refused to honour it.
func (s *state) settleGate(i, line int, e protocol.Envelope, gate packGate) {
	advertised := s.advertised(gate.key)
	if e.Type != protocol.TypeErrorResponse {
		if !advertised {
			s.feature(i, line, e, gate.key)
		}
		return
	}
	var payload protocol.ErrorResponse
	if err := e.DecodePayload(&payload); err != nil {
		return
	}
	if !advertised {
		feature, _ := payload.Error.Details["feature"].(string)
		reason, _ := payload.Error.Details["reason"].(string)
		if payload.Error.Code == errorUnsupportedFeature && feature == gate.key && reason == reasonUnadvertised {
			return
		}
		s.addExpected(CodeUnavailableCapability, i, line, e, "/payload/error/code",
			"an unadvertised capability must be refused with the typed unsupported-feature error naming it",
			fmt.Sprintf("%s with details.feature %q and details.reason %q", errorUnsupportedFeature, gate.key, reasonUnadvertised),
			payload.Error.Code, string(e.InReplyTo))
		return
	}
	if !gate.typed {
		return
	}
	if gate.refusals[payload.Error.Code] {
		return
	}
	s.addExpected(CodeUnhonouredCapability, i, line, e, "/payload/error/code",
		"an endpoint advertising this capability refused a well-formed request under a code its pack never declared",
		declaredRefusals(gate.refusals), payload.Error.Code, string(e.InReplyTo))
}

// advertised reports whether a capability key is affirmatively advertised by
// the current descriptor. A packed key is fully qualified, so it is looked up
// as declared; the core prefixes the gate tries are core spellings.
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
