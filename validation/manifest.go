package validation

import (
	"encoding/json"
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
	LoadPackRoleUndeclared       = "pack_role_undeclared"
	LoadPackResponseGated        = "pack_response_gated"
	LoadPackReplyTargetUnknown   = "pack_reply_target_unknown"
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
		LoadPackRestatesCoreMember, LoadPackMemberTargetUnknown, LoadPackRoleUndeclared,
		LoadPackResponseGated, LoadPackReplyTargetUnknown, LoadPackRefusalUndeclared,
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
		CodeUnavailableCapability, CodeStaleCapabilityRevision,
		CodeCancelNotSettled, CodeUndeclaredReplayGap, CodeUnknownParticipant,
		CodeSessionStateMismatch,
	}
	result := make(map[string]bool, len(codes))
	for _, code := range codes {
		result[code] = true
	}
	return result
}

func LoadManifest(filename string) (FixtureManifest, error) {
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
	knownUnits := map[string]bool{"core": true, "tools": true, "permissions": true, "user-input": true, "recovery": true, "capabilities": true}
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
			if e.Valid {
				return FixtureManifest{}, fmt.Errorf("fixture %q covers %q but is positive; coverage is asserted by negative fixtures", e.ID, c.Capability)
			}
		}
		if len(e.Units) == 0 {
			return FixtureManifest{}, fmt.Errorf("fixture %q has no conformance units", e.ID)
		}
		for _, unit := range e.Units {
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
	if err := checkCorpusCompleteness(m); err != nil {
		return FixtureManifest{}, err
	}
	return m, nil
}

// checkCorpusCompleteness requires, for every capability key a claimed unit
// owns, a negative fixture covering its gate aspect and one covering its
// honour aspect. A key whose honour aspect is deferred to another unit must
// have that fixture under the deferring unit instead. The check runs where the
// corpus is loaded, so CI fails on the first key added without its pair.
func checkCorpusCompleteness(m FixtureManifest) error {
	claimed := map[string]bool{}
	covered := map[Coverage][]string{}
	for _, e := range m.Fixtures {
		for _, unit := range e.Units {
			claimed[unit] = true
		}
		if e.Valid {
			continue
		}
		for _, c := range e.Covers {
			covered[c] = append(covered[c], e.Units...)
		}
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
		for _, key := range unitCapabilities[unit] {
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
	m, err := LoadManifest(filename)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(filename)
	listed := map[string]bool{}
	out := make([]FixtureOutcome, 0, len(m.Fixtures))
	for _, entry := range m.Fixtures {
		listed[filepath.Clean(entry.Path)] = true
		if entry.Kind == KindLoadInvalid {
			// The manifest shape admits load-invalid fixtures so a pack's
			// corpus can be declared; running one needs the extension-pack
			// loader, which this build does not carry.
			return out, fmt.Errorf("fixture %s is load-invalid but no pack loader is available in this build", entry.ID)
		}
		f, err := os.Open(filepath.Join(root, entry.Path))
		if err != nil {
			return nil, fmt.Errorf("open fixture %s: %w", entry.ID, err)
		}
		result := v.Validate(f, entry.Path)
		_ = f.Close()
		out = append(out, FixtureOutcome{Entry: entry, Result: result})
		if result.Valid() != entry.Valid {
			return out, fmt.Errorf("fixture %s validity: got %v want %v", entry.ID, result.Valid(), entry.Valid)
		}
		if !entry.Valid {
			if result.PrimaryPhase() != entry.Phase {
				return out, fmt.Errorf("fixture %s phase: got %s want %s", entry.ID, result.PrimaryPhase(), entry.Phase)
			}
			want := append([]string(nil), entry.Codes...)
			got := make([]string, 0, len(result.Diagnostics))
			for _, diagnostic := range result.Diagnostics {
				got = append(got, diagnostic.Code)
			}
			sort.Strings(want)
			sort.Strings(got)
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				return out, fmt.Errorf("fixture %s diagnostic codes: got %v want %v", entry.ID, got, want)
			}
		}
	}
	var unlisted []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && p == filepath.Join(root, "adapters") {
			return fs.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".json" || p == filename {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if !listed[filepath.Clean(rel)] {
			unlisted = append(unlisted, rel)
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		return out, fmt.Errorf("unlisted fixtures: %v", unlisted)
	}
	return out, nil
}
