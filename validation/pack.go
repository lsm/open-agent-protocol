package validation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	bundled "github.com/lsm/open-agent-protocol/schema"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// Extension packs are the protocol's own seam: a way for a third party to ship
// capability keys together with the schemas that say what they mean, so its
// envelopes are validated rather than merely tolerated.
//
// Two rules make packs composable rather than merely possible. The unprefixed
// namespace is the spec's in its entirety, so a pack may declare names only
// beneath its own reverse-DNS `id`; and the loaded set is prefix-free, so no
// pack id equals or dot-prefixes another. Prefix matching is then a function
// rather than a search: at most one loaded id can prefix any name, ownership is
// decided without a precedence rule, and a collision is a load refusal naming
// both ids rather than a runtime tie-break.
//
// Both are checked here, at load, and never on the wire. A refusal to load is
// not a diagnostic: the validator never ran, so it has said nothing about any
// trace. The load-error vocabulary (`pack_*`, `validation/manifest.go`) is kept
// apart from the diagnostic vocabulary for that reason.

// packBaseURI roots every pack resource. A pack's documents are registered
// beneath packBaseURI + "<id>/<version>/", which is what makes a cross-pack
// `$ref` distinguishable from an external one.
const packBaseURI = "https://open-agent-protocol.local/ext/"

// Declared roles. A packed type's role is stated, never inferred: a request and
// an event both omit `in_reply_to`, and the format has no naming convention to
// lean on, so the two would be indistinguishable — and they need opposite
// treatment under an unadvertised key.
const (
	PackRoleRequest  = "request"
	PackRoleResponse = "response"
	PackRoleEvent    = "event"
)

// PackDescriptor is a pack.json. It declares what the pack defines rather than
// leaving it to be inferred from the schemas, so containment can be checked
// before anything is compiled.
type PackDescriptor struct {
	ID             string              `json:"id"`
	Version        string              `json:"version"`
	Schemas        []string            `json:"schemas"`
	DependsOn      []PackDependency    `json:"depends_on,omitempty"`
	CapabilityKeys []string            `json:"capability_keys,omitempty"`
	EnvelopeTypes  []PackEnvelopeType  `json:"envelope_types,omitempty"`
	ErrorCodes     []string            `json:"error_codes,omitempty"`
	Gates          []PackGate          `json:"gates,omitempty"`
	PayloadMembers []PackPayloadMember `json:"payload_members,omitempty"`
	Fixtures       string              `json:"fixtures,omitempty"`
}

// PackDependency names a pack whose resources this pack's schemas may `$ref`.
// Only an exact id and version satisfies it: version ranges would make the
// loader choose between schemas on a vendor's behalf, and a mismatch caught at
// load is the fail-closed outcome.
type PackDependency struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// PackEnvelopeType declares one envelope type the pack defines. Schema points
// at the branch contributed to the envelope union; a response names the request
// it answers in RepliesTo and takes that request's gate; a request may enumerate
// in Refusals the error codes it may legitimately be answered with for domain
// reasons the validator cannot evaluate.
type PackEnvelopeType struct {
	Type      string   `json:"type"`
	Role      string   `json:"role"`
	Schema    string   `json:"schema"`
	RepliesTo string   `json:"replies_to,omitempty"`
	Refusals  []string `json:"refusals,omitempty"`
}

// PackGate binds a declared envelope type, or a member the pack adds to a core
// payload, to the capability key that must be advertised for it. Every declared
// request and event needs an entry: the gate is stated, never omitted, so a
// missing one is a load refusal rather than a silent hole. Ungated says so
// explicitly.
type PackGate struct {
	Type        string `json:"type,omitempty"`
	PayloadType string `json:"payload_type,omitempty"`
	Member      string `json:"member,omitempty"`
	Capability  string `json:"capability,omitempty"`
	Ungated     bool   `json:"ungated,omitempty"`
}

// PackPayloadMember is a member the pack adds to a core payload. New envelope
// types arrive as whole branches, but a member on an existing payload has no
// branch of its own: the strict core payload rejects it and the tolerant
// compile ignores it, so without a declared subschema a packed control would be
// gated but unchecked.
type PackPayloadMember struct {
	PayloadType string          `json:"payload_type"`
	Member      string          `json:"member"`
	Schema      json.RawMessage `json:"schema"`
}

// PackRefusal is one reason a pack was refused, carrying a code from the
// load-error vocabulary. Shape errors the plan does not name — unreadable
// JSON, a missing id — are ordinary errors instead, since no fixture asserts
// them.
type PackRefusal struct {
	Code    string
	Pack    string
	Message string
}

