package validation

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	bundled "github.com/lsm/open-agent-protocol/schema"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const schemaBase = "https://open-agent-protocol.local/v0.1/"

// Mode selects how the bundled schemas are compiled.
//
// ModeStrict compiles the bundle exactly as published: every payload object is
// closed, every enum is exact, and the envelope oneOf admits only the types it
// names. It is what fixtures are held to, so a misspelled member or an unknown
// type fails rather than passes.
//
// ModeTolerant compiles the same bundle under the layered draft's extension
// rules (unknown fields are ignored, unknown enum values surface as strings,
// unknown envelope types satisfy the common fields only), so a validator built
// from this revision accepts the additive fields and types a later revision or
// an extension pack introduces. It is what a live client's dev-mode validation
// and `oap validate -mode tolerant` use.
type Mode string

const (
	ModeStrict   Mode = "strict"
	ModeTolerant Mode = "tolerant"
)

// ParseMode maps a command-line spelling onto a Mode.
func ParseMode(s string) (Mode, error) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeStrict, "":
		return ModeStrict, nil
	case ModeTolerant:
		return ModeTolerant, nil
	}
	return "", fmt.Errorf("unsupported validation mode %q (strict or tolerant)", s)
}

// CompileOptions configures CompileSchemasWith.
//
// Packs are the second seam: zero or more loaded extension packs are registered
// as additional resources under their own base URI and contribute branches to
// the envelope union, so their envelope types are validated against their own
// schemas instead of taking the tolerant unknown path. The core bundle is never
// modified to get there — a pack adds vocabulary, it does not amend the
// protocol.
type CompileOptions struct {
	Mode  Mode
	Packs []*Pack
}

// metaSchemas describe the bundle and its extensions rather than an envelope,
// so they stay exact in every mode: tolerating a descriptor's vocabulary would
// make a malformed pack declaration load.
var metaSchemas = map[string]bool{"manifest.schema.json": true, "pack.schema.json": true}

// CompileSchemas compiles the embedded v0.1 bundle strictly. It is unchanged
// from before the tolerance step: existing callers keep the exact bundle.
func CompileSchemas() (*jsonschema.Schema, error) {
	return CompileSchemasWith(CompileOptions{Mode: ModeStrict})
}

// CompileSchemasWith compiles the embedded v0.1 bundle in the requested mode,
// with any loaded packs composed in. The tolerant mode transforms each schema
// document in memory before it is registered; the files on disk and the strict
// compile are untouched.
func CompileSchemasWith(opts CompileOptions) (*jsonschema.Schema, error) {
	bundle, err := compileBundle(opts)
	if err != nil {
		return nil, err
	}
	return bundle.root, nil
}

// compiledBundle is the compiled envelope schema plus, keyed by core payload
// type and member name, the subschema each loaded pack declared for a member it
// adds. The member schemas are separate because composition never patches a
// core branch: validation is two passes over the same envelope instead.
type compiledBundle struct {
	root    *jsonschema.Schema
	members map[string]map[string]*jsonschema.Schema
}

// refusingLoader answers every reference the registered resources do not
// already satisfy with a refusal. A third-party pack is a document a user loads
// from someone else, so compiling it with the engine's default file loader
// would let a `$ref` reach paths outside the pack on the loading machine.
type refusingLoader struct{ attempted []string }

func (l *refusingLoader) Load(url string) (any, error) {
	l.attempted = append(l.attempted, url)
	return nil, fmt.Errorf("reference %q is outside the registered resources", url)
}

