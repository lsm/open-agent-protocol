package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type httpParityRequest struct {
	method  string
	path    string
	media   string
	origin  string
	body    string
	unbound bool
}

func openRequestBody(session string) string {
	return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.request","id":"open-` + session + `","payload":{"session_id":"` + session + `"}}`
}

func submitRequestBody(session string) string {
	return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.request","id":"submit-` + session + `","session_id":"` + session + `","payload":{"session_id":"` + session + `","delivery":"auto","messages":[{"role":"user","content":"run"}]}}`
}

var httpParityRequests = []httpParityRequest{
	{method: "GET", path: "/adapters"},
	{method: "GET", path: "/adapters/memory/capabilities"},
	{method: "GET", path: "/adapters/ghost/capabilities"},
	{method: "GET", path: "/sessions"},
	{method: "GET", path: "/nowhere"},
	{method: "DELETE", path: "/sessions"},
	{method: "GET", path: "/adapters", origin: "http://evil.example"},
	{method: "GET", path: "/adapters", unbound: true},
	{method: "POST", path: "/adapters/memory/sessions", media: "text/plain", body: openRequestBody("s")},
	{method: "POST", path: "/adapters/memory/sessions", media: "application/json", body: "{"},
	{method: "POST", path: "/adapters/memory/sessions", media: "application/json", body: openRequestBody("s")},
	{method: "POST", path: "/adapters/memory/sessions", media: "application/json", body: openRequestBody("s")},
	{method: "GET", path: "/sessions/s/state"},
	{method: "GET", path: "/sessions/ghost/state"},
	{method: "GET", path: "/sessions/s/tools"},
	{method: "GET", path: "/sessions/s/models"},
	{method: "GET", path: "/sessions/s/transcript"},
	{method: "GET", path: "/sessions/s/transcript?limit=501"},
	{method: "GET", path: "/sessions/history"},
	{method: "POST", path: "/sessions/s/submit", media: "application/json", body: strings.Replace(submitRequestBody("s"), `"payload":{"session_id":"s"`, `"payload":{"session_id":"other"`, 1)},
	{method: "POST", path: "/sessions/s/submit", media: "application/json", body: submitRequestBody("s")},
	{method: "POST", path: "/sessions/s/close", media: "application/json"},
	{method: "GET", path: "/work"},
	{method: "GET", path: "/work/capabilities"},
	{method: "GET", path: "/work/sessions/ghost"},
	{method: "GET", path: "/work/sessions/ghost/read?after=x"},
	{method: "POST", path: "/shutdown", media: "application/json", body: `{"mode":"later"}`},
}

func TestHubHTTPAnswersGoapAndRustTheSame(t *testing.T) {
	rust := os.Getenv("OAP_RUST_HUB_BIN")
	if rust == "" {
		t.Skip("set OAP_RUST_HUB_BIN to an oap-rust-hub binary to compare its HTTP hub with goap's")
	}
	if !filepath.IsAbs(rust) {
		t.Fatalf("OAP_RUST_HUB_BIN must be absolute, got %q", rust)
	}
	want := httpExchange(t, oapBinary(t), "--session-history=")
	got := httpExchange(t, rust)
	for index, request := range httpParityRequests {
		if !reflect.DeepEqual(want[index], got[index]) {
			t.Errorf("%s %s answers differently\n--- goap\n%v\n--- oap-rust-hub\n%v", request.method, request.path, want[index], got[index])
		}
	}
}

func httpExchange(t *testing.T, binary string, extra ...string) []map[string]any {
	t.Helper()
	command := exec.Command(binary, append([]string{"serve", "--addr=127.0.0.1:0"}, extra...)...)
	command.Stdin = strings.NewReader("")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	bound := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if address, ok := strings.CutPrefix(scanner.Text(), "listening on http://"); ok {
				bound <- address
				return
			}
		}
		bound <- ""
	}()
	var address string
	select {
	case address = <-bound:
	case <-time.After(30 * time.Second):
		t.Fatalf("%s serve --addr never reported a bound address", binary)
	}
	if address == "" {
		t.Fatalf("%s serve --addr bound nothing", binary)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	answers := make([]map[string]any, 0, len(httpParityRequests))
	for _, asked := range httpParityRequests {
		request, err := http.NewRequest(asked.method, "http://"+address+asked.path, strings.NewReader(asked.body))
		if err != nil {
			t.Fatal(err)
		}
		if asked.media != "" {
			request.Header.Set("Content-Type", asked.media)
		}
		if asked.origin != "" {
			request.Header.Set("Origin", asked.origin)
		}
		if asked.unbound {
			request.Host = "example.com"
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", asked.method, asked.path, err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, map[string]any{
			"status": response.StatusCode,
			"media":  response.Header.Get("Content-Type"),
			"allow":  response.Header.Get("Allow"),
			"body":   normalizedHTTPBody(body),
		})
	}
	return answers
}

var mintedMembers = map[string]bool{"id": true, "in_reply_to": true, "created_at": true, "updated_at": true, "updated_at_ms": true, "timestamp_ms": true, "last_activity_ms": true}

func normalizedHTTPBody(body []byte) any {
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return strings.TrimSpace(string(body))
	}
	return withoutMinted(decoded)
}

func withoutMinted(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		kept := make(map[string]any, len(typed))
		for key, member := range typed {
			if !mintedMembers[key] {
				kept[key] = withoutMinted(member)
			}
		}
		if kept["code"] == "malformed_json" {
			delete(kept, "message")
		}
		return kept
	case []any:
		for index, member := range typed {
			typed[index] = withoutMinted(member)
		}
		return typed
	default:
		return fmt.Sprint(typed)
	}
}