// PackLoadError is the refusal to load one or more packs.
type PackLoadError struct{ Refusals []PackRefusal }

func (e *PackLoadError) Error() string {
	parts := make([]string, 0, len(e.Refusals))
	for _, r := range e.Refusals {
		parts = append(parts, fmt.Sprintf("%s: %s: %s", r.Pack, r.Code, r.Message))
	}
	return "extension pack refused: " + strings.Join(parts, "; ")
}

// Codes returns the distinct load-error codes, sorted. A load-invalid fixture
// asserts exactly this set.
func (e *PackLoadError) Codes() []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(e.Refusals))
	for _, r := range e.Refusals {
		if r.Code == "" || seen[r.Code] {
			continue
		}
		seen[r.Code] = true
		out = append(out, r.Code)
	}
	sort.Strings(out)
	return out
}

// PackType is a loaded pack's envelope type, with the gate the stateful
// validator resolves through instead of a hard-coded name.
type PackType struct {
	Pack       *Pack
	Type       string
	Role       string
	RepliesTo  string
	Capability string // "" when the pack declared the type ungated
	Refusals   map[string]bool
	Response   string // for a request: the declared response type, if any
}

// PackMember is a loaded pack's member on a core payload.
type PackMember struct {
	Pack        *Pack
	PayloadType string
	Member      string
	Capability  string
	URI         string
}

// Pack is a loaded extension pack: its descriptor, its schema documents under
// their own base URI, and the lookup tables the validator resolves gates with.
type Pack struct {
	Descriptor PackDescriptor
	Root       string
	Base       string

	documents map[string]any // absolute URI -> decoded schema document
	order     []string       // document URIs in descriptor order
	branches  map[string]string
	types     map[string]*PackType
	members   map[string]map[string]*PackMember
	fixtures  string // absolute path to the pack's own fixture manifest, or ""
}

// ID reports the pack's namespace.
func (p *Pack) ID() string { return p.Descriptor.ID }

// Version reports the pack's declared version.
func (p *Pack) Version() string { return p.Descriptor.Version }

// Unit is the conformance term a pack's own fixtures claim. It is derived from
// the pack rather than added to the hard-coded unit list, so a stale or
// misspelled claim still fails closed.
func (p *Pack) Unit() string { return "ext:" + p.Descriptor.ID + "/" + p.Descriptor.Version }

// PackSet is the loaded set as the validator consults it: at most one pack owns
// any name, so every lookup is a map hit.
type PackSet struct {
	packs   []*Pack
	types   map[string]*PackType
	members map[string]map[string]*PackMember
}

// NewPackSet indexes loaded packs for validation. The set is prefix-free and
// contained, both checked at load, so no name can be claimed twice.
func NewPackSet(packs []*Pack) *PackSet {
	if len(packs) == 0 {
		return nil
	}
	set := &PackSet{packs: packs, types: map[string]*PackType{}, members: map[string]map[string]*PackMember{}}
	for _, p := range packs {
		for name, t := range p.types {
			set.types[name] = t
		}
		for payloadType, members := range p.members {
			if set.members[payloadType] == nil {
				set.members[payloadType] = map[string]*PackMember{}
			}
			for name, m := range members {
				set.members[payloadType][name] = m
			}
		}
	}
	return set
}

// Packs returns the loaded packs in load order.
func (s *PackSet) Packs() []*Pack {
	if s == nil {
		return nil
	}
	return s.packs
}

// Type returns the declaration for a packed envelope type, or nil.
func (s *PackSet) Type(name string) *PackType {
	if s == nil {
		return nil
	}
	return s.types[name]
}

// Members returns the members packs add to one core payload type, or nil.
func (s *PackSet) Members(payloadType string) map[string]*PackMember {
	if s == nil {
		return nil
	}
	return s.members[payloadType]
}

// LoadPacks reads, checks, and compiles a set of extension packs. Every refusal
// the plan names is collected in the phase it belongs to and the phases stop at
// the first that refuses, so a pack is never judged on rules a prior failure
// made meaningless.
func LoadPacks(dirs []string) ([]*Pack, error) {
	packs := make([]*Pack, 0, len(dirs))
	for _, dir := range dirs {
		p, err := readPack(dir)
		if err != nil {
			return nil, err
		}
		packs = append(packs, p)
	}
	phases := []func([]*Pack) []PackRefusal{
		checkPrefixFree,
		checkDeclarations,
		readDocuments,
		checkDependencies,
		checkReferences,
		checkBranches,
		trialCompile,
		checkPackCorpora,
	}
	for _, phase := range phases {
		if refusals := phase(packs); len(refusals) > 0 {
			return nil, &PackLoadError{Refusals: refusals}
		}
	}
	return packs, nil
}

