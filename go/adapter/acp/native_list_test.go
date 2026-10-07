package acp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
)

type listClient struct {
	pages  []string
	asked  []sessionListParams
	fails  error
	closed bool
}

func (c *listClient) Call(_ context.Context, method string, params, result any) error {
	if method != "session/list" {
		return errors.New("unexpected " + method)
	}
	if c.fails != nil {
		return c.fails
	}
	c.asked = append(c.asked, params.(sessionListParams))
	page := `{"sessions":[]}`
	if len(c.asked) <= len(c.pages) {
		page = c.pages[len(c.asked)-1]
	}
	return json.Unmarshal([]byte(page), result)
}
func (c *listClient) CallStarted(ctx context.Context, m string, p, r any, started chan<- error) error {
	close(started)
	return c.Call(ctx, m, p, r)
}
func (c *listClient) Notify(context.Context, string, any) error     { return nil }
func (c *listClient) Requests() <-chan *rpc.IncomingRequest         { return nil }
func (c *listClient) Notifications() <-chan rpc.NotificationMessage { return nil }
func (c *listClient) Inbound() <-chan rpc.InboundMessage            { return nil }
func (c *listClient) Done() <-chan struct{}                         { return nil }
func (c *listClient) Err() error                                    { return nil }
func (c *listClient) Close() error                                  { c.closed = true; return nil }

func listingAdapter(t *testing.T, client *listClient, capabilities string) *Adapter {
	t.Helper()
	var offered rpc.AgentCapabilities
	if err := json.Unmarshal([]byte(capabilities), &offered); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{WorkingDirectory: "/work", Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		return client, rpc.InitializeResponse{AgentCapabilities: offered}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestTheNativeListFollowsSessionListPagesForTheDirectoryUpToTheLimitSkippingRowsWithoutAnID(t *testing.T) {
	client := &listClient{pages: []string{
		`{"sessions":[{"sessionId":"a","title":"first","cwd":"/work","updatedAt":"2026-10-06T10:00:00.5+02:00"},7,{"title":"no id"}],"nextCursor":"c1"}`,
		`{"sessions":[{"sessionId":"b","title":3},{"sessionId":"c"}],"nextCursor":"c2"}`,
	}}
	adapter := listingAdapter(t, client, `{"sessionCapabilities":{"list":{}}}`)
	listed, err := adapter.NativeList(context.Background(), base.NativeListRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].NativeID != "a" || listed[0].Title != "first" || listed[0].Directory != "/work" || listed[0].UpdatedAtMS != 1791273600500 || listed[1].NativeID != "b" || listed[1].Title != "" {
		t.Fatalf("listed %+v", listed)
	}
	if len(client.asked) != 2 || client.asked[0] != (sessionListParams{Cwd: "/work"}) || client.asked[1] != (sessionListParams{Cwd: "/work", Cursor: "c1"}) {
		t.Fatalf("asked %+v", client.asked)
	}
	if !client.closed {
		t.Fatal("the listing left its agent running")
	}
}

func TestTheNativeListStopsAtARepeatedCursorAndAtThePageCap(t *testing.T) {
	repeating := &listClient{pages: []string{`{"sessions":[{"sessionId":"a"}],"nextCursor":"same"}`, `{"sessions":[{"sessionId":"b"}],"nextCursor":"same"}`}}
	if listed, _ := listingAdapter(t, repeating, `{"sessionCapabilities":{"list":{}}}`).NativeList(context.Background(), base.NativeListRequest{Directory: "/other", Limit: 50}); len(listed) != 2 || len(repeating.asked) != 2 || repeating.asked[0].Cwd != "/other" {
		t.Fatalf("listed %+v after %d pages", listed, len(repeating.asked))
	}
	var endless []string
	for range nativeListPagesMax + 4 {
		endless = append(endless, `{"sessions":[],"nextCursor":"`+string(rune('a'+len(endless)))+`"}`)
	}
	capped := &listClient{pages: endless}
	if _, err := listingAdapter(t, capped, `{"sessionCapabilities":{"list":{}}}`).NativeList(context.Background(), base.NativeListRequest{Limit: 50}); err != nil || len(capped.asked) != nativeListPagesMax {
		t.Fatalf("asked %d pages, %v", len(capped.asked), err)
	}
}

func TestTheNativeListAsksNothingOfAnAgentThatDoesNotOfferItAndFailsWhenTheListFails(t *testing.T) {
	for _, capabilities := range []string{`{}`, `{"sessionCapabilities":{}}`, `{"sessionCapabilities":{"list":null}}`} {
		client := &listClient{pages: []string{`{"sessions":[{"sessionId":"a"}]}`}}
		listed, err := listingAdapter(t, client, capabilities).NativeList(context.Background(), base.NativeListRequest{Limit: 5})
		if err != nil || len(listed) != 0 || len(client.asked) != 0 {
			t.Fatalf("%s listed %+v, asked %d, %v", capabilities, listed, len(client.asked), err)
		}
	}
	failing := &listClient{fails: errors.New("agent gone")}
	if _, err := listingAdapter(t, failing, `{"sessionCapabilities":{"list":{}}}`).NativeList(context.Background(), base.NativeListRequest{Limit: 5}); err == nil {
		t.Fatal("a failed session/list answered no error")
	}
}
