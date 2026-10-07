package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver/internal/rpc"
)

type askClient struct {
	answers map[string][]string
	asked   []string
	fails   error
	closes  int
}

func (c *askClient) Call(_ context.Context, method string, params, result any) error {
	encoded, _ := json.Marshal(params)
	c.asked = append(c.asked, method+" "+string(encoded))
	if c.fails != nil {
		return c.fails
	}
	queue := c.answers[method]
	if len(queue) == 0 {
		return fmt.Errorf("no answer for %s", method)
	}
	c.answers[method] = queue[1:]
	return json.Unmarshal([]byte(queue[0]), result)
}
func (c *askClient) Notify(context.Context, string, any) error { return nil }
func (c *askClient) Inbound() <-chan rpc.InboundMessage        { return nil }
func (c *askClient) Done() <-chan struct{}                     { return nil }
func (c *askClient) Err() error                                { return nil }
func (c *askClient) Close() error                              { c.closes++; return nil }

func askingAdapter(t *testing.T, client *askClient) *Adapter {
	t.Helper()
	implementation, err := New(Config{WorkingDirectory: "/work", Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil })})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func TestAThreadListAnswerBecomesNativeSessionsTitledByNameOrThePreviewsFirstLine(t *testing.T) {
	client := &askClient{answers: map[string][]string{"thread/list": {`{"data":[{"id":"t1","name":"named","preview":"ignored","cwd":"/w","updatedAt":1700000000,"status":{"type":"active"}},
{"id":"t2","name":null,"preview":"first line\nsecond","cwd":"/w","updatedAt":1700000001,"status":{"type":"notLoaded"}},
{"name":"no id"},7]}`}}}
	listed, err := askingAdapter(t, client).NativeList(context.Background(), adapter.NativeListRequest{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := []adapter.NativeListing{
		{NativeID: "t1", Title: "named", Directory: "/w", UpdatedAtMS: 1700000000000, Running: true, Link: "codex://threads/t1"},
		{NativeID: "t2", Title: "first line", Directory: "/w", UpdatedAtMS: 1700000001000, Link: "codex://threads/t2"},
	}
	if len(listed) != len(want) || listed[0] != want[0] || listed[1] != want[1] {
		t.Fatalf("listed %+v", listed)
	}
	if len(client.asked) != 1 || client.asked[0] != `thread/list {"cwd":"/work","limit":5}` || client.closes != 1 {
		t.Fatalf("asked %v, closed %d", client.asked, client.closes)
	}
	long := strings.Repeat("a", 119) + "é" + "b"
	if cut := titleCut(long, nativeTitleLimit); cut != 119 {
		t.Fatalf("titleCut = %d", cut)
	}
}

func TestAThreadTurnsPageBecomesTheUserMessageAndFinalReplyOfEachTurnFollowingTheCursor(t *testing.T) {
	client := &askClient{answers: map[string][]string{"thread/turns/list": {
		`{"data":[{"id":"u1","status":"completed","startedAt":1791311072,"completedAt":1791311075,"items":[
{"type":"userMessage","id":"i1","content":[{"type":"text","text":"Reply with one."},{"type":"image","url":"x"},{"type":"text","text":"Please."}]},
{"type":"reasoning","id":"i2"},
{"type":"agentMessage","id":"i3","phase":"commentary","text":"Thinking aloud."},
{"type":"agentMessage","id":"i4","phase":"final_answer","text":"one"}]}],"nextCursor":"c2"}`,
		`{"data":[{"id":"u2","status":"failed","startedAt":1791311080,"items":[{"type":"userMessage","id":"i5","content":[{"type":"text","text":"again"}]}]}],"nextCursor":null}`,
	}}}
	read, err := askingAdapter(t, client).NativeRead(context.Background(), adapter.NativeReadRequest{NativeID: "th", MaxTurns: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := []adapter.NativeTurn{{Role: "user", Text: "Reply with one.\nPlease.", AtMS: 1791311072000}, {Role: "assistant", Text: "one", AtMS: 1791311075000}, {Role: "user", Text: "again", AtMS: 1791311080000}}
	if len(read) != len(want) {
		t.Fatalf("read %+v", read)
	}
	for i := range want {
		if read[i] != want[i] {
			t.Fatalf("turn %d = %+v, want %+v", i, read[i], want[i])
		}
	}
	if len(client.asked) != 2 || client.asked[0] != `thread/turns/list {"itemsView":"full","limit":100,"sortDirection":"asc","threadId":"th"}` || client.asked[1] != `thread/turns/list {"cursor":"c2","itemsView":"full","limit":100,"sortDirection":"asc","threadId":"th"}` {
		t.Fatalf("asked %v", client.asked)
	}
	if client.closes != 1 {
		t.Fatalf("a two-page read started %d app-servers", client.closes)
	}
}

func TestANativeReadStopsOnceItHasTheTurnsAskedForAndFailsWithTheAppServer(t *testing.T) {
	page := `{"data":[{"startedAt":1,"items":[{"type":"userMessage","content":[{"type":"text","text":"x"}]}]}],"nextCursor":"more"}`
	client := &askClient{answers: map[string][]string{"thread/turns/list": {page, page, page, page}}}
	if read, _ := askingAdapter(t, client).NativeRead(context.Background(), adapter.NativeReadRequest{NativeID: "th", MaxTurns: 1}); len(read) != 1 || len(client.asked) != 1 {
		t.Fatalf("read %d turns over %d pages", len(read), len(client.asked))
	}
	failing := &askClient{fails: errors.New("gone")}
	if _, err := askingAdapter(t, failing).NativeList(context.Background(), adapter.NativeListRequest{Limit: 5}); err == nil {
		t.Fatal("a failed thread/list listed")
	}
	if _, err := askingAdapter(t, failing).NativeRead(context.Background(), adapter.NativeReadRequest{NativeID: "th", MaxTurns: 5}); err == nil {
		t.Fatal("a failed thread/turns/list read")
	}
}
