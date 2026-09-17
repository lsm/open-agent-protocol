package validation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/schema"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type Validator struct {
	schema  *jsonschema.Schema
	mode    Mode
	packs   *PackSet
	members map[string]map[string]*jsonschema.Schema
}

// Options configures a Validator. The zero value is strict validation with no
// extension packs, which is what every existing caller gets.
type Options struct {
	Mode  Mode
	Packs []*Pack
}

type rawEnvelope struct {
	raw  json.RawMessage
	line int
}

// New returns a strict validator: the bundle exactly as published, which is
// what fixtures are held to.
func New() (*Validator, error) {
	return NewWith(Options{Mode: ModeStrict})
}

// NewWith returns a validator in the requested mode. Tolerant mode compiles the
// bundle under the extension rules and lets the stateful validator classify an
// unknown envelope type by its wire scope rather than ignoring it.
func NewWith(opts Options) (*Validator, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ModeStrict
	}
	bundle, err := compileBundle(CompileOptions{Mode: mode, Packs: opts.Packs})
	if err != nil {
		return nil, err
	}
	return &Validator{schema: bundle.root, mode: mode, packs: NewPackSet(opts.Packs), members: bundle.members}, nil
}

// Mode reports the mode the validator was built in.
func (v *Validator) Mode() Mode { return v.mode }

// Packs reports the extension packs the validator was built with.
func (v *Validator) Packs() []*Pack { return v.packs.Packs() }

func MustNew() *Validator {
	v, err := New()
	if err != nil {
		panic(err)
	}
	return v
}

func (v *Validator) Validate(r io.Reader, fixture string) Result {
	raw, parseDiag := parseTrace(r, fixture)
	if parseDiag != nil {
		return Result{Diagnostics: []Diagnostic{*parseDiag}}
	}
	var envelopes []protocol.Envelope
	lines := make([]int, 0, len(raw))
	var diagnostics []Diagnostic
	for i, item := range raw {
		var value any
		dec := json.NewDecoder(bytes.NewReader(item.raw))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			diagnostics = append(diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Index: i, Line: item.line, Message: err.Error()})
			continue
		}
		// encoding/json silently keeps the last value for a repeated key, and
		// protocol.ParseEnvelope would make the same choice. A frame that other
		// implementations could read differently must not be certified, so the
		// raw bytes are examined recursively before schema validation.
		if key, duplicate := duplicateKey(item.raw); duplicate {
			diagnostics = append(diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeDuplicateKey, Index: i, Line: item.line, Message: fmt.Sprintf("duplicate object key %q", key)})
			continue
		}
		if err := v.validateSchema(value); err != nil {
			prefix := ""
			var member *memberSchemaError
			if errors.As(err, &member) {
				prefix = member.pointer
			}
			// The envelope's own declared type selects which oneOf branch
			// its diagnostics should come from. It is read from the raw
			// value because the envelope has not been parsed yet — that is
			// what failed.
			diagnostics = append(diagnostics, schemaDiagnosticsFor(err, fixture, i, item.line, prefix, declaredType(item.raw))...)
			continue
		}
		env, err := protocol.ParseEnvelope(item.raw)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Index: i, Line: item.line, Message: err.Error()})
			continue
		}
		if dst := payloadTarget(env.Type); dst != nil {
			if err := env.DecodePayload(dst); err != nil {
				diagnostics = append(diagnostics, baseDiagnostic(fixture, PhaseSemantic, CodePayloadDecode, i, item.line, env, "/payload", err.Error()))
				continue
			}
		}
		envelopes = append(envelopes, env)
		lines = append(lines, item.line)
	}
	if len(diagnostics) == 0 {
		s := newState(fixture)
		s.tolerant = v.mode == ModeTolerant
		s.packs = v.packs
		for i := range envelopes {
			s.apply(i, lines[i], envelopes[i])
		}
		s.close(len(envelopes))
		diagnostics = append(diagnostics, s.diagnostics...)
	}
	sortDiagnostics(diagnostics)
	return Result{Diagnostics: diagnostics}
}