func compileBundle(opts CompileOptions) (*compiledBundle, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ModeStrict
	}
	if mode != ModeStrict && mode != ModeTolerant {
		return nil, fmt.Errorf("unsupported validation mode %q", mode)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	loader := &refusingLoader{}
	compiler.UseLoader(loader)
	entries, err := fs.ReadDir(bundled.V01, "v0.1")
	if err != nil {
		return nil, fmt.Errorf("read embedded schemas: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := fs.ReadFile(bundled.V01, "v0.1/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded schema %s: %w", entry.Name(), err)
		}
		var document any
		if err := json.Unmarshal(data, &document); err != nil {
			return nil, fmt.Errorf("decode embedded schema %s: %w", entry.Name(), err)
		}
		if mode == ModeTolerant && !metaSchemas[entry.Name()] {
			document = tolerate(document)
		}
		if entry.Name() == "envelope.schema.json" {
			document = composeEnvelope(document, opts.Packs)
		}
		if err := compiler.AddResource(schemaBase+entry.Name(), document); err != nil {
			return nil, fmt.Errorf("register embedded schema %s: %w", entry.Name(), err)
		}
	}
	memberURIs := map[string]map[string]string{}
	for _, pack := range opts.Packs {
		for uri, document := range pack.documents {
			if mode == ModeTolerant {
				document = tolerate(document)
			}
			if err := compiler.AddResource(uri, document); err != nil {
				return nil, fmt.Errorf("register pack schema %s: %w", uri, err)
			}
		}
		for payloadType, members := range pack.members {
			for name, member := range members {
				declared := pack.declaredMember(payloadType, name)
				var document any
				if err := json.Unmarshal(declared.Schema, &document); err != nil {
					return nil, fmt.Errorf("decode payload member %s: %w", name, err)
				}
				if mode == ModeTolerant {
					document = tolerate(document)
				}
				if err := compiler.AddResource(member.URI, document); err != nil {
					return nil, fmt.Errorf("register payload member %s: %w", name, err)
				}
				if memberURIs[payloadType] == nil {
					memberURIs[payloadType] = map[string]string{}
				}
				memberURIs[payloadType][name] = member.URI
			}
		}
	}
	var root *jsonschema.Schema
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			continue
		}
		compiled, err := compiler.Compile(schemaBase + entry.Name())
		if err != nil {
			return nil, packCompileError(opts.Packs, loader, err)
		}
		if entry.Name() == "envelope.schema.json" {
			root = compiled
		}
	}
	if root == nil {
		return nil, fmt.Errorf("compile schema bundle: envelope schema not found")
	}
	bundle := &compiledBundle{root: root, members: map[string]map[string]*jsonschema.Schema{}}
	for payloadType, members := range memberURIs {
		bundle.members[payloadType] = map[string]*jsonschema.Schema{}
		for name, uri := range members {
			compiled, err := compiler.Compile(uri)
			if err != nil {
				return nil, packCompileError(opts.Packs, loader, err)
			}
			bundle.members[payloadType][name] = compiled
		}
	}
	return bundle, nil
}

// packCompileError reports a compile failure that a pack caused. A reference
// the refusing loader was asked for is an external reference, which is a load
// refusal rather than a compile error surfaced later.
func packCompileError(packs []*Pack, loader *refusingLoader, err error) error {
	if len(packs) == 0 {
		return fmt.Errorf("compile schema bundle: %w", err)
	}
	if len(loader.attempted) > 0 {
		return &PackLoadError{Refusals: []PackRefusal{{
			Code:    LoadPackExternalRef,
			Pack:    packIDs(packs),
			Message: fmt.Sprintf("reference %q is outside the pack's own resources, the core bundle, and its declared dependencies", loader.attempted[0]),
		}}}
	}
	return fmt.Errorf("compile schema bundle with packs: %w", err)
}

// declaredMember returns the descriptor entry behind an indexed member.
func (p *Pack) declaredMember(payloadType, name string) PackPayloadMember {
	for _, member := range p.Descriptor.PayloadMembers {
		if member.PayloadType == payloadType && member.Member == name {
			return member
		}
	}
	return PackPayloadMember{}
}

