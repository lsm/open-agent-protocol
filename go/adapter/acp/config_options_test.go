package acp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type configClient struct {
	*fakeClient
	calls []native.SetConfigOptionParams
	hook  func(*configClient, native.SetConfigOptionParams) error
}

func (c *configClient) Call(ctx context.Context, method string, params, result any) error {
	if method != native.MethodSessionSetConfigOption {
		return c.fakeClient.Call(ctx, method, params, result)
	}
	p := params.(native.SetConfigOptionParams)
	c.calls = append(c.calls, p)
	for i := range c.options {
		if c.options[i].ID == p.ConfigID {
			c.options[i].CurrentValue = p.Value
		}
	}
	if c.hook != nil {
		if err := c.hook(c, p); err != nil {
			return err
		}
	}
	*result.(*native.SetConfigOptionResult) = native.SetConfigOptionResult{ConfigOptions: append([]native.ConfigOption(nil), c.options...)}
	return nil
}

func configuredClient() *configClient {
	c := &configClient{fakeClient: newFake()}
	c.options = []native.ConfigOption{
		{ID: "mode", Category: "mode", CurrentValue: "accept-edits", Options: json.RawMessage(`[{"value":"ask","name":"Ask"},{"value":"accept-edits","name":"Code"}]`)},
		{ID: "model", Category: "model", CurrentValue: "default", Options: json.RawMessage(`[{"group":"models","name":"Models","options":[{"value":"swe-2-high","name":"SWE"}]}]`)},
		{ID: "effort", Category: native.CategoryThoughtLevel, CurrentValue: "medium", Options: json.RawMessage(`[{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]`)},
	}
	return c
}

