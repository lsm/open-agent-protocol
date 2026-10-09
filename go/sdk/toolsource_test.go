package sdk

import "testing"

func TestAProvidedToolNamesTheEndpointsNativeSourceElseItsFirst(t *testing.T) {
	native := map[string]any{"id": "oapx", "kind": "native"}
	process := map[string]any{"id": "files", "kind": "process"}
	for _, c := range []struct {
		sources []any
		want    string
	}{
		{[]any{process, native}, "oapx"},
		{[]any{process}, "files"},
		{[]any{map[string]any{"kind": "native"}, process}, "files"},
		{nil, ""},
	} {
		if got := declaredToolSource(c.sources); got != c.want {
			t.Fatalf("declaredToolSource(%v) = %q, want %q", c.sources, got, c.want)
		}
	}
}
