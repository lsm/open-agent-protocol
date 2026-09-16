package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FixtureManifest struct {
	Version  int            `json:"version"`
	Fixtures []FixtureEntry `json:"fixtures"`
}

type FixtureEntry struct {
	ID         string     `json:"id"`
	Path       string     `json:"path"`
	Kind       string     `json:"kind"`
	Valid      bool       `json:"valid"`
	Phase      Phase      `json:"phase"`
	Codes      []string   `json:"codes"`
	Units      []string   `json:"units"`
	Provenance []string   `json:"provenance"`
	Covers     []Coverage `json:"covers,omitempty"`
	// Mode is the validation mode the fixture is judged in, strict by
	// default, so every entry written before the tolerance step keeps its
	// meaning. Which vocabulary is in force is the entry's stated choice,
	// never inferred from the trace.
	Mode Mode `json:"mode,omitempty"`
	// Packs are the extension packs loaded for this fixture, as paths
	// relative to the manifest. For a load-invalid entry they are the packs
	// loaded beside the one Path names, which is how a refusal that no single
	// pack is at fault for — two ids that are not prefix-free — is stated.
	Packs []string `json:"packs,omitempty"`
}

// Coverage declares which capability promise a fixture falsifies, so the
// corpus-completeness check can find it. Every capability key a unit owns
// needs a negative fixture for each aspect:
//
//   - "gate": the key unadvertised, its operation admitted anyway, and the
//     admission diagnosed.
//   - "honour": the key advertised, a request within every constraint the
//     endpoint disclosed and carrying no defect any rule names, refused, and
//     the refusal diagnosed.
//
// The second is the class an endpoint can otherwise pass conformance without
// ever honouring: advertise the key, refuse everything, pass.
type Coverage struct {
	Capability string `json:"capability"`
	Aspect     string `json:"aspect"`
}

const (
	AspectGate   = "gate"
	AspectHonour = "honour"
)

// Fixture kinds. A load-invalid fixture asserts that loading a resource — an
// extension pack — fails before any trace is read; its path names the pack
// and its codes come from the load-error vocabulary, not from the validator's
// diagnostics, because a load error is what the loader says about a pack and a
// diagnostic is what the validator says about a trace.
const (
	KindPositive        = "positive"
	KindSchemaInvalid   = "schema-invalid"
	KindSemanticInvalid = "semantic-invalid"
	KindLoadInvalid     = "load-invalid"
)

// Load-error codes, one per refusal the extension-pack loader can make. The
// vocabulary lands with the manifest shape so a pack's fixtures can be
// declared before the loader exists; nothing produces these codes yet.
const (
	LoadPackUnprefixedName       = "pack_unprefixed_name"
	LoadPackForeignPrefix        = "pack_foreign_prefix"
	LoadPackIDCollision          = "pack_id_collision"
	LoadPackBranchUndeclaredType = "pack_branch_undeclared_type"
	LoadPackBranchUnpinned       = "pack_branch_unpinned"
	LoadPackUngatedType          = "pack_ungated_type"
	LoadPackRestatesCoreMember   = "pack_restates_core_member"
	LoadPackMemberTargetUnknown  = "pack_member_target_unknown"
	LoadPackMemberDuplicate      = "pack_member_duplicate"
	LoadPackRoleUndeclared       = "pack_role_undeclared"
	LoadPackResponseGated        = "pack_response_gated"
	LoadPackReplyTargetUnknown   = "pack_reply_target_unknown"
	LoadPackReplyTargetAmbiguous = "pack_reply_target_ambiguous"
	LoadPackRefusalUndeclared    = "pack_refusal_undeclared"
	LoadPackSchemaPathEscape     = "pack_schema_path_escape"
	LoadPackExternalRef          = "pack_external_ref"
	LoadPackDependencyMissing    = "pack_dependency_missing"
	LoadPackFixtureClaimsCore    = "pack_fixture_claims_core_unit"
	LoadExtClaimWithoutPack      = "ext_claim_without_pack"
)

func loadErrorCodes() map[string]bool {
	codes := []string{
		LoadPackUnprefixedName, LoadPackForeignPrefix, LoadPackIDCollision,
		LoadPackBranchUndeclaredType, LoadPackBranchUnpinned, LoadPackUngatedType,
		LoadPackRestatesCoreMember, LoadPackMemberTargetUnknown, LoadPackMemberDuplicate, LoadPackRoleUndeclared,
		LoadPackResponseGated, LoadPackReplyTargetUnknown, LoadPackReplyTargetAmbiguous, LoadPackRefusalUndeclared,
		LoadPackSchemaPathEscape, LoadPackExternalRef, LoadPackDependencyMissing,
		LoadPackFixtureClaimsCore, LoadExtClaimWithoutPack,
	}
	result := make(map[string]bool, len(codes))
	for _, code := range codes {
		result[code] = true
	}
	return result
}