// composeEnvelope adds each loaded pack's branches to the envelope union. In
// strict mode they join the `oneOf`, where the verified `type` const makes
// branch selection a dispatch: a core envelope can never reach a pack branch,
// so no core envelope becomes invalid because a pack was loaded. In tolerant
// mode the union is already an `anyOf` with a fallback for unknown types, and
// the packed types are removed from what that fallback accepts — otherwise a
// malformed packed envelope would be caught by its own branch and then excused
// by the fallback, and loading the pack would change nothing.
func composeEnvelope(document any, packs []*Pack) any {
	if len(packs) == 0 {
		return document
	}
	root, ok := document.(map[string]any)
	if !ok {
		return document
	}
	var branches []any
	var types []any
	for _, pack := range packs {
		for name, uri := range pack.branches {
			branches = append(branches, map[string]any{"$ref": uri})
			types = append(types, name)
		}
	}
	if len(branches) == 0 {
		return document
	}
	sort.Slice(branches, func(i, j int) bool {
		return branches[i].(map[string]any)["$ref"].(string) < branches[j].(map[string]any)["$ref"].(string)
	})
	out := make(map[string]any, len(root))
	for k, v := range root {
		out[k] = v
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		members, ok := out[key].([]any)
		if !ok {
			continue
		}
		composed := make([]any, 0, len(members)+len(branches))
		for _, member := range members {
			composed = append(composed, excludeFromFallback(member, types))
		}
		out[key] = append(composed, branches...)
		return out
	}
	return out
}

// excludeFromFallback narrows the tolerant union's fallback branch so it no
// longer accepts a type a loaded pack claims. Any other member is returned as
// is.
func excludeFromFallback(member any, types []any) any {
	branch, ok := member.(map[string]any)
	if !ok {
		return member
	}
	properties, ok := branch["properties"].(map[string]any)
	if !ok {
		return member
	}
	discriminator, ok := properties["type"].(map[string]any)
	if !ok {
		return member
	}
	not, ok := discriminator["not"].(map[string]any)
	if !ok {
		return member
	}
	known, ok := not["enum"].([]any)
	if !ok {
		return member
	}
	widened := make([]any, 0, len(known)+len(types))
	widened = append(widened, known...)
	widened = append(widened, types...)
	return map[string]any{
		"type":     branch["type"],
		"required": branch["required"],
		"properties": map[string]any{
			"type": map[string]any{"type": "string", "not": map[string]any{"enum": widened}},
		},
	}
}

// tolerate rewrites one decoded schema document into its tolerant form. The
// rules are the layered draft's extension rules made mechanical:
//
//   - `additionalProperties: false` is lifted from every object schema, so an
//     unknown member is ignored rather than rejected — on a payload, on a
//     nested object, and on a known branch of a discriminated union alike, so
//     a later revision's optional member on a known content kind passes.
//   - An extensible leaf `enum` of strings is widened to `type: string`, so an
//     unknown value surfaces as a string instead of failing the message.
//   - A `const` is never lifted. It is a fixed semantic value, not a vocabulary:
//     `run.started.status` is `const: "running"` and a `run.started` carrying
//     `status: "failed"` is malformed under any revision, and the same holds
//     for `protocol`, `version`, `profile`, and every `type` discriminator.
//   - Nothing inside an `if` guard is changed. Those enums and consts decide
//     which conditional branch applies; widening one would make both branches
//     match and require and forbid the same members at once.
//   - A `oneOf`/`anyOf` whose every member pins a `const` on one property is
//     a discriminated union; it gains a fallback branch that accepts an object
//     whose discriminator is a string outside the known set and that carries
//     only the members every branch requires. A known branch keeps its
//     discriminator const, required members, and member types exact, which
//     is what rejects a known kind with a malformed body; closedness adds
//     nothing to that and is lifted like everywhere else. An unknown kind
//     is tolerated by the fallback, which excludes every known discriminator.
//     The envelope root's oneOf over `type` is the largest instance; the
//     content-part union is the other.
func tolerate(document any) any {
	t := &tolerator{doc: document}
	return t.walk(document, walkContext{})
}

type walkContext struct {
	inIf bool
}

type tolerator struct {
	doc any
}

func (t *tolerator) walk(node any, ctx walkContext) any {
	switch n := node.(type) {
	case map[string]any:
		return t.walkObject(n, ctx)
	case []any:
		out := make([]any, len(n))
		for i, item := range n {
			out[i] = t.walk(item, walkContext{inIf: ctx.inIf})
		}
		return out
	default:
		return node
	}
}