func (v *Validator) ValidateBytes(data []byte, fixture string) Result {
	return v.Validate(bytes.NewReader(data), fixture)
}

// memberSchemaError marks a failure raised by the pack pass rather than the
// core pass, so the diagnostic can point at the member inside the payload
// instead of at the root of the member's own subschema.
type memberSchemaError struct {
	pointer string
	err     error
}

func (e *memberSchemaError) Error() string { return e.err.Error() }
func (e *memberSchemaError) Unwrap() error { return e.err }

// memberInstance is one declared pack member present on an envelope.
type memberInstance struct {
	pointer string
	schema  *jsonschema.Schema
	value   any
}

// validateSchema judges one envelope in two passes when packs are loaded, which
// is what keeps a pack from amending the protocol.
//
// The core pass judges the envelope's core projection — the envelope with every
// loaded pack's declared members removed — against the untouched core bundle in
// the mode in force, so a strict core payload still rejects an undeclared
// member exactly as it does today, and no core rule on a core member is
// relaxed or tightened by a pack. The pack pass then validates each declared
// member that is present against the subschema its pack declared for it.
// Patching the core branch instead would make an envelope the core bundle
// rejects valid the moment a pack is loaded.
func (v *Validator) validateSchema(value any) error {
	projection, members := v.project(value)
	if err := v.schema.Validate(projection); err != nil {
		return err
	}
	for _, member := range members {
		if err := member.schema.Validate(member.value); err != nil {
			return &memberSchemaError{pointer: member.pointer, err: err}
		}
	}
	return nil
}

// project splits one envelope into its core projection and the declared pack
// members it carries. An envelope carrying none is returned untouched, so the
// unpacked path is the exact path it was before.
func (v *Validator) project(value any) (any, []memberInstance) {
	if v.packs == nil {
		return value, nil
	}
	envelope, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	envelopeType, _ := envelope["type"].(string)
	declared := v.packs.Members(envelopeType)
	if len(declared) == 0 {
		return value, nil
	}
	payload, ok := envelope["payload"].(map[string]any)
	if !ok {
		return value, nil
	}
	var members []memberInstance
	projected := make(map[string]any, len(payload))
	for name, member := range payload {
		if _, isPacked := declared[name]; !isPacked {
			projected[name] = member
			continue
		}
		schema := v.members[envelopeType][name]
		if schema == nil {
			// A member with no compiled subschema cannot be judged; leaving it
			// in the projection has the core pass refuse it rather than
			// silently accepting an unchecked packed control.
			projected[name] = member
			continue
		}
		members = append(members, memberInstance{pointer: "/payload/" + jsonPointerToken(name), schema: schema, value: member})
	}
	if len(members) == 0 {
		return value, nil
	}
	out := make(map[string]any, len(envelope))
	for k, item := range envelope {
		out[k] = item
	}
	out["payload"] = projected
	sort.Slice(members, func(i, j int) bool { return members[i].pointer < members[j].pointer })
	return out, members
}

// duplicateKey returns the first repeated object key in one raw frame, if any.
// encoding/json resolves duplicates silently (the last value wins) instead of
// failing, so the bytes must be walked directly to reject an ambiguous frame.
// Structural anomalies are ignored here: the frame has already decoded, and this
// check exists only to flag a key a different implementation could read
// differently.
func duplicateKey(data []byte) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() (string, bool)
	walk = func() (string, bool) {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return "", false
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return "", false
				}
				key, ok := keyToken.(string)
				if !ok {
					return "", false
				}
				if _, repeated := seen[key]; repeated {
					return key, true
				}
				seen[key] = struct{}{}
				if nested, found := walk(); found {
					return nested, true
				}
			}
			if _, err := decoder.Token(); err != nil {
				return "", false
			}
		case '[':
			for decoder.More() {
				if nested, found := walk(); found {
					return nested, true
				}
			}
			if _, err := decoder.Token(); err != nil {
				return "", false
			}
		default:
			return "", false
		}
		return "", false
	}
	return walk()
}

