package acp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type reopenClient struct {
	*fakeClient
	method   string
	params   native.SessionReopenParams
	returned native.SessionNewResult
	loadErr  error
	history  int
}

func (c *reopenClient) Call(ctx context.Context, method string, params, result any) error {
	if method != native.MethodSessionLoad && method != native.MethodSessionResume {
		if method == native.MethodSessionSetConfigOption {
			ack := make(chan struct{})
			select {
			case c.inbound <- rpc.InboundMessage{Barrier: ack}:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-ack:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return c.fakeClient.Call(ctx, method, params, result)
	}
	c.method = method
	c.params = params.(native.SessionReopenParams)
	for i := 0; i < c.history; i++ {
		notification := rpc.NotificationMessage{Method: native.MethodSessionUpdate, Params: json.RawMessage(`{"sessionId":"bound-native","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"old history"}}}`)}
		select {
		case c.inbound <- rpc.InboundMessage{Notification: &notification}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ack := make(chan struct{})
	select {
	case c.inbound <- rpc.InboundMessage{Barrier: ack}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-ack:
	case <-ctx.Done():
		return ctx.Err()
	}
	if c.loadErr != nil {
		return c.loadErr
	}
	opened := result.(*native.SessionNewResult)
	opened.ConfigOptions = c.returned.ConfigOptions
	if c.returned.SessionID != "" {
		opened.SessionID = c.returned.SessionID
	}
	return nil
}

func reopenAdapter(t *testing.T, client Client, capabilities string) *Adapter {
	t.Helper()
	var advertised rpc.AgentCapabilities
	if err := json.Unmarshal([]byte(capabilities), &advertised); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{WorkingDirectory: "/workspace", Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		return client, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: advertised}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestACPReopenLoadsTheBindingAndReportsReturnedSettingsWithoutReplayingHistory(t *testing.T) {
	for _, scenario := range []struct{ name, capabilities, method string }{
		{"load-before-resume", `{"loadSession":true,"sessionCapabilities":{"resume":{}}}`, native.MethodSessionLoad},
		{"load-only", `{"loadSession":true}`, native.MethodSessionLoad},
		{"resume", `{"loadSession":false,"sessionCapabilities":{"resume":{}}}`, native.MethodSessionResume},
		{"resume-without-load-member", `{"sessionCapabilities":{"resume":{}}}`, native.MethodSessionResume},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client := &reopenClient{fakeClient: newFake(), history: 320, returned: native.SessionNewResult{ConfigOptions: []native.ConfigOption{
				{ID: "model", Category: "model", CurrentValue: "resumed-model"},
				{ID: "effort", Category: native.CategoryThoughtLevel, CurrentValue: "deep", Options: json.RawMessage(`[{"value":"deep","name":"High"}]`)},
			}}}
			a := reopenAdapter(t, client, scenario.capabilities)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			opened, err := a.Open(ctx, base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: "bound-native"})
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close(context.Background())
			if client.method != scenario.method || client.params.SessionID != "bound-native" || client.params.Cwd != "/workspace" || client.params.MCPServers == nil {
				t.Fatalf("reload=%s %+v", client.method, client.params)
			}
			state, err := opened.State(ctx)
			if err != nil || state.Recovery == nil || !state.Recovery.Recovered || state.Recovery.Reason != reopenReason || state.CurrentModelID != "resumed-model" || state.ReasoningLevel != protocol.ReasoningHigh || state.Status != protocol.SessionIdle || state.ActiveRunID != "" {
				t.Fatalf("state=%+v err=%v", state, err)
			}
			s := opened.(*session)
			if s.NativeSessionID() != "bound-native" || len(s.runs) != 0 || len(s.journal) != 0 {
				t.Fatal("reload changed identity or invented OAP history")
			}
		})
	}
}

func TestACPReopenRefusesUnavailableOrUnsatisfiableBindings(t *testing.T) {
	for _, capabilities := range []string{`{}`, `{"loadSession":false}`, `{"sessionCapabilities":{"resume":null}}`, `{"sessionCapabilities":{"resume":true}}`, `{"loadSession":"true"}`} {
		client := &reopenClient{fakeClient: newFake()}
		_, err := reopenAdapter(t, client, capabilities).Open(context.Background(), base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: "bound-native"})
		assertReopenRefusal(t, err)
		if client.method != "" || !client.closed {
			t.Fatal("unadvertised reload wrote a method or left its client open")
		}
	}
	for _, scenario := range []struct {
		failure  error
		returned string
	}{{errors.New("session not found"), ""}, {nil, "another-session"}} {
		client := &reopenClient{fakeClient: newFake(), loadErr: scenario.failure, returned: native.SessionNewResult{SessionID: scenario.returned}}
		_, err := reopenAdapter(t, client, `{"loadSession":true}`).Open(context.Background(), base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: "bound-native"})
		assertReopenRefusal(t, err)
		if !client.closed {
			t.Fatal("refused reload left its client open")
		}
	}
	a := reopenAdapter(t, newFake(), `{"loadSession":true}`)
	_, err := a.Open(context.Background(), base.OpenRequest{Participant: protocol.Participant{ID: "user"}, Reopen: true})
	assertReopenRefusal(t, err)
}

