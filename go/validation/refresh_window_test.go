package validation

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOnlyTheAnnouncedDescriptorClosesTheRefreshWindow(t *testing.T) {
	path := filepath.Join(repositoryRoot(t), "fixtures", "semantic-invalid", "capability-refresh-requires-announced-revision.json")
	trace, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result := MustNew().ValidateBytes(trace, path)
	var indices []int
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code != CodeStaleCapabilityRevision || diagnostic.Phase != PhaseSemantic {
			t.Fatalf("unexpected diagnostic: %+v", diagnostic)
		}
		indices = append(indices, diagnostic.Index)
	}
	if want := []int{4, 5, 13, 14, 18, 19}; !reflect.DeepEqual(indices, want) {
		t.Fatalf("want stale responses and probes at %v; got %v: %+v", want, indices, result.Diagnostics)
	}
}