var (
	descriptorOnce   sync.Once
	descriptorSchema *jsonschema.Schema
	descriptorErr    error
)

// packDescriptorSchema compiles the descriptor schema out of the same embedded
// bundle the wire schemas come from. The descriptor is checked for shape here
// and for meaning by the loader: a schema can say that `role` is one of three
// words, never that a response's `replies_to` names a request of this pack.
func packDescriptorSchema() (*jsonschema.Schema, error) {
	descriptorOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compiler.UseLoader(&refusingLoader{})
		entries, err := fs.ReadDir(bundled.V01, "v0.1")
		if err != nil {
			descriptorErr = fmt.Errorf("read embedded schemas: %w", err)
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
				continue
			}
			data, err := fs.ReadFile(bundled.V01, "v0.1/"+entry.Name())
			if err != nil {
				descriptorErr = fmt.Errorf("read embedded schema %s: %w", entry.Name(), err)
				return
			}
			var document any
			if err := json.Unmarshal(data, &document); err != nil {
				descriptorErr = fmt.Errorf("decode embedded schema %s: %w", entry.Name(), err)
				return
			}
			if err := compiler.AddResource(schemaBase+entry.Name(), document); err != nil {
				descriptorErr = fmt.Errorf("register embedded schema %s: %w", entry.Name(), err)
				return
			}
		}
		descriptorSchema, descriptorErr = compiler.Compile(schemaBase + "pack.schema.json")
	})
	return descriptorSchema, descriptorErr
}

func readPack(dir string) (*Pack, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, "pack.json"))
	if err != nil {
		return nil, fmt.Errorf("read pack descriptor: %w", err)
	}
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Join(dir, "pack.json"), err)
	}
	schema, err := packDescriptorSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(document); err != nil {
		return nil, fmt.Errorf("pack descriptor %s: %w", filepath.Join(dir, "pack.json"), err)
	}
	var descriptor PackDescriptor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&descriptor); err != nil {
		return nil, fmt.Errorf("decode %s: %w", filepath.Join(dir, "pack.json"), err)
	}
	if !reverseDNS(descriptor.ID) {
		return nil, fmt.Errorf("pack id %q is not a reverse-DNS name", descriptor.ID)
	}
	return &Pack{
		Descriptor: descriptor,
		Root:       root,
		Base:       packBaseURI + descriptor.ID + "/" + descriptor.Version + "/",
		documents:  map[string]any{},
		branches:   map[string]string{},
		types:      map[string]*PackType{},
		members:    map[string]map[string]*PackMember{},
	}, nil
}

// checkPrefixFree rejects a loaded set in which one pack id equals or
// dot-prefixes another. The check is across the set, not per pack, because
// neither pack is at fault alone: `com.example` and `com.example.storage` each
// satisfy the own-prefix rule while both legally claiming
// `com.example.storage.read`.
func checkPrefixFree(packs []*Pack) []PackRefusal {
	var refusals []PackRefusal
	for i := 0; i < len(packs); i++ {
		for j := i + 1; j < len(packs); j++ {
			a, b := packs[i].ID(), packs[j].ID()
			if a == b || strings.HasPrefix(a, b+".") || strings.HasPrefix(b, a+".") {
				refusals = append(refusals, PackRefusal{
					Code:    LoadPackIDCollision,
					Pack:    a,
					Message: fmt.Sprintf("pack ids %q and %q are not prefix-free; neither pack owns a name both could claim", a, b),
				})
			}
		}
	}
	return refusals
}

// checkDeclarations judges one pack's descriptor: containment of every declared
// name, a stated role for every type, a stated gate for every request and
// event, a response deriving its gate through replies_to rather than a gate of
// its own, declared refusal codes, and payload members that name a real core
// target and do not restate a member the core payload already defines.
func checkDeclarations(packs []*Pack) []PackRefusal {
	var refusals []PackRefusal
	for _, p := range packs {
		refusals = append(refusals, p.checkDeclarations()...)
	}
	return refusals
}

