package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
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
	if len(client.listAsked) != 1 || client.listAsked[0] != "/w|7" {
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