func configuredAdapter(t *testing.T, c *configClient, wanted map[string]string) *Adapter {
	t.Helper()
	a, err := New(Config{WorkingDirectory: "/workspace", SessionConfig: wanted, Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		return c, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestConfiguredOptionsAreConfirmedInOrderBeforeOpenAndProjected(t *testing.T) {
	c := configuredClient()
	wanted := map[string]string{"model": "swe-2-high", "mode": "ask"}
	a := configuredAdapter(t, c, wanted)
	wanted["mode"] = "accept-edits"
	s, err := a.Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	got := []native.SetConfigOptionParams{{SessionID: "native-session", ConfigID: "mode", Value: "ask"}, {SessionID: "native-session", ConfigID: "model", Value: "swe-2-high"}}
	if !reflect.DeepEqual(c.calls, got) {
		t.Fatalf("calls = %+v, want %+v", c.calls, got)
	}
	state, err := s.State(context.Background())
	if err != nil || state.CurrentModelID != "swe-2-high" || state.ReasoningLevel != protocol.ReasoningMedium {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

func TestFreshOpenWithoutConfiguredOptionsPreservesUnspecifiedDefaults(t *testing.T) {
	c := configuredClient()
	s, err := configuredAdapter(t, c, nil).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	state, err := s.State(context.Background())
	if err != nil || state.CurrentModelID != "" || state.ReasoningLevel != "" || len(c.calls) != 0 {
		t.Fatalf("default state=%+v calls=%v err=%v", state, c.calls, err)
	}
}

func TestUnfulfillableConfigurationClosesTheClientBeforeAnyPrompt(t *testing.T) {
	cases := []struct {
		name   string
		wanted map[string]string
		hook   func(*configClient, native.SetConfigOptionParams) error
	}{
		{name: "missing option", wanted: map[string]string{"missing": "x"}},
		{name: "bad value", wanted: map[string]string{"model": "UNKNOWN"}},
		{name: "empty id", wanted: map[string]string{"": "ask"}},
		{name: "empty value", wanted: map[string]string{"model": ""}},
		{name: "rejected", wanted: map[string]string{"mode": "ask"}, hook: func(*configClient, native.SetConfigOptionParams) error { return errors.New("refused") }},
		{name: "unconfirmed", wanted: map[string]string{"mode": "ask"}, hook: func(c *configClient, _ native.SetConfigOptionParams) error {
			c.options[0].CurrentValue = "accept-edits"
			return nil
		}},
		{name: "dependent reset", wanted: map[string]string{"mode": "ask", "model": "swe-2-high"}, hook: func(c *configClient, p native.SetConfigOptionParams) error {
			if p.ConfigID == "model" {
				c.options[0].CurrentValue = "accept-edits"
			}
			return nil
		}},
		{name: "removed after setting", wanted: map[string]string{"mode": "ask"}, hook: func(c *configClient, _ native.SetConfigOptionParams) error { c.options = nil; return nil }},
		{name: "ambiguous confirmation", wanted: map[string]string{"mode": "ask"}, hook: func(c *configClient, _ native.SetConfigOptionParams) error {
			duplicate := c.options[0]
			duplicate.CurrentValue = "accept-edits"
			c.options = append(c.options, duplicate)
			return nil
		}},
		{name: "wrong confirmed type", wanted: map[string]string{"mode": "ask"}, hook: func(c *configClient, _ native.SetConfigOptionParams) error { c.options[0].Type = "boolean"; return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := configuredClient()
			c.hook = tc.hook
			s, err := configuredAdapter(t, c, tc.wanted).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
			if err == nil || s != nil || !c.closed || !strings.Contains(err.Error(), "ACP session config") {
				t.Fatalf("open = %v, %v, closed = %v", s, err, c.closed)
			}
			select {
			case <-c.promptStarted:
				t.Fatal("prompt submitted before configuration confirmation")
			default:
			}
		})
	}
}

func TestUnusableAdvertisementsAreRefusedWithoutSetting(t *testing.T) {
	for _, kind := range []string{"duplicate", "boolean"} {
		t.Run(kind, func(t *testing.T) {
			c := configuredClient()
			if kind == "duplicate" {
				c.options = append(c.options, c.options[0])
			} else {
				c.options[0].Type = "boolean"
			}
			_, err := configuredAdapter(t, c, map[string]string{"mode": "ask"}).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
			if err == nil || !c.closed || len(c.calls) != 0 {
				t.Fatalf("open = %v, closed=%v calls=%v", err, c.closed, c.calls)
			}
		})
	}
}

func TestFreshSettingsDrainNotificationsBeforeDispatchStarts(t *testing.T) {
	c := configuredClient()
	c.inbound = make(chan rpc.InboundMessage, 1)
	c.hook = func(c *configClient, _ native.SetConfigOptionParams) error {
		drained := make(chan struct{})
		c.inbound <- rpc.InboundMessage{Barrier: drained}
		select {
		case <-drained:
			return nil
		case <-time.After(3 * time.Second):
			return errors.New("opening did not drain notification")
		}
	}
	s, err := configuredAdapter(t, c, map[string]string{"mode": "ask", "model": "swe-2-high"}).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Close(context.Background())
}

func TestReopenedConfigurationMustBeConfirmedAndDrainsBeforeDispatch(t *testing.T) {
	for _, confirm := range []bool{true, false} {
		f := newFake()
		f.options = configuredClient().options[:1]
		f.confirm = confirm
		c := &reopenClient{fakeClient: f, returned: native.SessionNewResult{ConfigOptions: f.options}, history: 3}
		a := reopenAdapter(t, c, `{"loadSession":true}`)
		a.config.SessionConfig = map[string]string{"mode": "ask"}
		s, err := a.Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}, Reopen: true, NativeSessionID: "bound-native"})
		if confirm {
			if err != nil {
				t.Fatal(err)
			}
			s.Close(context.Background())
		} else if err == nil || s != nil || !f.closed {
			t.Fatalf("unconfirmed reopen = %v, %v, closed=%v", s, err, f.closed)
		}
	}
}

func TestReasoningChangesCannotUndoRequiredConfiguration(t *testing.T) {
	c := configuredClient()
	c.hook = func(c *configClient, p native.SetConfigOptionParams) error {
		if p.ConfigID == "effort" {
			c.options[0].CurrentValue = "accept-edits"
		}
		return nil
	}
	s, err := configuredAdapter(t, c, map[string]string{"mode": "ask"}).Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "reviewbot"}, ReasoningLevel: protocol.ReasoningHigh})
	if err == nil || s != nil || !c.closed {
		t.Fatalf("open = %v, %v, closed = %v", s, err, c.closed)
	}
}