func (p *Pack) checkDeclarations() []PackRefusal {
	var refusals []PackRefusal
	refuse := func(code, format string, args ...any) {
		refusals = append(refusals, PackRefusal{Code: code, Pack: p.ID(), Message: fmt.Sprintf(format, args...)})
	}
	contained := func(kind, name string) bool {
		if code, ok := containmentRefusal(p.ID(), name); !ok {
			refuse(code, "%s %q is outside the pack namespace %q", kind, name, p.ID())
			return false
		}
		return true
	}

	declaredKeys := map[string]bool{}
	for _, key := range p.Descriptor.CapabilityKeys {
		if contained("capability key", key) {
			declaredKeys[key] = true
		}
	}
	declaredCodes := map[string]bool{}
	for _, code := range p.Descriptor.ErrorCodes {
		if contained("error code", code) {
			declaredCodes[code] = true
		}
	}
	declaredTypes := map[string]*PackEnvelopeType{}
	for i := range p.Descriptor.EnvelopeTypes {
		declared := &p.Descriptor.EnvelopeTypes[i]
		if !contained("envelope type", declared.Type) {
			continue
		}
		declaredTypes[declared.Type] = declared
	}
	if len(refusals) > 0 {
		// Containment decides which names the pack owns; every later rule is
		// stated over those names, so judging them now would report failures
		// that are consequences of the first.
		return refusals
	}

	for _, declared := range p.Descriptor.EnvelopeTypes {
		switch declared.Role {
		case PackRoleRequest, PackRoleResponse, PackRoleEvent:
		default:
			refuse(LoadPackRoleUndeclared, "envelope type %q declares no role; a request, a response, and an event need opposite treatment under an unadvertised key", declared.Type)
			continue
		}
		if declared.Schema == "" {
			refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("envelope type %q contributes no branch", declared.Type)})
		}
		if declared.Role == PackRoleResponse {
			target, ok := declaredTypes[declared.RepliesTo]
			if !ok || target.Role != PackRoleRequest {
				refuse(LoadPackReplyTargetUnknown, "response %q answers %q, which is not a declared request of this pack", declared.Type, declared.RepliesTo)
			}
		}
		for _, refusal := range declared.Refusals {
			if !declaredCodes[refusal] {
				refuse(LoadPackRefusalUndeclared, "type %q may be refused with %q, which the pack never declared as an error code", declared.Type, refusal)
			}
		}
	}

	gated := map[string]bool{}
	for _, gate := range p.Descriptor.Gates {
		name := gate.Type
		if gate.Type == "" {
			if gate.PayloadType == "" || gate.Member == "" {
				refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: "a gate names neither an envelope type nor a payload member"})
				continue
			}
			name = gate.PayloadType + "#" + gate.Member
		}
		if gate.Ungated == (gate.Capability != "") {
			refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("gate for %q must name exactly one of a capability or ungated", name)})
			continue
		}
		if gate.Capability != "" && !declaredKeys[gate.Capability] {
			if code, ok := containmentRefusal(p.ID(), gate.Capability); !ok {
				refuse(code, "gate for %q names capability %q outside the pack namespace", name, gate.Capability)
			} else {
				refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("gate for %q names capability %q, which the pack never declared", name, gate.Capability)})
			}
			continue
		}
		if gate.Type != "" {
			declared, ok := declaredTypes[gate.Type]
			if !ok {
				refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("gate names undeclared envelope type %q", gate.Type)})
				continue
			}
			if declared.Role == PackRoleResponse {
				refuse(LoadPackResponseGated, "response %q carries a gate of its own; a response derives its gate from the request it answers", gate.Type)
				continue
			}
		}
		if gated[name] {
			refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("%q is gated twice", name)})
			continue
		}
		gated[name] = true
	}
	for _, declared := range p.Descriptor.EnvelopeTypes {
		if declared.Role == PackRoleResponse {
			continue
		}
		if !gated[declared.Type] {
			refuse(LoadPackUngatedType, "%s %q names no gate and is not declared ungated", declared.Role, declared.Type)
		}
	}

	core := coreVocabulary()
	for _, member := range p.Descriptor.PayloadMembers {
		payload, ok := core[member.PayloadType]
		if !ok {
			refuse(LoadPackMemberTargetUnknown, "payload member %q targets %q, which is not a core envelope type; a target with no known role has no gate point", member.Member, member.PayloadType)
			continue
		}
		// Restatement is judged before containment, and that order is what
		// makes the rule reachable: the member a pack would restate is a core
		// one, which is unprefixed by definition, so containment alone would
		// report every restatement as a namespace error and never as the
		// override it is.
		if payload.members[member.Member] {
			refuse(LoadPackRestatesCoreMember, "payload member %q restates a member %q already defines; a pack that could narrow a core member would change core validity", member.Member, member.PayloadType)
			continue
		}
		if !contained("payload member", member.Member) {
			continue
		}
		if len(member.Schema) == 0 {
			refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("payload member %q declares no schema", member.Member)})
			continue
		}
		if !gated[member.PayloadType+"#"+member.Member] {
			refuse(LoadPackUngatedType, "payload member %q on %q names no gate and is not declared ungated", member.Member, member.PayloadType)
		}
	}
	if len(refusals) > 0 {
		return refusals
	}
	p.index()
	return nil
}

