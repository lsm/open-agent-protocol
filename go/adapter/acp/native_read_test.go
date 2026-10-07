package acp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
)

type replayClient struct {
	listClient
	inbound chan rpc.InboundMessage
	replay  []string
	loaded  []native.SessionReopenParams
	fails   error
}

func (c *replayClient) Call(_ context.Context, method string, params, result any) error {
	if method != native.MethodSessionLoad {
		return errors.New("unexpected " + method)
	}
	c.loaded = append(c.loaded, params.(native.SessionReopenParams))
	for _, update := range c.replay {
		c.inbound <- rpc.InboundMessage{Notification: &rpc.NotificationMessage{Method: native.MethodSessionUpdate, Params: json.RawMessage(`{"sessionId":"s","update":` + update + `}`)}}
	}
	c.inbound <- rpc.InboundMessage{Notification: &rpc.NotificationMessage{Method: "session/other", Params: json.RawMessage(`{"sessionId":"s","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"not a turn"}}}`)}}
	ack := make(chan struct{})
	c.inbound <- rpc.InboundMessage{Barrier: ack}
	<-ack
	if c.fails != nil {
		return c.fails
	}
	return json.Unmarshal([]byte(`{}`), result)
}
func (c *replayClient) Inbound() <-chan rpc.InboundMessage { return c.inbound }

func replayingAdapter(t *testing.T, client *replayClient, capabilities string) *Adapter {
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

func TestANativeReadLoadsTheSessionAndReadsItsReplayAsEachUserMessageAndTheLastReplyBeforeTheNext(t *testing.T) {
	client := &replayClient{inbound: make(chan rpc.InboundMessage, 64), replay: []string{
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":" fix "}}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"it\n"}}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"hm"}}`,
		`{"sessionUpdate":"agent_message_chunk","messageId":"m1","content":{"type":"text","text":"Looking."}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"t","title":"Read","status":"completed"}`,
		`{"sessionUpdate":"agent_message_chunk","messageId":"m2","content":{"type":"text","text":"Fixed "}}`,
		`{"sessionUpdate":"agent_message_chunk","messageId":"m2","content":{"type":"text","text":"it."}}`,
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"thanks"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"You "}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"bet."}}`,
	}}
	read, err := replayingAdapter(t, client, `{"loadSession":true}`).NativeRead(context.Background(), base.NativeReadRequest{NativeID: "s", Directory: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	want := []base.NativeTurn{{Role: "user", Text: "fix it"}, {Role: "assistant", Text: "Fixed it."}, {Role: "user", Text: "thanks"}, {Role: "assistant", Text: "You bet."}}
	if len(read) != len(want) {
		t.Fatalf("read %+v", read)
	}
	for i := range want {
		if read[i] != want[i] {
			t.Fatalf("turn %d = %+v, want %+v", i, read[i], want[i])
		}
	}
	params, _ := json.Marshal(client.loaded)
	if string(params) != `[{"sessionId":"s","cwd":"/w","mcpServers":[]}]` || !client.closed {
		t.Fatalf("loaded %s, closed %v", params, client.closed)
	}
}

func TestANativeReadAsksNothingOfAnAgentThatCannotLoadAndFailsWhenTheLoadFails(t *testing.T) {
	for _, capabilities := range []string{`{}`, `{"loadSession":false}`, `{"sessionCapabilities":{"resume":{}}}`} {
		client := &replayClient{inbound: make(chan rpc.InboundMessage, 8)}
		read, err := replayingAdapter(t, client, capabilities).NativeRead(context.Background(), base.NativeReadRequest{NativeID: "s"})
		if err != nil || read != nil || len(client.loaded) != 0 {
			t.Fatalf("%s read %+v, loaded %d, %v", capabilities, read, len(client.loaded), err)
		}
	}
	failing := &replayClient{inbound: make(chan rpc.InboundMessage, 8), fails: errors.New("no such session")}
	if _, err := replayingAdapter(t, failing, `{"loadSession":true}`).NativeRead(context.Background(), base.NativeReadRequest{NativeID: "s"}); err == nil {
		t.Fatal("a failed load answered no error")
	}
	if loaded, _ := json.Marshal(failing.loaded); string(loaded) != `[{"sessionId":"s","cwd":"/work","mcpServers":[]}]` {
		t.Fatalf("an unscoped read loaded %s", loaded)
	}
}
