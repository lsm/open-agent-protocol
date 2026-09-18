package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
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

	Mode Mode `json:"mode,omitempty"`

	Profile string `json:"profile,omitempty"`

	Packs []string `json:"packs,omitempty"`
}

type Coverage struct {
	Capability string `json:"capability"`
	Aspect     string `json:"aspect"`
}

const (
	AspectGate   = "gate"
	AspectHonour = "honour"
)

const ProfileModelProvider = "model-provider-core"

var providerUnits = map[string]bool{"provider-core": true, "credentials": true, "carry": true}

func providerDiagnosticCodes() map[string]bool {
	return map[string]bool{
		CodeMalformedJSON:       true,
		CodeDuplicateKey:        true,
		CodePayloadDecode:       true,
		CodeSchemaInvalid:       true,
		CodeCredentialInTrace:   true,
		CodeCredentialInHeaders: true,
		CodeTerminalNotAssembly: true,
		CodeEventAfterTerminal:  true,
		CodeSequenceGap:         true,
		CodeSequenceRegression:  true,
	}
}

const (
	KindPositive        = "positive"
	KindSchemaInvalid   = "schema-invalid"
	KindSemanticInvalid = "semantic-invalid"
	KindLoadInvalid     = "load-invalid"
)

const (
	LoadPackUnprefixedName       = "pack_unprefixed_name"
	LoadPackForeignPrefix        = "pack_foreign_prefix"
	LoadPackIDCollision          = "pack_id_collision"
	LoadPackTypeDuplicate        = "pack_type_duplicate"
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
		LoadPackUnprefixedName, LoadPackForeignPrefix, LoadPackIDCollision, LoadPackTypeDuplicate,
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

var unitCapabilities = map[string][]string{

	"run-controls": {
		protocol.FeatureModelSelection,
		protocol.FeatureInstructions,
		protocol.FeatureToolSelection,
		protocol.FeatureStructuredOutput,
	},

	"queue": {protocol.FeatureDeliveryQueue},

	"tool-sources": {
		protocol.FeatureToolsList,
		protocol.FeatureToolSourcesAttach,
	},

	"control-tools": {protocol.FeatureToolsProvide},

	"models":        {protocol.FeatureModelsList},
	"compound-open": {protocol.FeatureOpenSubscribe},
}

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
		CodeUnappliedControl, CodeUnsatisfiableControl, CodeDegradedWithoutOptin,
		CodeDuplicateToolName, CodeUndisclosedSelectionModes,
		CodeQueueOrderViolation, CodeQueueLimitExceeded,
		CodePrematureSessionMutation, CodeUndisclosedQueueLimit,
		CodeUnmatchedToolSource, CodeDuplicateToolSource, CodeCatalogMismatch,
		CodeAttachmentFieldInCatalog, CodeUndisclosedAttachLimit, CodeUndisclosedAttachModes, CodeUnattributedCall,
		CodeWrongToolOwner, CodeUndisclosedProvideLimit, CodeResolutionPayloadMismatch,
		CodeModelNotInCatalog, CodeAmbiguousDefaultModel, CodeDuplicateModelID, CodeUnannouncedCatalogChange,
		CodeUnmatchedProvider, CodeDuplicateProvider,
	}
	result := make(map[string]bool, len(codes))
	for _, code := range codes {
		result[code] = true
	}
	return result
}

type ManifestOptions struct {
	Packs []*Pack
	Owner *Pack
}