// unitCapabilities lists the capability keys each conformance unit owns. A
// unit registers its keys when it graduates, and from that moment the corpus
// must carry a negative gate fixture and a negative honour fixture for each,
// or the manifest fails to load. It is empty until the first unit that
// introduces executable capability keys lands; the check is in place so that
// unit is the first held to it rather than the first grandfathered past it.
var unitCapabilities = map[string][]string{}

// honourDeferred names the unit whose corpus carries a key's honour fixture
// when the key's own unit cannot falsify it yet — a stated deferral rather
// than a silent gap. Empty until a unit declares one.
var honourDeferred = map[string]string{}

type FixtureOutcome struct {
	Entry  FixtureEntry
	Result Result
}

func diagnosticCodes() map[string]bool {
	codes := []string{
		CodeMalformedJSON, CodeDuplicateKey, CodeSchemaInvalid, CodePayloadDecode,
		CodeDuplicateEnvelopeID, CodeScopeMismatch, CodeUnmatchedResponse,
		CodeMissingResponse, CodeDuplicateResponse, CodeSequenceGap,
		CodeSequenceRegression, CodeIllegalRunTransition, CodeMissingRunStarted,
		CodeMissingRunTerminal, CodeDuplicateRunTerminal, CodeEventAfterTerminal,
		CodePendingToolAtTerminal, CodePendingInteractionAtTerminal,
		CodeUnmatchedTool, CodeIllegalToolTransition, CodeDuplicateInteraction,
		CodeUnmatchedInteraction, CodeWrongInteractionResponder,
		CodeUnavailableCapability, CodeUnhonouredCapability, CodeStaleCapabilityRevision,
		CodeCancelNotSettled, CodeUndeclaredReplayGap, CodeUnknownParticipant,
		CodeSessionStateMismatch,
	}
	result := make(map[string]bool, len(codes))
	for _, code := range codes {
		result[code] = true
	}
	return result
}

// ManifestOptions scopes a manifest to the extension vocabulary in force.
//
// Packs are the packs loaded for the run: an `ext:<pack id>/<version>` unit
// term is accepted exactly when one of them matches, so the unit list is
// derived from the packs actually loaded rather than added to the hard-coded
// map, and a stale or misspelled pack claim still fails closed. Owner is set
// when the manifest is a pack's own: the two directions are kept apart
// deliberately, because a pack that could contribute evidence toward a core
// unit would widen a core claim.
type ManifestOptions struct {
	Packs []*Pack
	Owner *Pack
}

// LoadManifest reads a core fixture manifest with no extension vocabulary in
// force. It is what every existing caller gets.
func LoadManifest(filename string) (FixtureManifest, error) {
	return LoadManifestWith(filename, ManifestOptions{})
}