func parseTrace(r io.Reader, fixture string) ([]rawEnvelope, *Diagnostic) {
	data, err := io.ReadAll(r)
	if err != nil {
		d := Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Message: err.Error()}
		return nil, &d
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	if data[0] == '[' {
		var items []json.RawMessage
		dec := json.NewDecoder(bytes.NewReader(data))
		if err := dec.Decode(&items); err != nil {
			d := malformed(fixture, err)
			return nil, &d
		}
		if err := requireEOF(dec); err != nil {
			d := malformed(fixture, err)
			return nil, &d
		}
		out := make([]rawEnvelope, len(items))
		for i := range items {
			out[i].raw = items[i]
		}
		return out, nil
	}
	var one json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&one); err == nil && requireEOF(dec) == nil {
		return []rawEnvelope{{raw: one, line: 1}}, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var out []rawEnvelope
	for line := 1; scanner.Scan(); line++ {
		b := bytes.TrimSpace(scanner.Bytes())
		if len(b) == 0 {
			continue
		}
		var value any
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			x := malformed(fixture, fmt.Errorf("line %d: %w", line, err))
			x.Line = line
			x.Index = len(out)
			return nil, &x
		}
		if err := requireEOF(d); err != nil {
			x := malformed(fixture, fmt.Errorf("line %d: %w", line, err))
			x.Line = line
			x.Index = len(out)
			return nil, &x
		}
		out = append(out, rawEnvelope{raw: append(json.RawMessage(nil), b...), line: line})
	}
	if err := scanner.Err(); err != nil {
		d := malformed(fixture, err)
		return nil, &d
	}
	return out, nil
}

func requireEOF(d *json.Decoder) error {
	var extra any
	err := d.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}
func malformed(fixture string, err error) Diagnostic {
	return Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Message: err.Error()}
}

// declaredType reads the `type` member of a raw envelope, or "" when the value
// is not an object or carries no string type.
func declaredType(raw []byte) string {
	var shape struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &shape) != nil {
		return ""
	}
	return shape.Type
}

func schemaDiagnostics(err error, fixture string, index, line int) []Diagnostic {
	return schemaDiagnosticsAt(err, fixture, index, line, "")
}

