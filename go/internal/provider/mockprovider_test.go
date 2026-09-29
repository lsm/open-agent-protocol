package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestTheLoopbackIsWhatARunIsPointedAt(t *testing.T) {
	const loopback = "http://127.0.0.1:9"
	env := []providercatalog.EnvironmentValue{{Name: providercatalog.GlobalBaseURLEnv, Value: loopback}}
	base, ok := providercatalog.ResolveBaseURL(heldCatalogForTest(t), env, "openai", "openai-completions", "")
	if !ok || base != loopback {
		t.Fatalf("the base url = %q, want the loopback at %q: a static catalog row cannot point at a loopback", base, loopback)
	}
	model := Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", BaseURL: base, HasBaseURL: true, MaxTokens: 100, HasCompat: true}
	events := runStream(t, model, TextTurn("hello from the loopback")...)
	if countKind(events, EventDone) != 1 {
		t.Fatalf("got %v, want the loopback's turn to complete", kindsOf(events))
	}
	if findEvent(t, events, EventDone).Message.Content[0].Text.Text != "hello from the loopback" {
		t.Error("the terminal must carry what the loopback sent")
	}
}

func TestTheMockServesItsFramesToAnHTTPCaller(t *testing.T) {
	mock := NewMockProvider(TextTurn("over http")...)
	defer mock.Close()
	got := getFromLoopback(t, mock.URL())
	if !strings.Contains(got, `"content":"over http"`) {
		t.Errorf("the body = %q, want the text frame", got)
	}
	if !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Errorf("the body = %q, want the done sentinel last", got)
	}
}

func TestTheMockCanRefuseARequest(t *testing.T) {
	mock := NewMockProvider()
	defer mock.Close()
	mock.FailWith = 400
	got := getFromLoopback(t, mock.URL())
	if !strings.Contains(got, "mock failure") {
		t.Errorf("the body = %q, want the refusal", got)
	}
}

func TestTheMockRecordsWhatItWasSent(t *testing.T) {
	mock := NewMockProvider(TextTurn("x")...)
	defer mock.Close()
	getFromLoopback(t, mock.URL())
	if mock.RequestCount() != 1 {
		t.Fatalf("the mock saw %d requests, want 1", mock.RequestCount())
	}
	if mock.Paths[0] != "/v1/chat/completions" {
		t.Errorf("the path = %q, want the completions route", mock.Paths[0])
	}
}

func TestTwoRunsOfOneFixtureSendIdenticalBytes(t *testing.T) {
	first := NewMockProvider(TextTurn("same")...)
	defer first.Close()
	second := NewMockProvider(TextTurn("same")...)
	defer second.Close()
	model := Model{ID: "gpt-4o", API: "openai-completions", Provider: "openai", MaxTokens: 100, HasCompat: true}
	ctx := Context{Messages: []Message{{User: &UserContent{Text: "hi", HasText: true}}}}
	if string(BuildRequestBody(model, ctx, StreamOptions{})) != string(BuildRequestBody(model, ctx, StreamOptions{})) {
		t.Error("the same inputs must build the same bytes, or a diff cannot be attributed to the runtime")
	}
	if len(first.Frames) != len(second.Frames) {
		t.Error("two mocks built from the same fixture must hold the same frames")
	}
}

func getFromLoopback(t *testing.T, url string) string {
	t.Helper()
	resp, err := httpGet(url + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
