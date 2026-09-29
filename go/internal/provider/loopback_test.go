package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/provider/mockprovider"
	"github.com/lsm/open-agent-protocol/go/providercatalog"
)

func heldCatalogForTest(t *testing.T) providercatalog.Catalog {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "providers", "catalog.json"))
	if err != nil {
		t.Fatalf("the catalog is not readable: %v", err)
	}
	var catalog providercatalog.Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("the catalog is not json: %v", err)
	}
	return catalog
}

func loopbackReader(t *testing.T, url string, body []byte) ReadChunkFunc {
	t.Helper()
	var resp *http.Response
	return func() ([]byte, error) {
		if resp == nil {
			req, err := http.NewRequest("POST", url+"/v1/chat/completions", bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			req.Header.Set("content-type", "application/json")
			req.Header.Set("accept", "text/event-stream")
			resp, err = http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			if resp.StatusCode < 200 || resp.StatusCode > 299 {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				return nil, fmt.Errorf("the provider refused with %d: %s", resp.StatusCode, bytes.TrimSpace(body))
			}
		}
		chunk := make([]byte, 4096)
		n, err := resp.Body.Read(chunk)
		if n > 0 {
			return chunk[:n], nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return nil, nil
	}
}

func firstError(events []Event) string {
	for _, e := range events {
		if e.Kind == EventError {
			return e.Reason
		}
	}
	return ""
}

func TestARunIsPointedAtTheLoopbackAndTheLoopbackAnswersTheRun(t *testing.T) {
	mock := mockprovider.New(mockprovider.TextTurn("hello from the loopback")...)
	defer mock.Close()

	env := []providercatalog.EnvironmentValue{{Name: providercatalog.GlobalBaseURLEnv, Value: mock.URL()}}
	base, ok := providercatalog.ResolveBaseURL(heldCatalogForTest(t), env, "openai", "openai-completions", "")
	if !ok || base != mock.URL() {
		t.Fatalf("the base url = %q, want the loopback at %q", base, mock.URL())
	}

	model := Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", BaseURL: base, HasBaseURL: true, MaxTokens: 100, HasCompat: true}
	ctx := Context{Messages: []Message{{User: &UserContent{Text: "hi", HasText: true}}}}
	body := BuildRequestBody(model, ctx, StreamOptions{})

	sink := &EventSink{}
	Stream(sink, model, ctx, StreamOptions{}, loopbackReader(t, mock.URL(), body), nil)
	events := sink.take()
	if reason := firstError(events); reason != "" {
		t.Fatalf("the run errored: %s", reason)
	}

	if mock.RequestCount() != 1 {
		t.Fatalf("the loopback saw %d requests, want 1: nothing was actually sent", mock.RequestCount())
	}
	if mock.Paths[0] != "/v1/chat/completions" {
		t.Errorf("the path = %q, want the completions route the run asked for", mock.Paths[0])
	}
	if mock.LastBody() != string(body) {
		t.Errorf("the loopback was sent %q, want the body the run built", mock.LastBody())
	}
	done := findEvent(t, events, EventDone)
	if done.Message.Content[0].Text.Text != "hello from the loopback" {
		t.Errorf("the terminal text = %q, want what the loopback served over the wire", done.Message.Content[0].Text.Text)
	}
}

func TestTheLoopbackRefusalArrivesAsAnErrorAndNoTerminalIsReached(t *testing.T) {
	mock := mockprovider.New()
	defer mock.Close()
	mock.FailWith = http.StatusTooManyRequests
	mock.FailMessage = "rate limited"

	sink := &EventSink{}
	Stream(sink, Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", MaxTokens: 100, HasCompat: true}, Context{}, StreamOptions{},
		loopbackReader(t, mock.URL(), []byte(`{"model":"gpt-4o"}`)), nil)
	events := sink.take()

	if firstError(events) == "" {
		t.Fatalf("got %v, want the refusal to surface as an error", kindsOf(events))
	}
	if countKind(events, EventDone) != 0 {
		t.Errorf("got %v, want no terminal: a refused run must not look like a completed one", kindsOf(events))
	}
	if mock.RequestCount() != 1 {
		t.Fatalf("the loopback saw %d requests, want 1", mock.RequestCount())
	}
	if served, err := mockprovider.Get(mock.URL() + "/v1/chat/completions"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(served, "rate limited") {
		t.Errorf("the refusal body = %q, want the message it was given", served)
	}
}

func TestATextWithAQuoteInItReachesTheTerminal(t *testing.T) {
	const quoted = `he said "stop" and \ left`
	mock := mockprovider.New(mockprovider.TextTurn(quoted)...)
	defer mock.Close()
	sink := &EventSink{}
	Stream(sink, Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", MaxTokens: 100, HasCompat: true}, Context{}, StreamOptions{},
		loopbackReader(t, mock.URL(), []byte(`{"model":"gpt-4o"}`)), nil)
	events := sink.take()
	if got := findEvent(t, events, EventDone).Message.Content[0].Text.Text; got != quoted {
		t.Errorf("the terminal text = %q, want %q: a fixture that splices raw would not survive its own round trip", got, quoted)
	}
}

func TestAToolCallWithQuotesInItsArgumentsReachesTheTerminal(t *testing.T) {
	const args = `{"path":"a\"b","note":"line\nbreak"}`
	mock := mockprovider.New(mockprovider.ToolTurn("call_1", "read", args)...)
	defer mock.Close()
	sink := &EventSink{}
	Stream(sink, Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", MaxTokens: 100, HasCompat: true}, Context{}, StreamOptions{},
		loopbackReader(t, mock.URL(), []byte(`{"model":"gpt-4o"}`)), nil)
	events := sink.take()
	found := findEvent(t, events, EventToolCallEnd)
	if found.ToolCall.Arguments != args {
		t.Errorf("the arguments = %q, want %q", found.ToolCall.Arguments, args)
	}
}

func TestUsageTurnsCarryTheirCountsOverTheWire(t *testing.T) {
	mock := mockprovider.New(mockprovider.UsageTurn(11, 5)...)
	defer mock.Close()
	sink := &EventSink{}
	Stream(sink, Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", MaxTokens: 100, HasCompat: true}, Context{}, StreamOptions{},
		loopbackReader(t, mock.URL(), []byte(`{"model":"gpt-4o"}`)), nil)
	done := findEvent(t, sink.take(), EventDone)
	if done.Message.Usage.InputTokens != 11 || done.Message.Usage.OutputTokens != 5 {
		t.Errorf("the usage = %+v, want 11 input and 5 output", done.Message.Usage)
	}
}
