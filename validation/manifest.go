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
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Kind       string   `json:"kind"`
	Valid      bool     `json:"valid"`
	Phase      Phase    `json:"phase"`
	Codes      []string `json:"codes"`
	Units      []string `json:"units"`
	Provenance []string `json:"provenance"`
}

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
	knownUnits := map[string]bool{"core": true, "tools": true, "permissions": true, "user-input": true, "recovery": true, "capabilities": true}
	for i, e := range m.Fixtures {
		if filepath.IsAbs(e.Path) || filepath.Clean(e.Path) == ".." || strings.HasPrefix(filepath.Clean(e.Path), ".."+string(filepath.Separator)) {
			return FixtureManifest{}, fmt.Errorf("fixture entry %d has path outside fixture root: %q", i, e.Path)
		}
		if e.ID == "" || e.Path == "" {
			return FixtureManifest{}, fmt.Errorf("fixture entry %d lacks id or path", i)
		}
		if e.Kind != "positive" && e.Kind != "schema-invalid" && e.Kind != "semantic-invalid" {
			return FixtureManifest{}, fmt.Errorf("fixture entry %d has invalid kind %q", i, e.Kind)
		}
		if e.Valid {
			if e.Kind != "positive" || e.Phase != "" || len(e.Codes) != 0 {
				return FixtureManifest{}, fmt.Errorf("valid fixture %q has inconsistent expectation", e.ID)
			}
		} else {
			if e.Kind == "positive" || (e.Phase != PhaseDecode && e.Phase != PhaseSchema && e.Phase != PhaseSemantic) || len(e.Codes) == 0 {
				return FixtureManifest{}, fmt.Errorf("invalid fixture %q lacks a valid phase or diagnostic codes", e.ID)
			}
			for _, code := range e.Codes {
				if !knownCodes[code] {
					return FixtureManifest{}, fmt.Errorf("invalid fixture %q has unknown diagnostic code %q", e.ID, code)
				}
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
	return m, nil
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