// LoadManifestWith reads a fixture manifest under the extension vocabulary the
// options name.
func LoadManifestWith(filename string, opts ManifestOptions) (FixtureManifest, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return FixtureManifest{}, err
	}
	var m FixtureManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return FixtureManifest{}, fmt.Errorf("decode fixture manifest: %w", err)
	}
	if m.Version != 1 {
		return FixtureManifest{}, fmt.Errorf("unsupported fixture manifest version %d", m.Version)
	}
	ids := map[string]bool{}
	paths := map[string]bool{}
	knownCodes := diagnosticCodes()
	knownLoadErrors := loadErrorCodes()
	knownUnits := map[string]bool{"core": true, "tools": true, "permissions": true, "user-input": true, "recovery": true, "capabilities": true, "extensions": true}
	extensionUnits := map[string]bool{}
	for _, pack := range opts.Packs {
		extensionUnits[pack.Unit()] = true
	}
	// A unit that registers capability keys is known by that registration,
	// and so is any unit a key's honour fixture is deferred to: one place to
	// declare a graduated unit, not two that can disagree.
	for unit := range unitCapabilities {
		knownUnits[unit] = true
	}
	for _, unit := range honourDeferred {
		knownUnits[unit] = true
	}
	for i, e := range m.Fixtures {
		if filepath.IsAbs(e.Path) || filepath.Clean(e.Path) == ".." || strings.HasPrefix(filepath.Clean(e.Path), ".."+string(filepath.Separator)) {
			return FixtureManifest{}, fmt.Errorf("fixture entry %d has path outside fixture root: %q", i, e.Path)
		}
		if e.ID == "" || e.Path == "" {
			return FixtureManifest{}, fmt.Errorf("fixture entry %d lacks id or path", i)
		}
		switch e.Kind {
		case KindPositive, KindSchemaInvalid, KindSemanticInvalid, KindLoadInvalid:
		default:
			return FixtureManifest{}, fmt.Errorf("fixture entry %d has invalid kind %q", i, e.Kind)
		}
		if e.Valid {
			if e.Kind != KindPositive || e.Phase != "" || len(e.Codes) != 0 {
				return FixtureManifest{}, fmt.Errorf("valid fixture %q has inconsistent expectation", e.ID)
			}
		} else if e.Kind == KindLoadInvalid {
			// A load refusal happens before any trace is read, so its phase
			// is `load` and its codes are load errors, never diagnostics.
			if e.Phase != PhaseLoad || len(e.Codes) == 0 {
				return FixtureManifest{}, fmt.Errorf("load-invalid fixture %q must have phase %q and load-error codes", e.ID, PhaseLoad)
			}
			for _, code := range e.Codes {
				if !knownLoadErrors[code] {
					return FixtureManifest{}, fmt.Errorf("load-invalid fixture %q has unknown load-error code %q", e.ID, code)
				}
			}
		} else {
			if e.Kind == KindPositive || (e.Phase != PhaseDecode && e.Phase != PhaseSchema && e.Phase != PhaseSemantic) || len(e.Codes) == 0 {
				return FixtureManifest{}, fmt.Errorf("invalid fixture %q lacks a valid phase or diagnostic codes", e.ID)
			}
			for _, code := range e.Codes {
				if !knownCodes[code] {
					return FixtureManifest{}, fmt.Errorf("invalid fixture %q has unknown diagnostic code %q", e.ID, code)
				}
			}
		}
		for _, c := range e.Covers {
			if strings.TrimSpace(c.Capability) == "" {
				return FixtureManifest{}, fmt.Errorf("fixture %q covers an empty capability", e.ID)
			}
			if c.Aspect != AspectGate && c.Aspect != AspectHonour {
				return FixtureManifest{}, fmt.Errorf("fixture %q covers %q with unknown aspect %q", e.ID, c.Capability, c.Aspect)
			}
			// Only a semantic-invalid fixture exercises the gate or honour
			// behaviour and its refusal diagnostic; a schema- or load-invalid
			// one fails before that behaviour is reached and proves nothing
			// about it.
			if e.Kind != KindSemanticInvalid {
				return FixtureManifest{}, fmt.Errorf("fixture %q covers %q but is %s; coverage is asserted by semantic-invalid fixtures", e.ID, c.Capability, e.Kind)
			}
		}
		if e.Mode != "" && e.Mode != ModeStrict && e.Mode != ModeTolerant {
			return FixtureManifest{}, fmt.Errorf("fixture %q has unsupported validation mode %q", e.ID, e.Mode)
		}
		for _, pack := range e.Packs {
			if filepath.IsAbs(pack) || filepath.Clean(pack) == ".." || strings.HasPrefix(filepath.Clean(pack), ".."+string(filepath.Separator)) {
				return FixtureManifest{}, fmt.Errorf("fixture %q names a pack outside the fixture root: %q", e.ID, pack)
			}
		}
		if opts.Owner != nil && len(e.Packs) > 0 {
			return FixtureManifest{}, fmt.Errorf("fixture %q is a pack fixture and cannot load further packs", e.ID)
		}
		if len(e.Units) == 0 {
			return FixtureManifest{}, fmt.Errorf("fixture %q has no conformance units", e.ID)
		}
		for _, unit := range e.Units {
			if strings.HasPrefix(unit, extensionUnitPrefix) {
				if !extensionUnits[unit] {
					return FixtureManifest{}, &PackLoadError{Refusals: []PackRefusal{{
						Code:    LoadExtClaimWithoutPack,
						Pack:    ownerID(opts.Owner),
						Message: fmt.Sprintf("fixture %q claims %q, and no pack of that id and version is loaded", e.ID, unit),
					}}}
				}
				if opts.Owner != nil && unit != opts.Owner.Unit() {
					// A pack's corpus proves its own term only. A fixture
					// claiming a sibling pack's term would let the pack leave
					// its own term unclaimed — and with it every capability
					// key uncovered, since completeness is judged over claimed
					// units — while still loading.
					return FixtureManifest{}, &PackLoadError{Refusals: []PackRefusal{{
						Pack:    opts.Owner.ID(),
						Message: fmt.Sprintf("fixture %q claims %q; a pack's corpus proves its own term %q only", e.ID, unit, opts.Owner.Unit()),
					}}}
				}
				continue
			}
			if opts.Owner != nil {
				// A pack's fixture may not claim a core unit: without that a
				// pack could contribute evidence toward a core claim, which is
				// the one thing "a pack can never widen a core claim" has to
				// mean.
				return FixtureManifest{}, &PackLoadError{Refusals: []PackRefusal{{
					Code:    LoadPackFixtureClaimsCore,
					Pack:    opts.Owner.ID(),
					Message: fmt.Sprintf("fixture %q claims the core unit %q; a pack's corpus proves its own term only", e.ID, unit),
				}}}
			}
			if !knownUnits[unit] {
				return FixtureManifest{}, fmt.Errorf("fixture %q has unknown conformance unit %q", e.ID, unit)
			}
		}
		for _, provenance := range e.Provenance {
			if strings.TrimSpace(provenance) == "" {
				return FixtureManifest{}, fmt.Errorf("fixture %q has empty provenance", e.ID)
			}
		}
		if ids[e.ID] {
			return FixtureManifest{}, fmt.Errorf("duplicate fixture id %q", e.ID)
		}
		if paths[e.Path] {
			return FixtureManifest{}, fmt.Errorf("duplicate fixture path %q", e.Path)
		}
		ids[e.ID] = true
		paths[e.Path] = true
	}
	if err := checkCorpusCompleteness(m, nil); err != nil {
		return FixtureManifest{}, err
	}
	return m, nil
}

