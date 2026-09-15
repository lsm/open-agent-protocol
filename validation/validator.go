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
	"strconv"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type Validator struct {
	schema *jsonschema.Schema
	mode   Mode
}

// Options configures a Validator. The zero value is strict validation, which is
// what every existing caller gets.
type Options struct {
	Mode Mode
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
	s, err := CompileSchemasWith(CompileOptions{Mode: mode})
	if err != nil {
		return nil, err
	}
	return &Validator{schema: s, mode: mode}, nil
}

// Mode reports the mode the validator was built in.
func (v *Validator) Mode() Mode { return v.mode }

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
		if err := v.schema.Validate(value); err != nil {
			diagnostics = append(diagnostics, schemaDiagnostics(err, fixture, i, item.line)...)
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

func schemaDiagnostics(err error, fixture string, index, line int) []Diagnostic {
	var validationErr *jsonschema.ValidationError
	if !errors.As(err, &validationErr) {
		return []Diagnostic{{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: index, Line: line, Message: err.Error()}}
	}
	var leaves []*jsonschema.ValidationError
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			leaves = append(leaves, e)
			return
		}
		for _, cause := range e.Causes {
			walk(cause)
		}
	}
	walk(validationErr)
	result := make([]Diagnostic, 0, len(leaves))
	seen := map[string]bool{}
	for _, leaf := range leaves {
		pointer := jsonPointer(leaf.InstanceLocation)
		message := leaf.ErrorKind.LocalizedString(message.NewPrinter(language.English))
		key := pointer + "\x00" + message
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, Diagnostic{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: index, Line: line, Pointer: pointer, Message: message})
	}
	if len(result) == 0 {
		result = append(result, Diagnostic{Fixture: fixture, Phase: PhaseSchema, Code: CodeSchemaInvalid, Index: index, Line: line, Message: err.Error()})
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
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(p, "~", "~0"), "/", "~1"))
	}
	return b.String()
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
