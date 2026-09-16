package validation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func packDir(t *testing.T, names ...string) []string {
	t.Helper()
	dirs := make([]string, 0, len(names))
	for _, name := range names {
		dirs = append(dirs, filepath.Join(repositoryRoot(t), "fixtures", "packs", name))
	}
	return dirs
}

// Every refusal the loader can make, against the pack that makes it. A pack is
// refused rather than diagnosed: the validator never ran, so it has said
// nothing about any trace.
func TestPackLoadRefusals(t *testing.T) {
	cases := []struct {
		dirs  []string
		codes []string
	}{
		{[]string{"bad-unprefixed-name"}, []string{LoadPackUnprefixedName}},
		{[]string{"bad-foreign-prefix"}, []string{LoadPackForeignPrefix}},
		{[]string{"nested-parent", "nested-child"}, []string{LoadPackIDCollision}},
		{[]string{"duplicate-a", "duplicate-b"}, []string{LoadPackIDCollision}},
		{[]string{"bad-branch-undeclared-type"}, []string{LoadPackBranchUndeclaredType}},
		{[]string{"bad-branch-unpinned"}, []string{LoadPackBranchUnpinned}},
		{[]string{"bad-ungated-type"}, []string{LoadPackUngatedType}},
		{[]string{"bad-response-gated"}, []string{LoadPackResponseGated}},
		{[]string{"bad-role-undeclared"}, []string{LoadPackRoleUndeclared}},
		{[]string{"bad-reply-target-unknown"}, []string{LoadPackReplyTargetUnknown}},
		{[]string{"bad-reply-target-ambiguous"}, []string{LoadPackReplyTargetAmbiguous}},
		{[]string{"bad-refusal-undeclared"}, []string{LoadPackRefusalUndeclared}},
		{[]string{"bad-restates-core-member"}, []string{LoadPackRestatesCoreMember}},
		{[]string{"bad-member-target-unknown"}, []string{LoadPackMemberTargetUnknown}},
		{[]string{"bad-schema-path-escape"}, []string{LoadPackSchemaPathEscape}},
		{[]string{"bad-external-ref"}, []string{LoadPackExternalRef}},
		{[]string{"bad-schema-id-escape"}, []string{LoadPackExternalRef}},
		{[]string{"bad-dependency-missing"}, []string{LoadPackDependencyMissing}},
		{[]string{"bad-fixture-claims-core"}, []string{LoadPackFixtureClaimsCore}},
		{[]string{"bad-ext-claim-without-pack"}, []string{LoadExtClaimWithoutPack}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.dirs, "+"), func(t *testing.T) {
			_, err := LoadPacks(packDir(t, tc.dirs...))
			if err == nil {
				t.Fatalf("pack loaded cleanly, want %v", tc.codes)
			}
			refusal, ok := err.(*PackLoadError)
			if !ok {
				t.Fatalf("refused without a load-error code: %v", err)
			}
			if got := strings.Join(refusal.Codes(), ","); got != strings.Join(tc.codes, ",") {
				t.Fatalf("load errors: got %v want %v (%s)", refusal.Codes(), tc.codes, refusal.Error())
			}
		})
	}
}

// A dependency is satisfied only by a loaded pack of that exact id and version.
// Loaded alone, the dependent pack is refused; loaded together, the reference
// into the declared dependency resolves.
func TestPackDependencyIsExactAndDeclared(t *testing.T) {
	if _, err := LoadPacks(packDir(t, "client")); err == nil {
		t.Fatal("a pack loaded without its declared dependency")
	}
	if _, err := LoadPacks(packDir(t, "index", "client")); err != nil {
		t.Fatalf("declared cross-pack reference refused: %v", err)
	}
}