// index builds the lookup tables the validator resolves gates through, once the
// descriptor has been judged.
func (p *Pack) index() {
	capabilities := map[string]string{}
	for _, gate := range p.Descriptor.Gates {
		if gate.Type != "" {
			capabilities[gate.Type] = gate.Capability
			continue
		}
		capabilities[gate.PayloadType+"#"+gate.Member] = gate.Capability
	}
	responses := map[string]string{}
	for _, declared := range p.Descriptor.EnvelopeTypes {
		if declared.Role == PackRoleResponse && responses[declared.RepliesTo] == "" {
			responses[declared.RepliesTo] = declared.Type
		}
	}
	for _, declared := range p.Descriptor.EnvelopeTypes {
		refusals := map[string]bool{}
		for _, code := range declared.Refusals {
			refusals[code] = true
		}
		p.types[declared.Type] = &PackType{
			Pack:       p,
			Type:       declared.Type,
			Role:       declared.Role,
			RepliesTo:  declared.RepliesTo,
			Capability: capabilities[declared.Type],
			Refusals:   refusals,
			Response:   responses[declared.Type],
		}
	}
	for i, member := range p.Descriptor.PayloadMembers {
		if p.members[member.PayloadType] == nil {
			p.members[member.PayloadType] = map[string]*PackMember{}
		}
		p.members[member.PayloadType][member.Member] = &PackMember{
			Pack:        p,
			PayloadType: member.PayloadType,
			Member:      member.Member,
			Capability:  capabilities[member.PayloadType+"#"+member.Member],
			URI:         p.memberURI(i),
		}
	}
}

func (p *Pack) memberURI(index int) string {
	return fmt.Sprintf("%smember-%d.json", p.Base, index)
}

// containmentRefusal judges one declared name against the pack's own id. The
// refusal distinguishes the two ways a name can fall outside it: a name in the
// spec's namespace — unprefixed, which is every name with no reverse-DNS prefix
// and every name opening on a core vocabulary root — and a name under another
// vendor's prefix.
func containmentRefusal(id, name string) (string, bool) {
	if name == "" {
		return LoadPackUnprefixedName, false
	}
	if strings.HasPrefix(name, id+".") {
		return "", true
	}
	if specNamespace(name) {
		return LoadPackUnprefixedName, false
	}
	return LoadPackForeignPrefix, false
}

// specNamespace reports whether a name belongs to the spec's namespace. The
// rule is stated the way round that leaves the existing vocabulary alone: the
// unprefixed namespace is the spec's in its entirety, whatever the name's
// shape, and an extension name is one carrying a reverse-DNS prefix. A name
// with fewer than three labels has no room for one; a longer name whose first
// two labels open a core envelope type — `session.message.submit.request` — is
// core vocabulary however many labels follow. No list of permitted roots is
// maintained: the core roots are read off the bundle the validator already
// compiles.
func specNamespace(name string) bool {
	labels := strings.Split(name, ".")
	if len(labels) < 3 {
		return true
	}
	return coreRoots()[labels[0]+"."+labels[1]]
}

// reverseDNS reports whether a pack id is a reverse-DNS name: at least two
// labels, each a non-empty DNS label.
func reverseDNS(id string) bool {
	labels := strings.Split(id, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			case c == '-' && i > 0 && i < len(label)-1:
			default:
				return false
			}
		}
	}
	return true
}

// corePayload is one core envelope type as the loader needs it: the role that
// decides where a member added to it settles its gate, and the members the core
// payload already defines, which a pack may not restate.
type corePayload struct {
	role    string
	members map[string]bool
}

var (
	coreOnce      sync.Once
	coreTypes     map[string]corePayload
	coreRootNames map[string]bool
)

// coreVocabulary reads the core envelope types and their payload members off
// the embedded bundle. The schemas are the authority: a type added to the
// bundle is a core type here without a second list to update.
func coreVocabulary() map[string]corePayload {
	coreOnce.Do(loadCoreVocabulary)
	return coreTypes
}

func coreRoots() map[string]bool {
	coreOnce.Do(loadCoreVocabulary)
	return coreRootNames
}

