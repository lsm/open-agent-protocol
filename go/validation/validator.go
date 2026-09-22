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

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/schema"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type Validator struct {
	schema  *jsonschema.Schema
	mode    Mode
	packs   *PackSet
	members map[string]map[string]*jsonschema.Schema
}

type Options struct {
	Mode  Mode
	Packs []*Pack
}

type rawEnvelope struct {
	raw  json.RawMessage
	line int
}

func New() (*Validator, error) {
	return NewWith(Options{Mode: ModeStrict})
}

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

func (v *Validator) Mode() Mode { return v.mode }

func (v *Validator) Packs() []*Pack { return v.packs.Packs() }

func MustNew() *Validator {
	v, err := New()
	if err != nil {
		panic(err)
	}
	return v
}

func (v *Validator) Validate(r io.Reader, fixture string) Result {
	input, readErr := io.ReadAll(r)
	if readErr != nil {
		return Result{Diagnostics: []Diagnostic{{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Message: "could not read trace"}}}
	}
	raw, parseDiag := parseTrace(bytes.NewReader(input), fixture)
	if parseDiag != nil {
		if containsAuthReply(input) {
			parseDiag.Message = "malformed auth reply trace (sensitive answer redacted)"
		}
		return Result{Diagnostics: []Diagnostic{*parseDiag}}
	}
	var envelopes []protocol.Envelope
	lines := make([]int, 0, len(raw))
	var diagnostics []Diagnostic
	for i, item := range raw {
		sensitive := containsAuthReply(item.raw)
		var value any
		dec := json.NewDecoder(bytes.NewReader(item.raw))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			message := err.Error()
			if sensitive {
				message = "malformed auth reply (sensitive answer redacted)"
			}
			diagnostics = append(diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Index: i, Line: item.line, Message: message})
			continue
		}

		if key, duplicate := duplicateKey(item.raw); duplicate {
			message := fmt.Sprintf("duplicate object key %q", key)
			if sensitive {
				message = "duplicate key in auth reply (sensitive answer redacted)"
			}
			diagnostics = append(diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeDuplicateKey, Index: i, Line: item.line, Message: message})
			continue
		}
		if err := v.validateSchema(value); err != nil {
			prefix := ""
			var member *memberSchemaError
			if errors.As(err, &member) {
				prefix = member.pointer
			}

			found := schemaDiagnosticsFor(err, fixture, i, item.line, prefix, declaredType(item.raw))
			if sensitive {
				redactAuthReplyDiagnostics(found)
			}
			diagnostics = append(diagnostics, found...)
			continue
		}
		env, err := protocol.ParseEnvelope(item.raw)
		if err != nil {
			message := err.Error()
			if sensitive {
				message = "malformed auth reply envelope (sensitive answer redacted)"
			}
			diagnostics = append(diagnostics, Diagnostic{Fixture: fixture, Phase: PhaseDecode, Code: CodeMalformedJSON, Index: i, Line: item.line, Message: message})
			continue
		}
		if dst := payloadTarget(env.Type); dst != nil {
			if err := env.DecodePayload(dst); err != nil {
				message := err.Error()
				if sensitive {
					message = "invalid auth reply payload (sensitive answer redacted)"
				}
				diagnostics = append(diagnostics, baseDiagnostic(fixture, PhaseSemantic, CodePayloadDecode, i, item.line, env, "/payload", message))
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

func containsAuthReply(raw []byte) bool {
	return bytes.Contains(raw, []byte("auth.login.reply.request")) ||
		declaredType(raw) == string(protocol.TypeAuthLoginReplyRequest)
}

func redactAuthReplyDiagnostics(diagnostics []Diagnostic) {
	for i := range diagnostics {
		diagnostics[i].Message = "invalid auth reply (sensitive answer redacted)"
		diagnostics[i].Expected = ""
		diagnostics[i].Actual = ""
	}
}

func (v *Validator) ValidateBytes(data []byte, fixture string) Result {
	return v.Validate(bytes.NewReader(data), fixture)
}

type memberSchemaError struct {
	pointer string
	err     error
}

func (e *memberSchemaError) Error() string { return e.err.Error() }
func (e *memberSchemaError) Unwrap() error { return e.err }

type memberInstance struct {
	pointer string
	schema  *jsonschema.Schema
	value   any
}

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

var branchNames = func() map[string]bool {
	names := make(map[string]bool, len(envelopeBranches))
	for _, name := range envelopeBranches {
		names[name] = true
	}
	return names
}()

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

func schemaDiagnosticsAt(err error, fixture string, index, line int, prefix string) []Diagnostic {
	return schemaDiagnosticsFor(err, fixture, index, line, prefix, "")
}

func schemaDiagnosticsFor(err error, fixture string, index, line int, prefix, declared string) []Diagnostic {
	var validationErr *jsonschema.ValidationError
	if !errors.As(err, &validationErr) {
		return []Diagnostic{{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: index, Line: line, Pointer: prefix, Message: err.Error()}}
	}

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
	case protocol.TypeAuthProvidersRequest:
		return &protocol.AuthProvidersRequest{}
	case protocol.TypeAuthProvidersResponse:
		return &protocol.AuthProvidersResponse{}
	case protocol.TypeAuthLoginStartRequest:
		return &protocol.AuthLoginStartRequest{}
	case protocol.TypeAuthLoginStartResponse:
		return &protocol.AuthLoginStartResponse{}
	case protocol.TypeAuthLoginEvent:
		return &protocol.AuthLoginEvent{}
	case protocol.TypeAuthLoginReplyRequest:
		return &protocol.AuthLoginReplyRequest{}
	case protocol.TypeAuthLoginReplyResponse:
		return &protocol.AuthLoginReplyResponse{}
	case protocol.TypeAuthLoginCancelRequest:
		return &protocol.AuthLoginCancelRequest{}
	case protocol.TypeAuthLoginCancelResponse:
		return &protocol.AuthLoginCancelResponse{}
	case protocol.TypeAuthLoginCompleted:
		return &protocol.AuthLoginCompleted{}
	case protocol.TypeSessionOpenRequest:
		return &protocol.SessionOpenRequest{}
	case protocol.TypeSessionOpenResponse:
		return &protocol.SessionOpenResponse{}
	case protocol.TypeSessionStateRequest:
		return &protocol.SessionStateRequest{}
	case protocol.TypeSessionStateResponse, protocol.TypeSessionStateUpdated:
		return &protocol.SessionState{}
	case protocol.TypeSessionModelSwitchRequest:
		return &protocol.SessionModelSwitchRequest{}
	case protocol.TypeSessionModelSwitchResponse:
		return &protocol.SessionModelSwitchResponse{}
	case protocol.TypeSessionProviderAttachRequest:
		return &protocol.SessionProviderAttachRequest{}
	case protocol.TypeSessionProviderAttachResponse:
		return &protocol.SessionProviderAttachResponse{}
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
