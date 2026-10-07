package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
)

func messagePage(next string, messages ...string) httpapi.MessagePage {
	page := httpapi.MessagePage{}
	page.Cursor.Next = next
	for _, message := range messages {
		page.Data = append(page.Data, json.RawMessage(message))
	}
	return page
}

func TestTheNativeReadFollowsMessagePagesAndKeepsEachUserMessageWithTheLastReplyBeforeTheNext(t *testing.T) {
	client := newFakeClient()
	client.pages = []httpapi.MessagePage{
		messagePage("c1",
			`{"id":"m0","time":{"created":1},"type":"model-switched","model":{"id":"x","providerID":"y"}}`,
			`{"id":"m1","time":{"created":10},"text":" first ask \n","type":"user"}`,
			`{"id":"m2","time":{"created":11},"type":"assistant","content":[{"type":"reasoning","text":"hm"},{"type":"text","text":"Let me look."}]}`,
			`{"id":"m3","time":{"created":12},"type":"assistant","content":[{"type":"reasoning","text":"thinking"},{"type":"tool","id":"t","name":"bash","state":{"status":"completed"},"time":{"created":12}},{"type":"text","text":"Found "},{"type":"text","text":"it."}]}`,
		),
		messagePage("c2",
			`{"id":"m4","time":{"created":13},"type":"idle","outcome":"succeeded"}`,
			`{"id":"m5","time":{"created":20},"text":"   ","type":"user"}`,
			`not json`,
			`{"id":"m6","time":{"created":21},"text":"thanks","type":"user"}`,
		),
		messagePage(""),
	}
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil })})
	if err != nil {
		t.Fatal(err)
	}
	turns, err := adapter.NativeRead(context.Background(), base.NativeReadRequest{NativeID: "ses_x"})
	if err != nil {
		t.Fatal(err)
	}
	want := []base.NativeTurn{{Role: "user", Text: "first ask", AtMS: 10}, {Role: "assistant", Text: "Found it.", AtMS: 12}, {Role: "user", Text: "thanks", AtMS: 21}}
	if len(turns) != len(want) {
		t.Fatalf("turns %+v", turns)
	}
	for i := range want {
		if turns[i] != want[i] {
			t.Fatalf("turn %d = %+v, want %+v", i, turns[i], want[i])
		}
	}
	if len(client.readAsked) != 3 || client.readAsked[0] != "ses_x||200" || client.readAsked[1] != "ses_x|c1|200" || client.readAsked[2] != "ses_x|c2|200" {
		t.Fatalf("asked %v", client.readAsked)
	}
	if !client.closed {
		t.Fatal("the read left its client open")
	}
}

func TestTheNativeReadStopsAtARepeatedCursorAndAtThePageCapAndFailsWithTheServer(t *testing.T) {
	repeating := newFakeClient()
	repeating.pages = []httpapi.MessagePage{messagePage("same", `{"type":"user","text":"a","time":{"created":1}}`), messagePage("same", `{"type":"user","text":"b","time":{"created":2}}`)}
	adapter, _ := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return repeating, nil })})
	if turns, _ := adapter.NativeRead(context.Background(), base.NativeReadRequest{NativeID: "ses_x"}); len(turns) != 2 || len(repeating.readAsked) != 2 {
		t.Fatalf("read %+v over %d pages", turns, len(repeating.readAsked))
	}
	endless := newFakeClient()
	for i := range nativeReadPagesMax + 4 {
		endless.pages = append(endless.pages, messagePage(string(rune('a'+i)), `{"type":"idle","time":{"created":1}}`))
	}
	adapter, _ = New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return endless, nil })})
	if _, err := adapter.NativeRead(context.Background(), base.NativeReadRequest{NativeID: "ses_x"}); err != nil || len(endless.readAsked) != nativeReadPagesMax {
		t.Fatalf("asked %d pages, %v", len(endless.readAsked), err)
	}
	failing := newFakeClient()
	failing.readErr = errors.New("down")
	adapter, _ = New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return failing, nil })})
	if _, err := adapter.NativeRead(context.Background(), base.NativeReadRequest{NativeID: "ses_x"}); err == nil {
		t.Fatal("a failing server read")
	}
}