func loadCoreVocabulary() {
	coreTypes = map[string]corePayload{}
	coreRootNames = map[string]bool{}
	documents := map[string]map[string]any{}
	entries, err := fs.ReadDir(bundled.V01, "v0.1")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := fs.ReadFile(bundled.V01, "v0.1/"+entry.Name())
		if err != nil {
			continue
		}
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			continue
		}
		documents[entry.Name()] = document
	}
	envelope := documents["envelope.schema.json"]
	defs, _ := envelope["$defs"].(map[string]any)
	for _, raw := range defs {
		branch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		properties, _ := branch["properties"].(map[string]any)
		typeSchema, _ := properties["type"].(map[string]any)
		name, ok := typeSchema["const"].(string)
		if !ok {
			continue
		}
		members := map[string]bool{}
		collectPayloadMembers(documents, "envelope.schema.json", properties["payload"], members, 0)
		coreTypes[name] = corePayload{role: coreRole(name), members: members}
		if labels := strings.Split(name, "."); len(labels) >= 2 {
			coreRootNames[labels[0]+"."+labels[1]] = true
		}
	}
}

// coreRole is a core type's role, which the protocol's own naming already
// states: a `.request` is answered, a `.response` answers, everything else is
// an event the endpoint emits.
func coreRole(name string) string {
	switch {
	case strings.HasSuffix(name, ".request"):
		return PackRoleRequest
	case strings.HasSuffix(name, ".response"):
		return PackRoleResponse
	default:
		return PackRoleEvent
	}
}

// collectPayloadMembers gathers the property names one payload schema defines,
// following same- and cross-file references and allOf composition.
func collectPayloadMembers(documents map[string]map[string]any, file string, node any, out map[string]bool, depth int) {
	if depth > 8 {
		return
	}
	schema, ok := node.(map[string]any)
	if !ok {
		return
	}
	if ref, ok := schema["$ref"].(string); ok {
		targetFile, pointer := file, ref
		if idx := strings.Index(ref, "#"); idx >= 0 {
			if idx > 0 {
				targetFile = ref[:idx]
			}
			pointer = ref[idx:]
		}
		document, ok := documents[targetFile]
		if !ok {
			return
		}
		collectPayloadMembers(documents, targetFile, resolvePointer(document, pointer), out, depth+1)
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name := range properties {
			out[name] = true
		}
	}
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, member := range allOf {
			collectPayloadMembers(documents, file, member, out, depth+1)
		}
	}
}

func resolvePointer(document map[string]any, pointer string) any {
	if !strings.HasPrefix(pointer, "#/") {
		return nil
	}
	var node any = document
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		object, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node, ok = object[part]
		if !ok {
			return nil
		}
	}
	return node
}

// readDocuments reads each pack's schema files. The refusing loader governs
// references, but the files the descriptor names are read before any reference
// is resolved, so the same containment is owed to them first: every entry is a
// relative path, cleaned, joined to the pack root, and — after resolving
// symlinks — still beneath it, or the pack is refused before a file is opened.
func readDocuments(packs []*Pack) []PackRefusal {
	var refusals []PackRefusal
	for _, p := range packs {
		root, err := filepath.EvalSymlinks(p.Root)
		if err != nil {
			refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("resolve pack root: %v", err)})
			continue
		}
		for _, rel := range p.Descriptor.Schemas {
			resolved, err := containedPath(root, rel)
			if err != nil {
				refusals = append(refusals, PackRefusal{Code: LoadPackSchemaPathEscape, Pack: p.ID(), Message: fmt.Sprintf("schema %q: %v", rel, err)})
				continue
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("read schema %q: %v", rel, err)})
				continue
			}
			var document any
			if err := json.Unmarshal(data, &document); err != nil {
				refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("decode schema %q: %v", rel, err)})
				continue
			}
			uri := p.Base + filepath.ToSlash(filepath.Clean(rel))
			p.documents[uri] = document
			p.order = append(p.order, uri)
		}
		if p.Descriptor.Fixtures != "" {
			resolved, err := containedPath(root, p.Descriptor.Fixtures)
			if err != nil {
				refusals = append(refusals, PackRefusal{Code: LoadPackSchemaPathEscape, Pack: p.ID(), Message: fmt.Sprintf("fixture manifest %q: %v", p.Descriptor.Fixtures, err)})
				continue
			}
			p.fixtures = resolved
		}
	}
	return refusals
}

// containedPath cleans one declared relative path, joins it beneath root, and
// resolves symlinks, refusing anything that leaves the pack. An absolute path,
// a `../` path, and a symlink pointing out of the pack are each refused before
// the file is opened: a descriptor could otherwise name a private key on the
// loading machine and have the loader read it as a schema.
func containedPath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute path is outside the pack root")
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes the pack root")
	}
	joined := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("resolve: %w", err)
	}
	if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path resolves outside the pack root")
	}
	return resolved, nil
}

