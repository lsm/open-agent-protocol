package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver/internal/rpc"
)

type askClient struct {
	answers  map[string][]string
	asked    []string
	fails    error
	failOnce error
	closes   int
	done     chan struct{}
	inbound  chan rpc.InboundMessage
}

func (c *askClient) Call(ctx context.Context, method string, params, result any) error {
	encoded, _ := json.Marshal(params)
	c.asked = append(c.asked, method+" "+string(encoded))
	if err := ctx.Err(); err != nil {
		if c.done != nil {
			select {
			case <-c.done:
			default:
				close(c.done)
			}
		}
		return err
	}
	if c.fails != nil {
		return c.fails
	}
	if failure := c.failOnce; failure != nil {
		c.failOnce = nil
		return failure
	}
	queue := c.answers[method]
	if len(queue) == 0 {
		return fmt.Errorf("no answer for %s", method)
	}
	c.answers[method] = queue[1:]
	return json.Unmarshal([]byte(queue[0]), result)
}
func (c *askClient) Notify(context.Context, string, any) error { return nil }
func (c *askClient) Inbound() <-chan rpc.InboundMessage        { return c.inbound }
func (c *askClient) Done() <-chan struct{}                     { return c.done }
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
	if len(client.asked) != 1 || client.asked[0] != `thread/list {"cwd":"/work","limit":5}` || client.closes != 0 {
		t.Fatalf("asked %v, closed %d", client.asked, client.closes)
	}
	long := strings.Repeat("a", 119) + "é" + "b"
	if cut := titleCut(long, nativeTitleLimit); cut != 119 {
		t.Fatalf("titleCut = %d", cut)
	}
}