func (t *tolerator) walkObject(m map[string]any, ctx walkContext) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	rewroteUnion := false
	if !ctx.inIf {
		// Discriminated unions first, so the fallback is built from the
		// members before they are walked.
		for _, key := range []string{"oneOf", "anyOf"} {
			members, ok := out[key].([]any)
			if !ok || len(members) == 0 {
				continue
			}
			if discriminator, known, common, ok := t.discriminated(members); ok {
				walked := make([]any, 0, len(members)+1)
				for _, member := range members {
					walked = append(walked, t.walk(member, walkContext{}))
				}
				walked = append(walked, fallbackBranch(discriminator, known, common))
				delete(out, key)
				out["anyOf"] = walked
				rewroteUnion = true
			}
		}
		if enum, ok := out["enum"].([]any); ok && allStrings(enum) {
			delete(out, "enum")
			if _, hasType := out["type"]; !hasType {
				out["type"] = "string"
			}
		}
		if ap, ok := out["additionalProperties"]; ok && ap == false {
			delete(out, "additionalProperties")
		}
	}
	for k, v := range out {
		switch k {
		case "enum", "const", "type", "required", "additionalProperties":
			continue
		case "anyOf":
			// A union rewritten above already holds walked members plus the
			// fallback; an ordinary anyOf is walked like any other list.
			if rewroteUnion {
				continue
			}
			out[k] = t.walk(v, walkContext{inIf: ctx.inIf})
		case "if":
			out[k] = t.walk(v, walkContext{inIf: true})
		default:
			out[k] = t.walk(v, walkContext{inIf: ctx.inIf})
		}
	}
	return out
}

// discriminated reports whether every member of a union pins the same property
// to a const, and if so the property, the known consts, and the required
// members common to every branch.
func (t *tolerator) discriminated(members []any) (string, []any, []string, bool) {
	var discriminator string
	known := make([]any, 0, len(members))
	var common map[string]int
	for _, member := range members {
		resolved := t.resolve(member)
		if resolved == nil {
			return "", nil, nil, false
		}
		props, _ := resolved["properties"].(map[string]any)
		found := ""
		var value any
		for name, schema := range props {
			s, ok := schema.(map[string]any)
			if !ok {
				continue
			}
			if c, has := s["const"]; has {
				if found != "" {
					// Two consts on one branch: not a single discriminator.
					return "", nil, nil, false
				}
				found, value = name, c
			}
		}
		if found == "" || (discriminator != "" && found != discriminator) {
			return "", nil, nil, false
		}
		discriminator = found
		known = append(known, value)
		req := map[string]int{}
		if list, ok := resolved["required"].([]any); ok {
			for _, r := range list {
				if s, ok := r.(string); ok {
					req[s] = 1
				}
			}
		}
		if common == nil {
			common = req
		} else {
			for name := range common {
				if req[name] == 0 {
					delete(common, name)
				}
			}
		}
	}
	names := make([]string, 0, len(common))
	for name := range common {
		names = append(names, name)
	}
	sort.Strings(names)
	return discriminator, known, names, true
}

// resolve follows a same-document `#/$defs/...` reference; any other member
// is returned as is, and a member that is not an object schema is nil.
func (t *tolerator) resolve(member any) map[string]any {
	m, ok := member.(map[string]any)
	if !ok {
		return nil
	}
	ref, ok := m["$ref"].(string)
	if !ok {
		return m
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var node any = t.doc
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		obj, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node, ok = obj[part]
		if !ok {
			return nil
		}
	}
	resolved, _ := node.(map[string]any)
	return resolved
}

func fallbackBranch(discriminator string, known []any, common []string) map[string]any {
	required := make([]any, 0, len(common)+1)
	seen := false
	for _, name := range common {
		if name == discriminator {
			seen = true
		}
		required = append(required, name)
	}
	if !seen {
		required = append(required, discriminator)
	}
	return map[string]any{
		"type":     "object",
		"required": required,
		"properties": map[string]any{
			discriminator: map[string]any{
				"type": "string",
				"not":  map[string]any{"enum": known},
			},
		},
	}
}

func allStrings(values []any) bool {
	if len(values) == 0 {
		return false
	}
	for _, v := range values {
		if _, ok := v.(string); !ok {
			return false
		}
	}
	return true
}