// checkDependencies requires every declared dependency to be satisfied by a
// loaded pack of that exact id and version.
func checkDependencies(packs []*Pack) []PackRefusal {
	loaded := map[string]bool{}
	for _, p := range packs {
		loaded[p.ID()+"/"+p.Version()] = true
	}
	var refusals []PackRefusal
	for _, p := range packs {
		for _, dependency := range p.Descriptor.DependsOn {
			if !loaded[dependency.ID+"/"+dependency.Version] {
				refusals = append(refusals, PackRefusal{
					Code:    LoadPackDependencyMissing,
					Pack:    p.ID(),
					Message: fmt.Sprintf("depends on %s/%s, which is not loaded at that exact version", dependency.ID, dependency.Version),
				})
			}
		}
	}
	return refusals
}

// checkReferences bounds a pack's reachable schema surface to exactly what it
// said it was: its own documents, the core bundle, and the packs it declared as
// dependencies. A reference anywhere else — a file path, an unregistered URI,
// another loaded pack's base absent from depends_on — is a load refusal rather
// than a compile error surfaced later.
func checkReferences(packs []*Pack) []PackRefusal {
	var refusals []PackRefusal
	for _, p := range packs {
		allowed := map[string]bool{}
		for uri := range p.documents {
			allowed[uri] = true
		}
		for _, dependency := range p.Descriptor.DependsOn {
			for _, other := range packs {
				if other.ID() == dependency.ID && other.Version() == dependency.Version {
					for uri := range other.documents {
						allowed[uri] = true
					}
				}
			}
		}
		for _, uri := range p.order {
			refusals = append(refusals, checkDocumentReferences(p, uri, p.documents[uri], allowed)...)
		}
		for i, member := range p.Descriptor.PayloadMembers {
			var document any
			if err := json.Unmarshal(member.Schema, &document); err != nil {
				refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("decode payload member %q: %v", member.Member, err)})
				continue
			}
			refusals = append(refusals, checkDocumentReferences(p, p.memberURI(i), document, allowed)...)
		}
	}
	return refusals
}

func checkDocumentReferences(p *Pack, base string, document any, allowed map[string]bool) []PackRefusal {
	baseURL, err := url.Parse(base)
	if err != nil {
		return []PackRefusal{{Pack: p.ID(), Message: fmt.Sprintf("parse base %q: %v", base, err)}}
	}
	var refusals []PackRefusal
	for _, ref := range collectRefs(document, 0) {
		target, err := url.Parse(ref)
		if err != nil {
			refusals = append(refusals, PackRefusal{Code: LoadPackExternalRef, Pack: p.ID(), Message: fmt.Sprintf("reference %q is not a resolvable URI", ref)})
			continue
		}
		resolved := baseURL.ResolveReference(target)
		resolved.Fragment = ""
		resolved.RawFragment = ""
		absolute := resolved.String()
		if allowed[absolute] || isCoreResource(absolute) {
			continue
		}
		refusals = append(refusals, PackRefusal{
			Code:    LoadPackExternalRef,
			Pack:    p.ID(),
			Message: fmt.Sprintf("reference %q resolves to %q, outside the pack's own resources, the core bundle, and its declared dependencies", ref, absolute),
		})
	}
	return refusals
}

func isCoreResource(absolute string) bool {
	if !strings.HasPrefix(absolute, schemaBase) {
		return false
	}
	name := strings.TrimPrefix(absolute, schemaBase)
	return name != "" && !strings.Contains(name, "/")
}

func collectRefs(node any, depth int) []string {
	if depth > 64 {
		return nil
	}
	switch n := node.(type) {
	case map[string]any:
		var out []string
		for key, value := range n {
			if key == "$ref" || key == "$dynamicRef" {
				if ref, ok := value.(string); ok {
					out = append(out, ref)
					continue
				}
			}
			out = append(out, collectRefs(value, depth+1)...)
		}
		return out
	case []any:
		var out []string
		for _, item := range n {
			out = append(out, collectRefs(item, depth+1)...)
		}
		return out
	default:
		return nil
	}
}

