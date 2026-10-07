package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
)

func TestTheNativeListMapsEachRootSessionAndMarksTheOnesTheServerRuns(t *testing.T) {
	client := newFakeClient()
	busy := fakeSessionInfo(client.session, nil)
	busy.Title, busy.Time.Updated = "busy one", 9
	idle := fakeSessionInfo("ses_idle000000000000000", nil)
	idle.Location = json.RawMessage(`{"directory":"/elsewhere"}`)
	client.listed = []native.SessionInfo{busy, idle}
	client.activeFor = 1
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil })})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := adapter.NativeList(context.Background(), base.NativeListRequest{Directory: "/w", Limit: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.listAsked) != 1 || client.listAsked[0] != "/w||7" {
		t.Fatalf("asked %v", client.listAsked)
	}
	if len(listed) != 2 || listed[0] != (base.NativeListing{NativeID: string(client.session), Title: "busy one", Directory: "/w", UpdatedAtMS: 9, Running: true}) || listed[1].Running || listed[1].Directory != "/elsewhere" {
		t.Fatalf("listed %+v", listed)
	}
	if !client.closed {
		t.Fatal("the listing left its client open")
	}
}

func TestTheNativeListFailsWhenTheServerCannotListOrCannotSayWhatRuns(t *testing.T) {
	for _, broken := range []func(*fakeClient){
		func(c *fakeClient) { c.listErr = errors.New("down") },
		func(c *fakeClient) { c.activeErr = errors.New("down") },
	} {
		client := newFakeClient()
		broken(client)
		adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil })})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.NativeList(context.Background(), base.NativeListRequest{Limit: 5}); err == nil {
			t.Fatal("a failing server listed")
		}
	}
}

func TestTheNativeListGivesUpOnAServerThatStallsWithinTheRequestTimeout(t *testing.T) {
	client := newFakeClient()
	client.stalls = true
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), RequestTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := adapter.NativeList(context.Background(), base.NativeListRequest{Limit: 5})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a stalled list answered %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the native list waited past the request timeout")
	}
}

func TestTheNativeListFollowsTheServersCursorUntilItHasTheRowsAsked(t *testing.T) {
	client := newFakeClient()
	row := func(id string) native.SessionInfo { return fakeSessionInfo(native.SessionID(id), nil) }
	client.listPages = []httpapi.SessionPage{
		{Data: []native.SessionInfo{row("ses_a00000000000000000"), row("ses_b00000000000000000")}, Next: "c1"},
		{Data: []native.SessionInfo{row("ses_c00000000000000000"), row("ses_d00000000000000000")}, Next: "c2"},
		{Data: []native.SessionInfo{row("ses_e00000000000000000")}, Next: "c3"},
		{Data: []native.SessionInfo{row("ses_f00000000000000000")}},
	}
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil })})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := adapter.NativeList(context.Background(), base.NativeListRequest{Directory: "/w", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 5 || listed[4].NativeID != "ses_e00000000000000000" {
		t.Fatalf("listed %+v", listed)
	}
	if want := []string{"/w||5", "/w|c1|3", "/w|c2|1"}; !slices.Equal(client.listAsked, want) {
		t.Fatalf("asked %v, want %v", client.listAsked, want)
	}
	client.listAsked, client.listPages = nil, []httpapi.SessionPage{{Data: []native.SessionInfo{row("ses_a00000000000000000")}, Next: "c1"}, {Data: []native.SessionInfo{row("ses_b00000000000000000")}}, {Data: []native.SessionInfo{row("ses_c00000000000000000")}}}
	if listed, _ := adapter.NativeList(context.Background(), base.NativeListRequest{Directory: "/w", Limit: 5}); len(listed) != 2 || len(client.listAsked) != 2 {
		t.Fatalf("a last page with no next cursor listed %d after %v", len(listed), client.listAsked)
	}
}