func assertReopenRefusal(t *testing.T, err error) {
	t.Helper()
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("refusal=%v", err)
	}
}

func TestACPCreateReportsTheNativeBindingAndUnknownSettingsStayUnspecified(t *testing.T) {
	a := reopenAdapter(t, newFake(), `{}`)
	opened, err := a.Open(context.Background(), base.OpenRequest{Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close(context.Background())
	if opened.(base.NativeSession).NativeSessionID() != "native-session" {
		t.Fatal("create has no native binding")
	}
	model, level := resumedSettings([]native.ConfigOption{{Category: "model", CurrentValue: "default"}, {Category: native.CategoryThoughtLevel, CurrentValue: "custom"}})
	if model != "" || level != "" {
		t.Fatal("opaque defaults were reported as known settings")
	}
}

func TestACPReloadRequiresAnObjectResponseAndPreservesTheBoundID(t *testing.T) {
	for _, invalid := range []string{"null", "[]", "true", "\"session\""} {
		var result native.SessionNewResult
		if json.Unmarshal([]byte(invalid), &result) == nil {
			t.Fatalf("invalid load response accepted: %s", invalid)
		}
	}
	result := native.SessionNewResult{SessionID: "bound-native"}
	if err := json.Unmarshal([]byte(`{"configOptions":[]}`), &result); err != nil || result.SessionID != "bound-native" {
		t.Fatalf("load response=%+v err=%v", result, err)
	}
}

func TestACPReopenConfirmsARequestedReasoningSettingBeforeReturning(t *testing.T) {
	thought := native.ConfigOption{ID: "effort", Category: native.CategoryThoughtLevel, CurrentValue: "medium", Options: json.RawMessage(`[{"value":"medium","name":"Medium"},{"value":"deep","name":"High"}]`)}
	client := &reopenClient{fakeClient: newFake(), returned: native.SessionNewResult{ConfigOptions: []native.ConfigOption{thought}}}
	client.options = []native.ConfigOption{thought}
	client.confirm = true
	implementation := reopenAdapter(t, client, `{"loadSession":true}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	opened, err := implementation.Open(ctx, base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: "bound-native", ReasoningLevel: protocol.ReasoningHigh})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close(context.Background())
	state, err := opened.State(ctx)
	if err != nil || state.ReasoningLevel != protocol.ReasoningHigh || client.setOption.SessionID != "bound-native" || client.setOption.Value != "deep" {
		t.Fatalf("setting=%+v state=%+v err=%v", client.setOption, state, err)
	}
}