// checkBranches requires every contributed branch to pin `type` to a const
// naming its own declared envelope type. The check is not bookkeeping: `oneOf`
// requires exactly one match, so a branch broad enough to also match
// `run.started` would make a core envelope invalid because a pack was loaded.
// With the discriminator pinned and verified, branch selection is a dispatch on
// `type` and the invariant holds by construction.
func checkBranches(packs []*Pack) []PackRefusal {
	var refusals []PackRefusal
	for _, p := range packs {
		declared := map[string]bool{}
		for _, entry := range p.Descriptor.EnvelopeTypes {
			declared[entry.Type] = true
		}
		for _, entry := range p.Descriptor.EnvelopeTypes {
			uri, branch, err := p.resolveBranch(entry.Schema)
			if err != nil {
				refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("branch for %q: %v", entry.Type, err)})
				continue
			}
			properties, _ := branch["properties"].(map[string]any)
			typeSchema, _ := properties["type"].(map[string]any)
			pinned, ok := typeSchema["const"].(string)
			if !ok {
				refusals = append(refusals, PackRefusal{
					Code:    LoadPackBranchUnpinned,
					Pack:    p.ID(),
					Message: fmt.Sprintf("branch for %q does not pin type to a const; an unpinned branch can match a core envelope and invalidate it by double match", entry.Type),
				})
				continue
			}
			if !declared[pinned] || pinned != entry.Type {
				refusals = append(refusals, PackRefusal{
					Code:    LoadPackBranchUndeclaredType,
					Pack:    p.ID(),
					Message: fmt.Sprintf("branch for %q pins type %q, which is not that declared envelope type", entry.Type, pinned),
				})
				continue
			}
			p.branches[entry.Type] = uri
		}
	}
	return refusals
}

// resolveBranch reads a `<file>#<pointer>` branch reference out of the pack's
// own documents and returns its absolute URI and the decoded subschema.
func (p *Pack) resolveBranch(ref string) (string, map[string]any, error) {
	file, pointer := ref, ""
	if idx := strings.Index(ref, "#"); idx >= 0 {
		file, pointer = ref[:idx], ref[idx:]
	}
	if file == "" {
		return "", nil, fmt.Errorf("branch reference %q names no schema file", ref)
	}
	uri := p.Base + path.Clean(filepath.ToSlash(file))
	document, ok := p.documents[uri]
	if !ok {
		return "", nil, fmt.Errorf("schema %q is not one the descriptor contributes", file)
	}
	node := document
	if pointer != "" && pointer != "#" {
		object, ok := document.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("schema %q is not an object", file)
		}
		node = resolvePointer(object, pointer)
	}
	branch, ok := node.(map[string]any)
	if !ok {
		return "", nil, fmt.Errorf("branch %q does not resolve to a schema", ref)
	}
	return uri + pointer, branch, nil
}

// trialCompile compiles the core bundle with the packs composed in, through the
// same refusing loader the references were checked against, so a reference the
// structural check could not judge still fails at load rather than at first use.
func trialCompile(packs []*Pack) []PackRefusal {
	if _, err := compileBundle(CompileOptions{Mode: ModeStrict, Packs: packs}); err != nil {
		var refusal *PackLoadError
		if errors.As(err, &refusal) {
			return refusal.Refusals
		}
		return []PackRefusal{{Pack: packIDs(packs), Message: err.Error()}}
	}
	return nil
}

func packIDs(packs []*Pack) string {
	ids := make([]string, 0, len(packs))
	for _, p := range packs {
		ids = append(ids, p.ID())
	}
	return strings.Join(ids, ", ")
}

// checkPackCorpora holds a pack to the corpus-completeness rule over its own
// capability keys: a vendor cannot claim conformance for a key that nothing
// could show it dishonouring. A pack's fixture manifest may claim only its own
// `ext:` term, never a core unit, so a pack can never widen a core claim.
func checkPackCorpora(packs []*Pack) []PackRefusal {
	var refusals []PackRefusal
	for _, p := range packs {
		if len(p.Descriptor.CapabilityKeys) == 0 && p.fixtures == "" {
			continue
		}
		if p.fixtures == "" {
			refusals = append(refusals, PackRefusal{
				Pack:    p.ID(),
				Message: fmt.Sprintf("declares %d capability key(s) and ships no fixture manifest; a key nothing can falsify is not a claim", len(p.Descriptor.CapabilityKeys)),
			})
			continue
		}
		options := ManifestOptions{Packs: packs, Owner: p}
		manifest, err := LoadManifestWith(p.fixtures, options)
		if err != nil {
			var refusal *PackLoadError
			if errors.As(err, &refusal) {
				refusals = append(refusals, refusal.Refusals...)
				continue
			}
			refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: fmt.Sprintf("fixture manifest: %v", err)})
			continue
		}
		if err := checkCorpusCompleteness(manifest, map[string][]string{p.Unit(): p.Descriptor.CapabilityKeys}); err != nil {
			refusals = append(refusals, PackRefusal{Pack: p.ID(), Message: err.Error()})
		}
	}
	return refusals
}