// envelopeBranches maps an envelope type to the `$defs` name of the branch
// that declares it. It is read from the schema rather than transcribed,
// because a hand-written table that drifted would misattribute exactly the
// diagnostics this exists to attribute correctly.
var envelopeBranches = func() map[string]string {
	var bundle struct {
		Defs map[string]struct {
			Properties struct {
				Type struct {
					Const string `json:"const"`
				} `json:"type"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	branches := map[string]string{}
	data, err := schema.V01.ReadFile("v0.1/envelope.schema.json")
	if err != nil || json.Unmarshal(data, &bundle) != nil {
		return branches
	}
	for name, def := range bundle.Defs {
		if def.Properties.Type.Const != "" {
			branches[def.Properties.Type.Const] = name
		}
	}
	return branches
}()

// branchNames is the same set keyed by `$defs` name, for recognising a schema
// location on the way down.
var branchNames = func() map[string]bool {
	names := make(map[string]bool, len(envelopeBranches))
	for _, name := range envelopeBranches {
		names[name] = true
	}
	return names
}()

// preferDeclaredBranch keeps only the leaves belonging to the branch the
// envelope's own `type` selects.
//
// The bundle is one big `oneOf`, so a frame that fails its own branch also
// fails all forty-two others, and the leaves from those are noise of the worst
// kind: they are true statements about schemas the author never claimed. An
// error.response missing `in_reply_to` was reported as "missing properties
// 'capability_revision', 'sequence'" — both real requirements of branches it
// was never trying to be, and neither the field actually missing. An
// implementer reading that goes looking for the wrong defect.
//
// When the type matches no branch there is nothing to prefer and every leaf is
// kept, which is right: an unrecognised type is itself the fault.
func preferDeclaredBranch(leaves []*jsonschema.ValidationError, branches []string, declared string) []*jsonschema.ValidationError {
	want, ok := envelopeBranches[declared]
	if !ok {
		return leaves
	}
	kept := make([]*jsonschema.ValidationError, 0, len(leaves))
	for i, leaf := range leaves {
		if branches[i] == want || branches[i] == "" {
			kept = append(kept, leaf)
		}
	}
	if len(kept) == 0 {
		return leaves
	}
	return kept
}

// branchOf reports the typed `$defs` branch a schema location names, or "" for
// a location that is not one — a shared base, the bundle root, a subschema
// inside a branch.
func branchOf(schemaURL string) string {
	const marker = "#/$defs/"
	at := strings.Index(schemaURL, marker)
	if at < 0 {
		return ""
	}
	rest := schemaURL[at+len(marker):]
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		rest = rest[:slash]
	}
	if _, ok := branchNames[rest]; ok {
		return rest
	}
	return ""
}

// schemaDiagnosticsAt reports a schema failure, rooting its pointer at prefix.
// The pack pass validates a member against its own subschema, whose instance
// locations are relative to that member; the prefix puts them back where the
// reader will look for them.
func schemaDiagnosticsAt(err error, fixture string, index, line int, prefix string) []Diagnostic {
	return schemaDiagnosticsFor(err, fixture, index, line, prefix, "")
}

func schemaDiagnosticsFor(err error, fixture string, index, line int, prefix, declared string) []Diagnostic {
	var validationErr *jsonschema.ValidationError
	if !errors.As(err, &validationErr) {
		return []Diagnostic{{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: index, Line: line, Pointer: prefix, Message: err.Error()}}
	}
	// The branch a leaf belongs to is carried down from its ancestors, not
	// readable off the leaf: a branch reaches its shared bases through $ref,
	// and a $ref failure anchors at the target, which every branch that
	// references it shares. So the nearest ancestor naming a typed branch is
	// remembered on the way down.
	var leaves []*jsonschema.ValidationError
	var branches []string
	var walk func(*jsonschema.ValidationError, string)
	walk = func(e *jsonschema.ValidationError, branch string) {
		if named := branchOf(e.SchemaURL); named != "" {
			branch = named
		}
		if len(e.Causes) == 0 {
			leaves = append(leaves, e)
			branches = append(branches, branch)
			return
		}
		for _, cause := range e.Causes {
			walk(cause, branch)
		}
	}
	walk(validationErr, "")
	leaves = preferDeclaredBranch(leaves, branches, declared)
	result := make([]Diagnostic, 0, len(leaves))
	seen := map[string]bool{}
	for _, leaf := range leaves {
		pointer := prefix + jsonPointer(leaf.InstanceLocation)
		message := leaf.ErrorKind.LocalizedString(message.NewPrinter(language.English))
		key := pointer + "\x00" + message
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, Diagnostic{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: index, Line: line, Pointer: pointer, Message: message})
	}
	if len(result) == 0 {
		result = append(result, Diagnostic{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: index, Line: line, Pointer: prefix, Message: err.Error()})
	}
	// A oneOf dispatch failure can expose every rejected event branch. Report one
	// stable structural diagnostic per envelope rather than leaking an engine-
	// dependent cascade of secondary failures.
	sortDiagnostics(result)
	return result[:1]
}
func jsonPointer(parts []string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteByte('/')
		b.WriteString(jsonPointerToken(p))
	}
	return b.String()
}

func jsonPointerToken(part string) string {
	return strings.ReplaceAll(strings.ReplaceAll(part, "~", "~0"), "/", "~1")
}
func baseDiagnostic(f string, phase Phase, code string, i, line int, e protocol.Envelope, ptr, msg string) Diagnostic {
	return Diagnostic{Fixture: f, Phase: phase, Code: code, Index: i, Line: line, EnvelopeID: string(e.ID), Type: string(e.Type), Pointer: ptr, Message: msg}
}

func payloadTarget(t protocol.EnvelopeType) any {
	switch t {
	case protocol.TypeProtocolInitializeRequest:
		return &protocol.InitializeRequest{}
	case protocol.TypeProtocolInitializeResponse:
		return &protocol.InitializeResponse{}
	case protocol.TypeCapabilitiesRequest:
		return &protocol.CapabilitiesRequest{}
	case protocol.TypeCapabilitiesResponse:
		return &protocol.CapabilitiesResponse{}
	case protocol.TypeCapabilitiesUpdated:
		return &protocol.CapabilitiesUpdated{}
	case protocol.TypeModelsRequest:
		return &protocol.ModelsRequest{}
	case protocol.TypeModelsResponse:
		return &protocol.ModelsResponse{}
	case protocol.TypeSessionOpenRequest:
		return &protocol.SessionOpenRequest{}
	case protocol.TypeSessionOpenResponse:
		return &protocol.SessionOpenResponse{}
	case protocol.TypeSessionStateRequest:
		return &protocol.SessionStateRequest{}
	case protocol.TypeSessionStateResponse, protocol.TypeSessionStateUpdated:
		return &protocol.SessionState{}
	case protocol.TypeSessionMessageSubmitRequest:
		return &protocol.MessageSubmitRequest{}
	case protocol.TypeSessionMessageSubmitResponse:
		return &protocol.MessageSubmitResponse{}
	case protocol.TypeRunCancelRequest:
		return &protocol.RunCancelRequest{}
	case protocol.TypeRunCancelResponse:
		return &protocol.RunCancelResponse{}
	case protocol.TypeRunStarted:
		return &protocol.RunStartedPayload{}
	case protocol.TypeRunStatusUpdated:
		return &protocol.RunStatusUpdatedPayload{}
	case protocol.TypeContentDelta:
		return &protocol.ContentDeltaPayload{}
	case protocol.TypeRunCompleted:
		return &protocol.RunCompletedPayload{}
	case protocol.TypeRunFailed:
		return &protocol.RunFailedPayload{}
	case protocol.TypeRunCancelled:
		return &protocol.RunCancelledPayload{}
	case protocol.TypeActionToolsListRequest:
		return &protocol.ToolsListRequest{}
	case protocol.TypeActionToolsListResponse:
		return &protocol.ToolsListResponse{}
	case protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled:
		return &protocol.ActionCallPayload{}
	case protocol.TypeActionCallResolveRequest:
		return &protocol.ActionCallResolveRequest{}
	case protocol.TypeActionCallResolveResponse:
		return &protocol.ActionCallResolveResponse{}
	case protocol.TypeActionPermissionRequested:
		return &protocol.PermissionRequestedPayload{}
	case protocol.TypeActionPermissionResolveRequest:
		return &protocol.PermissionResolveRequest{}
	case protocol.TypeActionPermissionResolveResponse:
		return &protocol.PermissionResolveResponse{}
	case protocol.TypeActionPermissionResolved:
		return &protocol.PermissionResolvedPayload{}
	case protocol.TypeUserInputRequested:
		return &protocol.UserInputRequestedPayload{}
	case protocol.TypeUserInputResolveRequest:
		return &protocol.UserInputResolveRequest{}
	case protocol.TypeUserInputResolveResponse:
		return &protocol.UserInputResolveResponse{}
	case protocol.TypeUserInputResolved:
		return &protocol.UserInputResolvedPayload{}
	case protocol.TypeUserInputCancelRequest:
		return &protocol.UserInputCancelRequest{}
	case protocol.TypeUserInputCancelResponse:
		return &protocol.UserInputCancelResponse{}
	case protocol.TypeErrorResponse:
		return &protocol.ErrorResponse{}
	default:
		return nil
	}
}

func uintString(v uint64) string { return strconv.FormatUint(v, 10) }