// extensionUnitPrefix marks a conformance term belonging to a pack rather than
// to the spec. The executable claim is unchanged for core and gains one
// independent term per pack, so "conformant to a vendor's extension" is a claim
// with the same executable meaning as a core unit, stated by the vendor,
// checked by the same tool, and carrying no authority over the core claim
// beside it.
const extensionUnitPrefix = "ext:"

func ownerID(owner *Pack) string {
	if owner == nil {
		return ""
	}
	return owner.ID()
}

// checkCorpusCompleteness requires, for every capability key a claimed unit
// owns, a negative fixture covering its gate aspect and one covering its
// honour aspect. A key whose honour aspect is deferred to another unit must
// have that fixture under the deferring unit instead. The check runs where the
// corpus is loaded, so CI fails on the first key added without its pair.
func checkCorpusCompleteness(m FixtureManifest, extra map[string][]string) error {
	keys := unitCapabilities
	if len(extra) > 0 {
		// A pack's keys are the same rule over a unit the hard-coded map
		// cannot name: a vendor cannot claim conformance for a key nothing
		// could show it dishonouring, which is the protection core gets.
		keys = make(map[string][]string, len(unitCapabilities)+len(extra))
		for unit, owned := range unitCapabilities {
			keys[unit] = owned
		}
		for unit, owned := range extra {
			keys[unit] = append(append([]string(nil), keys[unit]...), owned...)
		}
	}
	claimed := map[string]bool{}
	covered := map[Coverage][]string{}
	for _, e := range m.Fixtures {
		for _, unit := range e.Units {
			claimed[unit] = true
		}
		if e.Kind != KindSemanticInvalid {
			continue
		}
		for _, c := range e.Covers {
			covered[c] = append(covered[c], e.Units...)
		}
	}
	for unit := range extra {
		// A pack's own unit is checked whether or not any fixture claims it:
		// an empty corpus claims nothing and would otherwise owe nothing,
		// which is exactly the pack this rule exists to refuse.
		claimed[unit] = true
	}
	units := make([]string, 0, len(claimed))
	for unit := range claimed {
		units = append(units, unit)
	}
	sort.Strings(units)
	var missing []string
	coveredUnder := func(c Coverage, unit string) bool {
		for _, u := range covered[c] {
			if u == unit {
				return true
			}
		}
		return false
	}
	for _, unit := range units {
		for _, key := range keys[unit] {
			// Both aspects must be covered under the unit that owns the key
			// (or, for honour, the unit it is deferred to): a fixture under
			// an unrelated unit naming the same pair does not stand in for
			// the owning unit's own corpus.
			if !coveredUnder(Coverage{Capability: key, Aspect: AspectGate}, unit) {
				missing = append(missing, fmt.Sprintf("%s: %s has no negative %s fixture under unit %s", unit, key, AspectGate, unit))
			}
			honourUnit := unit
			if deferred, ok := honourDeferred[key]; ok {
				honourUnit = deferred
			}
			if !coveredUnder(Coverage{Capability: key, Aspect: AspectHonour}, honourUnit) {
				missing = append(missing, fmt.Sprintf("%s: %s has no negative %s fixture under unit %s", unit, key, AspectHonour, honourUnit))
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("corpus completeness: %s", strings.Join(missing, "; "))
	}
	return nil
}

func (v *Validator) ValidateManifest(filename string) ([]FixtureOutcome, error) {
	return v.validateManifest(filename, ManifestOptions{}, true)
}

// validateManifest runs one manifest. A core manifest runs its entries and then
// the corpus of every pack those entries load, so a pack's fixtures run under
// the same runner as the spec's. A pack's own manifest runs its entries under
// the pack that owns it and loads nothing further.
func (v *Validator) validateManifest(filename string, opts ManifestOptions, corpora bool) ([]FixtureOutcome, error) {
	m, err := LoadManifestWith(filename, opts)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(filename)
	listed := map[string]bool{}
	out := make([]FixtureOutcome, 0, len(m.Fixtures))
	validators := map[string]*Validator{}
	loaded := map[string][]*Pack{}
	for _, entry := range m.Fixtures {
		listed[filepath.Clean(entry.Path)] = true
		if entry.Kind == KindLoadInvalid {
			outcome, err := runLoadFixture(root, entry)
			out = append(out, outcome)
			if err != nil {
				return out, err
			}
			continue
		}
		validator := v
		if entry.Mode != "" || len(entry.Packs) > 0 || opts.Owner != nil {
			key := string(entry.Mode) + "\x00" + strings.Join(entry.Packs, "\x00")
			if validators[key] == nil {
				packs := opts.Packs
				if opts.Owner == nil {
					packs, err = loadEntryPacks(root, entry)
					if err != nil {
						return out, fmt.Errorf("fixture %s: %w", entry.ID, err)
					}
					loaded[key] = packs
				}
				built, err := NewWith(Options{Mode: entry.Mode, Packs: packs})
				if err != nil {
					return out, fmt.Errorf("fixture %s: %w", entry.ID, err)
				}
				validators[key] = built
			}
			validator = validators[key]
		}
		outcome, err := runTraceFixture(validator, root, ownerRoot(opts.Owner), entry)
		out = append(out, outcome)
		if err != nil {
			return out, err
		}
	}
	if err := checkUnlisted(root, filename, listed); err != nil {
		return out, err
	}
	if !corpora {
		return out, nil
	}
	for _, packs := range loaded {
		for _, pack := range packs {
			if pack.fixtures == "" {
				continue
			}
			corpus, err := NewWith(Options{Packs: packs})
			if err != nil {
				return out, fmt.Errorf("pack %s corpus: %w", pack.ID(), err)
			}
			outcomes, err := corpus.validateManifest(pack.fixtures, ManifestOptions{Packs: packs, Owner: pack}, false)
			out = append(out, outcomes...)
			if err != nil {
				return out, fmt.Errorf("pack %s corpus: %w", pack.ID(), err)
			}
		}
	}
	return out, nil
}

// loadEntryPacks loads the packs one fixture names, as paths relative to the
// manifest. Packs are opt-in per entry for the same reason the mode is: which
// vocabulary is in force must be a stated choice, not inferred from the trace.
func loadEntryPacks(root string, entry FixtureEntry) ([]*Pack, error) {
	if len(entry.Packs) == 0 {
		return nil, nil
	}
	dirs := make([]string, 0, len(entry.Packs))
	for _, pack := range entry.Packs {
		dirs = append(dirs, filepath.Join(root, pack))
	}
	return LoadPacks(dirs)
}

// runLoadFixture asserts that loading the named packs fails with exactly the
// declared load-error codes. An entry that loads cleanly, or fails with
// different codes, fails the fixture: a refusal nobody can reproduce is not a
// rule.
func runLoadFixture(root string, entry FixtureEntry) (FixtureOutcome, error) {
	dirs := make([]string, 0, len(entry.Packs)+1)
	dirs = append(dirs, filepath.Join(root, entry.Path))
	for _, pack := range entry.Packs {
		dirs = append(dirs, filepath.Join(root, pack))
	}
	outcome := FixtureOutcome{Entry: entry}
	_, err := LoadPacks(dirs)
	if err == nil {
		return outcome, fmt.Errorf("fixture %s: pack loaded cleanly, want load errors %v", entry.ID, entry.Codes)
	}
	var refusal *PackLoadError
	if !errors.As(err, &refusal) {
		return outcome, fmt.Errorf("fixture %s: pack refused without a load-error code: %w", entry.ID, err)
	}
	got := refusal.Codes()
	for _, code := range got {
		outcome.Result.Diagnostics = append(outcome.Result.Diagnostics, Diagnostic{Fixture: entry.Path, Phase: PhaseLoad, Code: code, Message: refusal.Error()})
	}
	want := append([]string(nil), entry.Codes...)
	sort.Strings(want)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		return outcome, fmt.Errorf("fixture %s load errors: got %v want %v (%s)", entry.ID, got, want, refusal.Error())
	}
	return outcome, nil
}

// ownerRoot is the pack root an owned manifest's fixtures must stay beneath,
// or "" for the core corpus.
func ownerRoot(owner *Pack) string {
	if owner == nil {
		return ""
	}
	return owner.Root
}

func runTraceFixture(v *Validator, root, packRoot string, entry FixtureEntry) (FixtureOutcome, error) {
	path := filepath.Join(root, entry.Path)
	if packRoot != "" {
		// A pack's fixtures are third-party documents. The lexical check at
		// load keeps the path beneath the manifest, but a symlink can still
		// lead anywhere and a FIFO would block the run, so the path is
		// resolved and verified before it is opened, as the loader does for
		// the schemas and the manifest itself.
		rel, err := filepath.Rel(packRoot, path)
		if err != nil {
			return FixtureOutcome{Entry: entry}, fmt.Errorf("fixture %s: path %q: %w", entry.ID, entry.Path, err)
		}
		resolved, err := containedPath(packRoot, rel)
		if err != nil {
			return FixtureOutcome{Entry: entry}, fmt.Errorf("fixture %s: path %q: %w", entry.ID, entry.Path, err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return FixtureOutcome{Entry: entry}, fmt.Errorf("fixture %s: path %q: %w", entry.ID, entry.Path, err)
		}
		if !info.Mode().IsRegular() {
			return FixtureOutcome{Entry: entry}, fmt.Errorf("fixture %s: path %q is not a regular file", entry.ID, entry.Path)
		}
		path = resolved
	}
	f, err := os.Open(path)
	if err != nil {
		return FixtureOutcome{Entry: entry}, fmt.Errorf("open fixture %s: %w", entry.ID, err)
	}
	result := v.Validate(f, entry.Path)
	_ = f.Close()
	outcome := FixtureOutcome{Entry: entry, Result: result}
	if result.Valid() != entry.Valid {
		return outcome, fmt.Errorf("fixture %s validity: got %v want %v (%v)", entry.ID, result.Valid(), entry.Valid, result.Diagnostics)
	}
	if !entry.Valid {
		if result.PrimaryPhase() != entry.Phase {
			return outcome, fmt.Errorf("fixture %s phase: got %s want %s", entry.ID, result.PrimaryPhase(), entry.Phase)
		}
		want := append([]string(nil), entry.Codes...)
		got := make([]string, 0, len(result.Diagnostics))
		for _, diagnostic := range result.Diagnostics {
			got = append(got, diagnostic.Code)
		}
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			return outcome, fmt.Errorf("fixture %s diagnostic codes: got %v want %v", entry.ID, got, want)
		}
	}
	return outcome, nil
}

// checkUnlisted refuses a corpus carrying a trace nothing declares. Pack
// directories are skipped the way the adapter corpora are: their contents are
// declared by the pack descriptor and the pack's own manifest, not by this one.
func checkUnlisted(root, filename string, listed map[string]bool) error {
	var unlisted []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && (p == filepath.Join(root, "adapters") || p == filepath.Join(root, "packs")) {
			return fs.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".json" || p == filename {
			return nil
		}
		if base := filepath.Base(p); base == "pack.json" || strings.HasSuffix(base, ".schema.json") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if !listed[filepath.Clean(rel)] {
			unlisted = append(unlisted, rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		return fmt.Errorf("unlisted fixtures: %v", unlisted)
	}
	return nil
}
