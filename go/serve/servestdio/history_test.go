package servestdio

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/serve"
	"github.com/lsm/open-agent-protocol/go/serve/servehttp"
)

func historyHub(t *testing.T, store binding.Store) *serve.Hub {
	t.Helper()
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})); err != nil {
		t.Fatal(err)
	}
	return serve.New(registry, serve.Options{StreamQueue: 64, Bindings: store})
}

func servedTwice(t *testing.T, hub *serve.Hub) (func(path string) (int, string), func(id int64, line string) responseLine) {
	t.Helper()
	server, err := servehttp.New(hub, servehttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	httpFrontend := httptest.NewServer(server.Handler())
	t.Cleanup(httpFrontend.Close)
	f := startFrontend(t, hub, Options{})
	t.Cleanup(func() { _ = f.finish() })
	get := func(path string) (int, string) {
		t.Helper()
		response, err := http.Get(httpFrontend.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body)
	}
	stdio := func(id int64, line string) responseLine {
		t.Helper()
		f.send(line)
		return f.expectResponse(id)
	}
	return get, stdio
}

func httpCode(t *testing.T, body string) string {
	t.Helper()
	var envelope struct {
		Payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("error body %s: %v", body, err)
	}
	return envelope.Payload.Error.Code
}

func TestSessionHistoryMatchesHTTPPageByPage(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "sessions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := historyHub(t, store)
	for _, id := range []string{"history-a", "history-b", "history-c"} {
		openSession(t, hub, id)
	}
	get, stdio := servedTwice(t, hub)
	requireOK(t, stdio(100, `{"id":100,"op":"close","session_id":"history-b"}`))

	status, whole := get("/sessions/history")
	if status != http.StatusOK {
		t.Fatalf("GET /sessions/history: %d %s", status, whole)
	}
	result := stdio(1, `{"id":1,"op":"history"}`)
	requireOK(t, result)
	if string(result.Result) != whole {
		t.Fatalf("history drifted:\n http %s\nstdio %s", whole, result.Result)
	}
	var listed serve.HistoryPage
	if err := json.Unmarshal([]byte(whole), &listed); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, entry := range listed.Sessions {
		states[entry.SessionID] = entry.State
	}
	if len(states) != 3 || states["history-a"] != "live" || states["history-b"] != "closed" || states["history-c"] != "live" {
		t.Fatalf("states = %v, want a and c live and b closed", states)
	}

	cursor := ""
	var paged []string
	for id := int64(2); id < 8; id++ {
		path, line := "/sessions/history?limit=1", `{"id":`+itoa(id)+`,"op":"history","limit":1}`
		if cursor != "" {
			path += "&cursor=" + cursor
			line = `{"id":` + itoa(id) + `,"op":"history","limit":1,"cursor":"` + cursor + `"}`
		}
		status, body := get(path)
		page := stdio(id, line)
		requireOK(t, page)
		if status != http.StatusOK || string(page.Result) != body {
			t.Fatalf("page drifted (%d):\n http %s\nstdio %s", status, body, page.Result)
		}
		var decoded serve.HistoryPage
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatal(err)
		}
		for _, entry := range decoded.Sessions {
			paged = append(paged, entry.SessionID)
		}
		if decoded.NextCursor == "" {
			break
		}
		cursor = decoded.NextCursor
	}
	if len(paged) != 3 {
		t.Fatalf("paging by one listed %v, want all three", paged)
	}
	for i, entry := range listed.Sessions {
		if paged[i] != entry.SessionID {
			t.Fatalf("paging listed %v, not the order of the whole list", paged)
		}
	}
}

func TestSessionHistoryRefusesAsHTTPDoes(t *testing.T) {
	store, err := binding.File(filepath.Join(t.TempDir(), "sessions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	get, stdio := servedTwice(t, historyHub(t, store))
	cases := []struct {
		path, line, code string
		status           int
	}{
		{"/sessions/history?cursor=not-a-cursor", `{"id":1,"op":"history","cursor":"not-a-cursor"}`, "invalid_cursor", http.StatusBadRequest},
		{"/sessions/history?limit=0", `{"id":2,"op":"history","limit":0}`, "invalid_request", http.StatusBadRequest},
		{"/sessions/history?limit=101", `{"id":3,"op":"history","limit":101}`, "invalid_request", http.StatusBadRequest},
	}
	for index, each := range cases {
		status, body := get(each.path)
		if status != each.status || httpCode(t, body) != each.code {
			t.Fatalf("GET %s: %d %s, want %d %s", each.path, status, body, each.status, each.code)
		}
		requireCode(t, stdio(int64(index+1), each.line), each.code)
	}
	requireCode(t, stdio(4, `{"id":4,"op":"history","session_id":"x"}`), "invalid_request")
}

func TestSessionHistoryIsUnadvertisedWithoutAStore(t *testing.T) {
	get, stdio := servedTwice(t, historyHub(t, nil))
	status, body := get("/sessions/history")
	if status != http.StatusBadRequest || httpCode(t, body) != "unsupported_feature" {
		t.Fatalf("GET /sessions/history with no store: %d %s", status, body)
	}
	response := stdio(1, `{"id":1,"op":"history"}`)
	requireCode(t, response, "unsupported_feature")
	if response.Error.Details["feature"] != "session.list" || response.Error.Details["reason"] != "unadvertised" {
		t.Fatalf("details = %v, want session.list unadvertised", response.Error.Details)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
