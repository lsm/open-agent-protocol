package serve

import (
	"context"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
)

type askedLister struct {
	base.Adapter
	asked []base.NativeListRequest
}

func (l *askedLister) NativeList(_ context.Context, request base.NativeListRequest) ([]base.NativeListing, error) {
	l.asked = append(l.asked, request)
	return nil, nil
}

func TestNativesForAsksOnlyTheNamedAdaptersAndAnAnyDirectoryOneForTheNamedPlace(t *testing.T) {
	registry := NewRegistry()
	listers := map[string]*askedLister{}
	for _, name := range []string{"placed", "fixed", "skipped"} {
		listers[name] = &askedLister{Adapter: base.NewMemory(base.Config{})}
		if err := registry.Register(name, listers[name]); err != nil {
			t.Fatal(err)
		}
		registry.SetWorkingDirectory(name, "/"+name)
	}
	registry.templates = map[string]template{"placed": {}}
	hub := New(registry, Options{})
	hub.NativesFor(context.Background(), nil, NativeQuery{Adapters: []string{"placed", "fixed"}, Directory: "/elsewhere", Limit: 7})
	if got := listers["placed"].asked; len(got) != 1 || got[0].Directory != "/elsewhere" || got[0].Limit != 7 {
		t.Fatalf("the any-directory adapter was asked %+v", got)
	}
	if got := listers["fixed"].asked; len(got) != 1 || got[0].Directory != "/fixed" {
		t.Fatalf("the fixed adapter was asked %+v", got)
	}
	if got := listers["skipped"].asked; len(got) != 0 {
		t.Fatalf("an adapter left out was asked %+v", got)
	}
	hub.NativesFor(context.Background(), nil, NativeQuery{})
	if got := listers["skipped"].asked; len(got) != 1 || got[0].Limit != NativeListLimit || got[0].Directory != "/skipped" {
		t.Fatalf("an unfiltered query asked %+v", got)
	}
}