func TestANativeSearchAsksThreadListForTheTermInTheDirectory(t *testing.T) {
	client := &askClient{answers: map[string][]string{"thread/list": {`{"data":[{"id":"t1","name":"apple pie","cwd":"/elsewhere","updatedAt":1700000000,"status":{"type":"idle"}}]}`}}}
	listed, err := askingAdapter(t, client).NativeSearch(context.Background(), adapter.NativeSearchRequest{Directory: "/elsewhere", Limit: 3, Term: "apple"})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.asked) != 1 || client.asked[0] != `thread/list {"cwd":"/elsewhere","limit":3,"searchTerm":"apple"}` {
		t.Fatalf("asked %v", client.asked)
	}
	if len(listed) != 1 || listed[0].NativeID != "t1" || listed[0].Title != "apple pie" {
		t.Fatalf("listed %+v", listed)
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
	if client.closes != 0 {
		t.Fatalf("a two-page read closed its app-server %d times", client.closes)
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

type startingFactory struct {
	clients []*askClient
	starts  int
}

func (f *startingFactory) Start(context.Context) (Client, error) {
	client := f.clients[min(f.starts, len(f.clients)-1)]
	f.starts++
	return client, nil
}

func listAnswers(count int) map[string][]string {
	answers := make([]string, count)
	for i := range answers {
		answers[i] = `{"data":[{"id":"t1"}]}`
	}
	return map[string][]string{"thread/list": answers}
}

func TestTheNativeCallsShareOneAppServerAndStartAnotherOnlyWhenItBreaks(t *testing.T) {
	kept := &askClient{answers: listAnswers(4), done: make(chan struct{})}
	fresh := &askClient{answers: listAnswers(4), done: make(chan struct{})}
	factory := &startingFactory{clients: []*askClient{kept, fresh}}
	implementation, err := New(Config{WorkingDirectory: "/work", Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	list := func() error {
		_, err := implementation.NativeList(context.Background(), adapter.NativeListRequest{Limit: 1})
		return err
	}
	if list() != nil || list() != nil || factory.starts != 1 || kept.closes != 0 {
		t.Fatalf("two lists started %d app-servers and closed %d", factory.starts, kept.closes)
	}
	kept.failOnce = &rpc.RemoteError{Object: rpc.ErrorObject{Code: -32600, Message: "refused"}}
	if list() == nil || factory.starts != 1 || kept.closes != 0 {
		t.Fatalf("a refused call dropped the app-server: %d starts, %d closes", factory.starts, kept.closes)
	}
	kept.failOnce = errors.New("pipe closed")
	if err := list(); err != nil || factory.starts != 2 || kept.closes != 1 || len(fresh.asked) != 1 {
		t.Fatalf("a broken reused app-server answered %v after %d starts, %d closes", err, factory.starts, kept.closes)
	}
	close(fresh.done)
	third := &askClient{answers: listAnswers(1), done: make(chan struct{})}
	factory.clients = append(factory.clients, third)
	if err := list(); err != nil || factory.starts != 3 || fresh.closes != 1 || len(third.asked) != 1 {
		t.Fatalf("an app-server that had exited was used again: %v, %d starts", err, factory.starts)
	}
	third.failOnce = errors.New("pipe closed")
	broken := &askClient{answers: listAnswers(1), done: make(chan struct{}), failOnce: errors.New("never answered")}
	factory.clients = append(factory.clients, broken)
	if list() == nil || factory.starts != 4 || third.closes != 1 || broken.closes != 1 {
		t.Fatalf("a reused app-server that broke was retried more than once: %d starts", factory.starts)
	}
	spare := &askClient{answers: listAnswers(1), done: make(chan struct{})}
	factory.clients = append(factory.clients, spare)
	implementation.native.mu.Lock()
	err = list()
	implementation.native.mu.Unlock()
	if err != nil || factory.starts != 5 || spare.closes != 1 {
		t.Fatalf("a call while the kept app-server was busy did not run on its own app-server: %v, %d starts, %d closes", err, factory.starts, spare.closes)
	}
	starting := &askClient{answers: listAnswers(1), done: make(chan struct{}), failOnce: errors.New("died at once")}
	factory.clients = append(factory.clients, starting, &askClient{answers: listAnswers(1), done: make(chan struct{})})
	if list() == nil || factory.starts != 6 {
		t.Fatalf("an app-server that broke on its first call was retried: %d starts", factory.starts)
	}
}

func TestTheKeptAppServerHasItsNotificationsDrainedSoItsQueueNeverFills(t *testing.T) {
	inbound := make(chan rpc.InboundMessage, 2)
	inbound <- rpc.InboundMessage{}
	inbound <- rpc.InboundMessage{}
	client := &askClient{answers: listAnswers(1), done: make(chan struct{}), inbound: inbound}
	if _, err := askingAdapter(t, client).NativeList(context.Background(), adapter.NativeListRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(inbound) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(inbound) != 0 {
		t.Fatalf("%d notifications were left queued", len(inbound))
	}
	close(client.done)
}

func TestACallerThatGivesUpLeavesTheKeptAppServerRunning(t *testing.T) {
	kept := &askClient{answers: listAnswers(3), done: make(chan struct{})}
	factory := &startingFactory{clients: []*askClient{kept}}
	implementation, err := New(Config{WorkingDirectory: "/work", Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := implementation.NativeList(context.Background(), adapter.NativeListRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = implementation.NativeList(gone, adapter.NativeListRequest{Limit: 1})
	if ended(kept) || kept.closes != 0 {
		t.Fatal("a caller that had given up shut the kept app-server down")
	}
	if _, err := implementation.NativeList(context.Background(), adapter.NativeListRequest{Limit: 1}); err != nil || factory.starts != 1 {
		t.Fatalf("the list after a cancelled one: %v, %d starts", err, factory.starts)
	}
	expiring, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := implementation.NativeList(expiring, adapter.NativeListRequest{Limit: 1}); err == nil || !ended(kept) {
		t.Fatalf("a call past its caller's deadline answered %v without timing out", err)
	}
}

func TestAKeptAppServerThatNeverInitializesGivesUpAtTheCallsDeadline(t *testing.T) {
	implementation, err := New(Config{WorkingDirectory: "/work", Factory: ClientFactoryFunc(func(ctx context.Context) (Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})})
	if err != nil {
		t.Fatal(err)
	}
	answered := make(chan error, 2)
	go func() {
		for range 2 {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			_, err := implementation.NativeList(ctx, adapter.NativeListRequest{Limit: 1})
			cancel()
			answered <- err
		}
	}()
	for range 2 {
		select {
		case err := <-answered:
			if err == nil {
				t.Fatal("an app-server that never initialized listed")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a start that never initialized held the kept app-server past the call's deadline")
		}
	}
}