// Containment is what makes two vendors' packs composable: their names cannot
// overlap, so loading both together is safe by construction.
func TestPacksComposeWhenPrefixFree(t *testing.T) {
	packs, err := LoadPacks(packDir(t, "storage", "decoy"))
	if err != nil {
		t.Fatalf("prefix-free packs refused: %v", err)
	}
	set := NewPackSet(packs)
	if set.Type("com.example.storage.objects.read") == nil || set.Type("com.example.decoy.run.started") == nil {
		t.Fatal("a composed set lost one pack's vocabulary")
	}
	if set.Type("run.started") != nil {
		t.Fatal("a pack claimed a core type")
	}
}

// The core claim is unchanged by a pack: the whole core corpus passes
// identically with and without one loaded, run against a pack whose declared
// type is deliberately close to a core one.
func TestCoreClaimUnchangedWithPackLoaded(t *testing.T) {
	manifest := filepath.Join(repositoryRoot(t), "fixtures", "manifest.json")
	packs, err := LoadPacks(packDir(t, "decoy"))
	if err != nil {
		t.Fatal(err)
	}
	withPack, err := NewWith(Options{Packs: packs})
	if err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bare := MustNew()
	root := filepath.Dir(manifest)
	for _, entry := range m.Fixtures {
		if entry.Kind == KindLoadInvalid || len(entry.Packs) > 0 || entry.Mode != "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Path))
		if err != nil {
			t.Fatal(err)
		}
		before := bare.ValidateBytes(data, entry.ID)
		after := withPack.ValidateBytes(data, entry.ID)
		if before.Valid() != after.Valid() || len(before.Diagnostics) != len(after.Diagnostics) {
			t.Fatalf("fixture %s changed meaning when a pack was loaded: %v -> %v", entry.ID, before.Diagnostics, after.Diagnostics)
		}
	}
}

