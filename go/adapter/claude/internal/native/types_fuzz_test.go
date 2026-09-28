package native

import (
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

func FuzzAnObservationIsRefusedForAFrameTypeItDoesNotServeWhateverTheRawSays(f *testing.F) {
	for _, frameType := range []string{TypeUser, TypeResult, TypeCommandLifecycle, "assistant", "", "USER"} {
		for _, subtype := range []string{"", ResultErrorMaxTurns, "some_subtype"} {
			for _, raw := range []string{`{}`, `not json`, `{"message":{"content":[]}}`, `[]`} {
				f.Add(frameType, subtype, raw)
			}
		}
	}
	f.Fuzz(func(t *testing.T, frameType, subtype, raw string) {
		observation, err := DecodeObservation(frameType, subtype, []byte(raw))
		if err != nil {
			if observation != nil {
				t.Fatalf("a refused observation returned the value %+v: %q", observation, raw)
			}
			return
		}
		unknown, isUnknown := observation.(*UnknownFrame)
		if !servedObservationFrame(frameType) {
			if !isUnknown {
				t.Fatalf("the unserved frame type %q returned the typed observation %+v: %q", frameType, observation, raw)
			}
			if unknown.Type != frameType || unknown.Subtype != subtype {
				t.Fatalf("the frame %q/%q is reported as %q/%q", frameType, subtype, unknown.Type, unknown.Subtype)
			}
			return
		}
		if isUnknown && unknown.Type != TypeKeepAlive {
			t.Fatalf("the served frame type %q was reported as unknown: %q", frameType, raw)
		}
	})
}

func FuzzAControlRequestIsRefusedForASubtypeItDoesNotServeWhateverTheRawSays(f *testing.F) {
	for _, subtype := range []string{ControlInitialize, ControlInterrupt, ControlCanUseTool, ControlHookCallback, "control.other", ""} {
		for _, raw := range []string{`{}`, `not json`, `{"tool_name":"Read"}`} {
			f.Add(subtype, raw)
		}
	}
	f.Fuzz(func(t *testing.T, subtype, raw string) {
		request, err := DecodeControlRequest(subtype, []byte(raw))
		if err != nil {
			if request != nil {
				t.Fatalf("a refused control request returned the value %+v: %q", request, raw)
			}
			return
		}
		unknown, isUnknown := request.(*UnknownFrame)
		if !servedControlSubtype(subtype) {
			if !isUnknown {
				t.Fatalf("the unserved control subtype %q returned the typed request %+v: %q", subtype, request, raw)
			}
			if unknown.Subtype != subtype {
				t.Fatalf("the unserved control subtype %q is reported as %q", subtype, unknown.Subtype)
			}
			return
		}
		if isUnknown {
			t.Fatalf("the served control subtype %q was reported as unknown: %q", subtype, raw)
		}
	})
}

func servedObservationFrame(frameType string) bool {
	switch frameType {
	case TypeUser, TypeAssistant, TypeResult, TypeSystem, TypeCommandLifecycle, TypeKeepAlive:
		return true
	default:
		return false
	}
}

func servedControlSubtype(subtype string) bool {
	switch subtype {
	case ControlCanUseTool, ControlHookCallback:
		return true
	default:
		return false
	}
}

func claudeSeeds(f *testing.F) []string {
	{
		bodies, err := fuzzseed.Corpus("claude-code", fuzzseed.DefaultLimit)
		if err != nil {
			{
				f.Fatal(err)
			}
		}
		if len(bodies) == 0 {
			{
				f.Fatalf("the catalog's corpus for %s is empty, so this target starts from its own literals only", "claude-code")
			}
		}
		seeds := make([]string, 0, len(bodies))
		for _, body := range bodies {
			{
				seeds = append(seeds, string(body))
			}
		}
		return seeds
	}
}
