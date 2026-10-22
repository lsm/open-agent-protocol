package validation

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOnlyTheAnnouncedDescriptorClosesTheRefreshWindow(t *testing.T) {
	for _, test := range []struct {
		name    string
		indices []int
	}{
		{"capability-refresh-requires-announced-revision", []int{4, 5, 13, 14, 18, 19}},
		{"capability-refresh-late-response-after-refresh", []int{6}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(repositoryRoot(t), "fixtures", "semantic-invalid", test.name+".json")
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
			if !reflect.DeepEqual(indices, test.indices) {
				t.Fatalf("want stale at %v; got %v: %+v", test.indices, indices, result.Diagnostics)
			}
		})
	}
}