// Loading a pack turns tolerance into conformance for its vocabulary: the same
// envelope is accepted on the common fields alone without the pack and judged
// against the pack's own branch with it.
func TestPackTurnsToleranceIntoConformance(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), "fixtures", "valid", "ext-unpacked-type-tolerated.json"))
	if err != nil {
		t.Fatal(err)
	}
	tolerant, err := NewWith(Options{Mode: ModeTolerant})
	if err != nil {
		t.Fatal(err)
	}
	if result := tolerant.ValidateBytes(data, "unpacked"); !result.Valid() {
		t.Fatalf("an unclaimed type was not tolerated: %v", result.Diagnostics)
	}
	packs, err := LoadPacks(packDir(t, "storage"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{ModeStrict, ModeTolerant} {
		v, err := NewWith(Options{Mode: mode, Packs: packs})
		if err != nil {
			t.Fatal(err)
		}
		result := v.ValidateBytes(data, "packed")
		if result.Valid() || !result.HasCode(CodeSchemaInvalid) {
			t.Fatalf("%s: a malformed packed envelope was accepted with its pack loaded: %v", mode, result.Diagnostics)
		}
	}
}

// A pack adds vocabulary, not a second lifecycle: a packed run-scoped event
// takes part in the run bookkeeping its wire scope implies, so it advances the
// cursor instead of leaving a gap for the next core event to be blamed for.
func TestPackedRunEventAdvancesTheCursor(t *testing.T) {
	packs, err := LoadPacks(packDir(t, "storage"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewWith(Options{Packs: packs})
	if err != nil {
		t.Fatal(err)
	}
	head := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",`
	trace := "[" + strings.Join([]string{
		head + `"type":"capabilities.request","id":"capq","payload":{}}`,
		head + `"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{"com.example.storage.objects":{"level":"native"}}}}`,
		head + `"type":"session.message.submit.request","id":"req1","session_id":"s1","payload":{"delivery":"auto","session_id":"s1","messages":[{"role":"user","content":"go"}]}}`,
		head + `"type":"session.message.submit.response","id":"resp1","in_reply_to":"req1","session_id":"s1","payload":{"accepted":true,"admission":"started","effective_delivery":"start","requested_delivery":"auto","run_id":"r1","session_id":"s1","status":"running","submission_id":"sub1"}}`,
		head + `"type":"run.started","id":"ev1","session_id":"s1","run_id":"r1","sequence":1,"payload":{"run_id":"r1","session_id":"s1","status":"running"}}`,
		head + `"type":"com.example.storage.objects.changed","id":"ev2","session_id":"s1","run_id":"r1","sequence":2,"capability_revision":"v1","payload":{"session_id":"s1","key":"q3.csv"}}`,
		head + `"type":"run.completed","id":"ev3","session_id":"s1","run_id":"r1","sequence":3,"payload":{"final_response":{"role":"assistant","content":"ok"},"run_id":"r1","session_id":"s1","stop_reason":"end_turn"}}`,
	}, ",") + "]"
	if result := v.ValidateBytes([]byte(trace), "packed-run-event"); !result.Valid() {
		t.Fatalf("a packed run event broke the run bookkeeping: %v", result.Diagnostics)
	}
}

// A pack may not amend the protocol. The core pass judges the core projection,
// so a core rule on a core member is neither relaxed nor tightened by a pack:
// an undeclared member is still refused and a missing required member is still
// missing, while the declared member is judged by its own subschema.
func TestCoreProjectionKeepsCoreRules(t *testing.T) {
	packs, err := LoadPacks(packDir(t, "storage"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewWith(Options{Packs: packs})
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",` +
		`"type":"session.message.submit.request","id":"req1","session_id":"s1","payload":{"delivery":"auto",` +
		`"session_id":"s1","messages":[{"role":"user","content":"go"}],"com.example.storage.workspace":{"bucket":"reports"}}}`
	// The lone request draws missing_response either way; the schema phase is
	// what the projection decides.
	if result := v.ValidateBytes([]byte(valid), "member"); result.HasCode(CodeSchemaInvalid) {
		t.Fatalf("a declared member was refused: %v", result.Diagnostics)
	}
	strict := MustNew()
	if result := strict.ValidateBytes([]byte(valid), "member"); !result.HasCode(CodeSchemaInvalid) {
		t.Fatal("the closed core payload accepted an undeclared member without the pack")
	}
}

// An $id rebases the references beneath it, and the compiler registers the
// resource there; the allowlist walk follows both, so a nested identifier
// under another pack is refused along with the reference it rebased.
func TestDocumentReferencesFollowSchemaIDs(t *testing.T) {
	p := &Pack{Base: packBaseURI + "com.example.a/1.0.0/"}
	p.Descriptor.ID = "com.example.a"
	doc := map[string]any{"$defs": map[string]any{"x": map[string]any{
		"$id":        packBaseURI + "com.example.b/1.0.0/x.json",
		"properties": map[string]any{"y": map[string]any{"$ref": "y.json"}},
	}}}
	allowed := map[string]bool{p.Base + "types.schema.json": true, p.Base + "y.json": true}
	refusals := checkDocumentReferences(p, p.Base+"types.schema.json", doc, allowed)
	if len(refusals) != 2 {
		t.Fatalf("refusals = %v, want the identifier and the rebased reference", refusals)
	}
	if !strings.Contains(refusals[0].Message, "schema identifier") || !strings.Contains(refusals[1].Message, packBaseURI+"com.example.b/1.0.0/y.json") {
		t.Fatalf("refusals = %v", refusals)
	}
	// Under the pack's own base an $id is a local alias and constrains nothing.
	doc = map[string]any{"$id": "alias.json", "properties": map[string]any{"y": map[string]any{"$ref": "y.json"}}}
	if refusals := checkDocumentReferences(p, p.Base+"types.schema.json", doc, allowed); len(refusals) != 0 {
		t.Fatalf("in-pack identifier refused: %v", refusals)
	}
}

// A pack's corpus proves its own term only: with a sibling loaded, a fixture
// claiming the sibling's term is refused rather than counted.
func TestPackFixtureClaimsItsOwnTermOnly(t *testing.T) {
	_, err := LoadPacks(packDir(t, "storage", "bad-fixture-claims-sibling"))
	refusal, ok := err.(*PackLoadError)
	if !ok || !strings.Contains(refusal.Error(), "proves its own term") {
		t.Fatalf("sibling claim was not refused: %v", err)
	}
}

// A packed member on capabilities.response is gated on a key that very
// response advertises, so it must be judged after the descriptor is installed.
func TestPackedMemberOnCapabilitiesResponseIsJudgedAfterInstall(t *testing.T) {
	packs, err := LoadPacks(packDir(t, "descriptor-member"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewWith(Options{Packs: packs})
	if err != nil {
		t.Fatal(err)
	}
	head := `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",`
	trace := func(features string) string {
		return "[" + strings.Join([]string{
			head + `"type":"capabilities.request","id":"capq","payload":{}}`,
			head + `"type":"capabilities.response","id":"capr","in_reply_to":"capq","capability_revision":"v1","payload":{"endpoint":{"id":"agent"},"features":{` + features + `},"com.example.descriptor.region":"eu-west"}}`,
		}, ",") + "]"
	}
	if result := v.ValidateBytes([]byte(trace(`"com.example.descriptor.regions":{"level":"native"}`)), "advertised"); !result.Valid() {
		t.Fatalf("member gated on a key the same response advertises was refused: %v", result.Diagnostics)
	}
	if result := v.ValidateBytes([]byte(trace(``)), "unadvertised"); !result.HasCode(CodeUnavailableCapability) {
		t.Fatalf("member on an unadvertised key passed: %v", result.Diagnostics)
	}
}

// The reference walk has no depth cutoff: a reference buried under any amount
// of nesting is still judged, since the compiler would still resolve it.
func TestDocumentReferencesHaveNoDepthCutoff(t *testing.T) {
	p := &Pack{Base: packBaseURI + "com.example.a/1.0.0/"}
	p.Descriptor.ID = "com.example.a"
	var doc any = map[string]any{"$ref": "https://example.invalid/private.schema.json"}
	for i := 0; i < 100; i++ {
		doc = map[string]any{"properties": map[string]any{"x": doc}}
	}
	refusals := checkDocumentReferences(p, p.Base+"types.schema.json", doc, map[string]bool{})
	if len(refusals) != 1 || refusals[0].Code != LoadPackExternalRef {
		t.Fatalf("deep external reference not refused: %v", refusals)
	}
}

// An owned fixture path is resolved and verified beneath the pack root before
// it is opened: a symlink out of the pack is refused, never followed.
func TestPackFixturePathIsContainedBeforeOpen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.json")
	if err := os.WriteFile(outside, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "pack")
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name string, v any) {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("pack.json", map[string]any{
		"id": "com.example.linked", "version": "1.0.0", "schemas": []string{"types.schema.json"},
		"envelope_types": []any{map[string]any{"type": "com.example.linked.ping", "role": "event", "schema": "types.schema.json#/$defs/ping"}},
		"gates":          []any{map[string]any{"type": "com.example.linked.ping", "ungated": true}},
		"fixtures":       "fixtures/manifest.json",
	})
	write("types.schema.json", map[string]any{"$defs": map[string]any{"ping": map[string]any{"type": "object", "properties": map[string]any{"type": map[string]any{"const": "com.example.linked.ping"}}}}})
	write(filepath.Join("fixtures", "manifest.json"), map[string]any{"version": 1, "fixtures": []any{map[string]any{
		"id": "linked", "path": "trace.json", "kind": "positive", "valid": true, "phase": "", "codes": []any{}, "units": []string{"ext:com.example.linked/1.0.0"}, "provenance": []any{},
	}}})
	if err := os.Symlink(outside, filepath.Join(dir, "fixtures", "trace.json")); err != nil {
		t.Fatal(err)
	}
	packs, err := LoadPacks([]string{dir})
	if err != nil {
		t.Fatalf("pack with a symlinked fixture did not load (the refusal belongs to the run, not the load): %v", err)
	}
	v, err := NewWith(Options{Packs: packs})
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.validateManifest(packs[0].fixtures, ManifestOptions{Packs: packs, Owner: packs[0]}, false)
	if err == nil || !strings.Contains(err.Error(), "outside the pack root") {
		t.Fatalf("symlinked fixture was opened: %v", err)
	}
}