func LoadManifest(filename string) (FixtureManifest, error) {
	return LoadManifestWith(filename, ManifestOptions{})
}

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
		if e.Profile != "" && e.Profile != ProfileModelProvider {
			return FixtureManifest{}, fmt.Errorf("fixture entry %d has unknown profile %q", i, e.Profile)
		}
		if e.Profile == ProfileModelProvider {
			if e.Kind == KindLoadInvalid || e.Mode != "" || len(e.Packs) > 0 || len(e.Covers) > 0 {
				return FixtureManifest{}, fmt.Errorf("fixture %q is a provider fixture and cannot carry packs, modes or capability coverage", e.ID)
			}
			if len(e.Units) == 0 {
				return FixtureManifest{}, fmt.Errorf("fixture %q has no conformance units", e.ID)
			}
			for _, unit := range e.Units {
				if !providerUnits[unit] {
					return FixtureManifest{}, fmt.Errorf("fixture %q has unknown provider conformance unit %q", e.ID, unit)
				}
			}
			if e.Valid {
				if e.Kind != KindPositive || e.Phase != "" || len(e.Codes) > 0 {
					return FixtureManifest{}, fmt.Errorf("valid fixture %q carries a non-positive kind, a phase or diagnostic codes", e.ID)
				}
			}
			if !e.Valid {
				if e.Kind == KindPositive {
					return FixtureManifest{}, fmt.Errorf("invalid fixture %q is marked positive", e.ID)
				}
				if e.Phase != PhaseDecode && e.Phase != PhaseSchema && e.Phase != PhaseSemantic {
					return FixtureManifest{}, fmt.Errorf("invalid fixture %q lacks a valid phase", e.ID)
				}
				if len(e.Codes) == 0 {
					return FixtureManifest{}, fmt.Errorf("invalid fixture %q lacks diagnostic codes", e.ID)
				}
				known := providerDiagnosticCodes()
				for _, code := range e.Codes {
					if !known[code] {
						return FixtureManifest{}, fmt.Errorf("invalid fixture %q has a diagnostic code the provider validator cannot emit: %q", e.ID, code)
					}
				}
			}
			if ids[e.ID] {
				return FixtureManifest{}, fmt.Errorf("duplicate fixture id %q", e.ID)
			}
			ids[e.ID] = true
			if paths[filepath.Clean(e.Path)] {
				return FixtureManifest{}, fmt.Errorf("duplicate fixture path %q", e.Path)
			}
			paths[filepath.Clean(e.Path)] = true
			continue
		}
		if e.Valid {
			if e.Kind != KindPositive || e.Phase != "" || len(e.Codes) != 0 {
				return FixtureManifest{}, fmt.Errorf("valid fixture %q has inconsistent expectation", e.ID)
			}
		} else if e.Kind == KindLoadInvalid {

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

					return FixtureManifest{}, &PackLoadError{Refusals: []PackRefusal{{
						Pack:    opts.Owner.ID(),
						Message: fmt.Sprintf("fixture %q claims %q; a pack's corpus proves its own term %q only", e.ID, unit, opts.Owner.Unit()),
					}}}
				}
				continue
			}
			if opts.Owner != nil {

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

const extensionUnitPrefix = "ext:"

func ownerID(owner *Pack) string {
	if owner == nil {
		return ""
	}
	return owner.ID()
}

func checkCorpusCompleteness(m FixtureManifest, extra map[string][]string) error {
	keys := unitCapabilities
	if len(extra) > 0 {

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

func (v *Validator) validateManifest(filename string, opts ManifestOptions, corpora bool) ([]FixtureOutcome, error) {
	m, err := LoadManifestWith(filename, opts)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(filename)
	listed := map[string]bool{}
	out := make([]FixtureOutcome, 0, len(m.Fixtures))
	validators := map[string]*Validator{}
	var providerValidator *ProviderValidator
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
		if entry.Profile == ProfileModelProvider {
			if providerValidator == nil {
				providerValidator, err = NewProviderValidator()
				if err != nil {
					return out, fmt.Errorf("fixture %s: %w", entry.ID, err)
				}
			}
			outcome, err := runTraceFixture(providerValidator, root, ownerRoot(opts.Owner), entry)
			out = append(out, outcome)
			if err != nil {
				return out, err
			}
			continue
		}
		var validator traceValidator = v
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

func ownerRoot(owner *Pack) string {
	if owner == nil {
		return ""
	}
	return owner.Root
}

type traceValidator interface {
	Validate(r io.Reader, fixture string) Result
}

func runTraceFixture(v traceValidator, root, packRoot string, entry FixtureEntry) (FixtureOutcome, error) {
	path := filepath.Join(root, entry.Path)
	if packRoot != "" {

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
