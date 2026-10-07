package workwire

import (
	"context"
	"encoding/base64"
	"slices"
	"sync"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type pagedLister struct {
	*scripted
	mu    sync.Mutex
	rows  []base.NativeListing
	asked []base.NativeListRequest
}

func (p *pagedLister) NativeList(_ context.Context, request base.NativeListRequest) ([]base.NativeListing, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, request)
	return p.rows[:min(request.Limit, len(p.rows))], nil
}

func listHarness(t *testing.T) (*Front, *pagedLister) {
	t.Helper()
	store := binding.Memory()
	ctx := context.Background()
	for _, entry := range []binding.Entry{
		{Action: binding.ActionOpened, TimeMS: 75, Record: binding.Record{SessionID: "u1", Adapter: "scripted", Directory: "/work/a"}},
		{Action: binding.ActionOpened, TimeMS: 50, Record: binding.Record{SessionID: "u2", Adapter: "scripted", Directory: "/work/b"}},
	} {
		if err := store.Append(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	lister := &pagedLister{scripted: &scripted{transcript: map[string][]base.NativeTurn{}}, rows: []base.NativeListing{
		{NativeID: "n1", Directory: "/work/a", UpdatedAtMS: 90},
		{NativeID: "n2", Directory: "/work/a", UpdatedAtMS: 75},
		{NativeID: "n3", Directory: "/work/a", UpdatedAtMS: 60},
		{NativeID: "n4", Directory: "/work/b", UpdatedAtMS: 50},
		{NativeID: "n5", Directory: "/work/a", UpdatedAtMS: 30},
	}}
	registry := serve.NewRegistry()
	for name, adapter := range map[string]base.Adapter{"scripted": &scripted{transcript: map[string][]base.NativeTurn{}}, "native": lister} {
		if err := registry.Register(name, adapter); err != nil {
			t.Fatal(err)
		}
		registry.SetWorkingDirectory(name, "/work/a")
	}
	return New(serve.New(registry, serve.Options{Bindings: store})), lister
}

func listedIDs(t *testing.T, answer map[string]any) []string {
	t.Helper()
	var ids []string
	for _, group := range answer["groups"].([]any) {
		for _, work := range group.(map[string]any)["work"].([]any) {
			ref := work.(map[string]any)["ref"].(map[string]any)
			if id, ok := ref["native_id"].(string); ok {
				ids = append(ids, id)
			} else {
				ids = append(ids, ref["session_id"].(string))
			}
		}
	}
	return ids
}

func TestWorkListPagesNewestFirstWithTiesInKeyOrderAndAsksDeeperNativeLists(t *testing.T) {
	front, lister := listHarness(t)
	ctx := context.Background()
	limit := 2
	var pages [][]string
	cursor := ""
	for range 5 {
		answer := encoded(t)(front.List(ctx, ListRequest{IncludeNative: true, Limit: &limit, Cursor: cursor}))
		pages = append(pages, listedIDs(t, answer))
		next, more := answer["next_cursor"].(string)
		if !more {
			break
		}
		cursor = next
	}
	want := [][]string{{"n1", "n2"}, {"u1", "n3"}, {"n4", "u2"}, {"n5"}}
	if !slices.EqualFunc(pages, want, slices.Equal) {
		t.Fatalf("paged %v, want %v", pages, want)
	}
	var limits []int
	for _, asked := range lister.asked {
		limits = append(limits, asked.Limit)
	}
	if !slices.Equal(limits, []int{3, 5, 7, 9}) {
		t.Fatalf("asked the native list for %v rows", limits)
	}
}

func TestWorkListKeepsOnlyTheNamedDirectoryAndAdapters(t *testing.T) {
	front, lister := listHarness(t)
	ctx := context.Background()
	if got := listedIDs(t, encoded(t)(front.List(ctx, ListRequest{IncludeNative: true, Directory: "/work/b"}))); !slices.Equal(got, []string{"n4", "u2"}) {
		t.Fatalf("directory /work/b listed %v", got)
	}
	asked := len(lister.asked)
	if got := listedIDs(t, encoded(t)(front.List(ctx, ListRequest{IncludeNative: true, Adapters: []string{"scripted"}}))); !slices.Equal(got, []string{"u1", "u2"}) {
		t.Fatalf("adapters [scripted] listed %v", got)
	}
	if len(lister.asked) != asked {
		t.Fatal("an adapter left out of the filter was still asked for its native list")
	}
	if got := listedIDs(t, encoded(t)(front.List(ctx, ListRequest{IncludeNative: true, Adapters: []string{"native"}}))); !slices.Equal(got, []string{"n1", "n2", "n3", "n5", "n4"}) {
		t.Fatalf("adapters [native] listed %v", got)
	}
	empty := encoded(t)(front.List(ctx, ListRequest{Directory: "/work/none"}))["groups"].([]any)
	if len(empty) != 1 || empty[0].(map[string]any)["directory"] != "/work/none" || len(empty[0].(map[string]any)["work"].([]any)) != 0 {
		t.Fatalf("a named place with no work listed %v", empty)
	}
}

func TestWorkListRefusesALimitOutOfRangeAndACursorItDidNotIssue(t *testing.T) {
	front, _ := listHarness(t)
	ctx := context.Background()
	for _, request := range []ListRequest{
		{Limit: new(0)},
		{Limit: new(101)},
		{Cursor: "not-a-cursor"},
		{Cursor: base64.RawURLEncoding.EncodeToString([]byte(`{"at":1,"depth":1}`))},
	} {
		if _, refusal := front.List(ctx, request); refusal == nil || refusal.Code != "invalid_request" {
			t.Fatalf("%+v answered %+v", request, refusal)
		}
	}
	if _, refusal := ParseListRequest([]byte(`{"limit":"ten"}`)); refusal == nil || refusal.Code != "invalid_request" {
		t.Fatalf("a limit that is not a number answered %+v", refusal)
	}
}
