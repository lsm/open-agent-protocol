package inferenceserve

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/providercatalog"
)

const parityModelRef = "deepseek/openai-chat-completions@deepseek-v4-pro"

func chunkFrame(delta string, finish string, usage string) string {
	finishValue := "null"
	if finish != "" {
		finishValue = `"` + finish + `"`
	}
	frame := `{"id":"x","object":"chat.completion.chunk","created":1,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finishValue + `}]`
	if usage != "" {
		frame += `,"usage":` + usage
	}
	return "data: " + frame + "}\n\n"
}

const parityUsage = `{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}`

var parityScenarios = map[string][]string{
	"text": {
		chunkFrame(`{"role":"assistant","content":"hel"}`, "", ""),
		chunkFrame(`{"content":"lo"}`, "", ""),
		chunkFrame(`{}`, "stop", parityUsage),
	},
	"reasoning then text": {
		chunkFrame(`{"role":"assistant","reasoning_content":"think"}`, "", ""),
		chunkFrame(`{"content":"answer"}`, "", ""),
		chunkFrame(`{}`, "stop", parityUsage),
	},
	"text then a tool call": {
		chunkFrame(`{"role":"assistant","content":"look"}`, "", ""),
		chunkFrame(`{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}`, "", ""),
		chunkFrame(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a\"}"}}]}`, "", ""),
		chunkFrame(`{}`, "tool_calls", parityUsage),
	},
	"a refused key": {
		`refuse {"error":{"message":"Authentication Fails, Your api key is invalid","type":"authentication_error"}}`,
	},
	"two tool calls": {
		chunkFrame(`{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a\"}"}}]}`, "", ""),
		chunkFrame(`{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"read","arguments":"{\"path\":\"b\"}"}}]}`, "", ""),
		chunkFrame(`{}`, "tool_calls", parityUsage),
	},
}

func parityProvider(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("content-type", "application/json")
			io.WriteString(w, `{"object":"list","data":[{"id":"deepseek-v4-pro","object":"model","context_length":128000,"max_output_tokens":8192,"supports_reasoning":true,"supports_image_in":false}]}`)
			return
		}
		io.Copy(io.Discard, r.Body)
		if len(frames) == 1 && strings.HasPrefix(frames[0], "refuse ") {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, strings.TrimPrefix(frames[0], "refuse "))
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		for _, frame := range frames {
			io.WriteString(w, frame)
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server
}

func parityCreate(id string) map[string]any {
	return map[string]any{
		"protocol": Protocol, "version": Version, "profile": Profile,
		"type": "inference.create.request", "id": id,
		"payload": map[string]any{"model_ref": parityModelRef, "messages": []any{map[string]any{"role": "user", "content": "hi"}}},
	}
}

func uniqueService(t *testing.T) string {
	t.Helper()
	held := make([]byte, 16)
	if _, err := rand.Read(held); err != nil {
		t.Fatal(err)
	}
	return "makai-test-" + hex.EncodeToString(held)
}

func oapxTrace(t *testing.T, binary, baseURL string) []map[string]any {
	t.Helper()
	home := t.TempDir()
	service := uniqueService(t)
	t.Cleanup(func() {
		if runtime.GOOS == "darwin" {
			exec.Command("security", "delete-generic-password", "-s", service).Run()
		}
	})
	cmd := exec.Command(binary, "serve", "provider", "--stdio")
	cmd.Env = []string{
		"HOME=" + home, "PATH=" + os.Getenv("PATH"), "OAPX_KEYCHAIN_SERVICE=" + service,
		"DEEPSEEK_API_KEY=sk-oap-parity", "DEEPSEEK_BASE_URL=" + baseURL,
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
		}
	})
	lines := make(chan map[string]any, 64)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		for scanner.Scan() {
			var envelope map[string]any
			if json.Unmarshal(scanner.Bytes(), &envelope) == nil {
				lines <- envelope
			}
		}
		close(lines)
	}()
	deadline := time.After(30 * time.Second)
	for attempt := 0; ; attempt++ {
		request := parityCreate("c0")
		encoded, _ := json.Marshal(request)
		if _, err := stdin.Write(append(encoded, '\n')); err != nil {
			t.Fatal(err)
		}
		trace := []map[string]any{request}
		for {
			var envelope map[string]any
			select {
			case held, open := <-lines:
				if !open {
					t.Fatalf("oapx closed its output after %v", trace)
				}
				envelope = held
			case <-deadline:
				t.Fatalf("oapx settled nothing in 30s; saw %v", trace)
			}
			trace = append(trace, envelope)
			typ, _ := envelope["type"].(string)
			if typ == "inference.create.response" {
				payload, _ := envelope["payload"].(map[string]any)
				if accepted, _ := payload["accepted"].(bool); !accepted {
					message, _ := payload["error"].(map[string]any)["message"].(string)
					if strings.Contains(message, "not loaded") {
						break
					}
					return trace
				}
			}
			if typ == "inference.completed" || typ == "inference.failed" {
				return trace
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func goTrace(t *testing.T, baseURL string) []map[string]any {
	t.Helper()
	catalog := heldParityCatalog(t)
	env := []providercatalog.EnvironmentValue{{Name: "DEEPSEEK_API_KEY", Value: "sk-oap-parity"}, {Name: "DEEPSEEK_BASE_URL", Value: baseURL}}
	found := provider.Discoverer{Catalog: catalog, Environment: env}.Discover(context.Background(), provider.DiscoverForceFetch)
	runner := &Runner{Catalog: catalog, Environment: env, Models: found.Models, KeyRefused: found.KeyRefused, NewID: func() string { return "i1" }}
	var request CreateRequest
	encoded, _ := json.Marshal(parityCreate("c0")["payload"])
	if err := json.Unmarshal(encoded, &request); err != nil {
		t.Fatal(err)
	}
	trace := []map[string]any{parityCreate("c0")}
	runner.Create(context.Background(), "c0", request, func(envelope Envelope) {
		held, _ := json.Marshal(envelope)
		var decoded map[string]any
		json.Unmarshal(held, &decoded)
		trace = append(trace, decoded)
	})
	return trace
}

func heldParityCatalog(t *testing.T) providercatalog.Catalog {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "providers", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog providercatalog.Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	return catalog
}

func normalizedTrace(trace []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(trace))
	for _, envelope := range trace {
		held := map[string]any{}
		for key, value := range envelope {
			switch key {
			case "id", "timestamp_ms":
				continue
			case "inference_id":
				held[key] = "inference"
			default:
				held[key] = value
			}
		}
		if payload, ok := held["payload"].(map[string]any); ok {
			copied := map[string]any{}
			for key, value := range payload {
				if key == "started_at_ms" {
					value = 0
				}
				copied[key] = value
			}
			held["payload"] = copied
		}
		out = append(out, held)
	}
	return out
}

func TestInferenceTracesMatchOapx(t *testing.T) {
	binary := os.Getenv("OAP_OAPX_BIN")
	if binary == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to compare its inference traces with this layer's")
	}
	if !filepath.IsAbs(binary) {
		t.Fatalf("OAP_OAPX_BIN must be absolute, got %q", binary)
	}
	for name, frames := range parityScenarios {
		t.Run(name, func(t *testing.T) {
			server := parityProvider(t, frames)
			served := goTrace(t, server.URL)
			want := normalizedTrace(oapxTrace(t, binary, server.URL))
			got := normalizedTrace(served)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("the traces differ:\n%s", traceDifference(want, got))
			}
			settled := "inference.completed"
			if name == "a refused key" {
				settled = "inference.failed"
			}
			if last := served[len(served)-1]["type"]; last != settled {
				t.Fatalf("both trees settled %v, want %s", last, settled)
			}
			assertValidTrace(t, served)
		})
	}
}

func assertValidTrace(t *testing.T, trace []map[string]any) {
	t.Helper()
	envelopes := make([]Envelope, 0, len(trace))
	for _, line := range trace[1:] {
		held, _ := json.Marshal(line)
		var envelope Envelope
		if err := json.Unmarshal(held, &envelope); err != nil {
			t.Fatal(err)
		}
		envelopes = append(envelopes, envelope)
	}
	assertValid(t, envelopes)
}

func traceDifference(want, got []map[string]any) string {
	var out strings.Builder
	for i := 0; i < len(want) || i < len(got); i++ {
		var left, right []byte
		if i < len(want) {
			left, _ = json.Marshal(want[i])
		}
		if i < len(got) {
			right, _ = json.Marshal(got[i])
		}
		if string(left) == string(right) {
			continue
		}
		out.WriteString("envelope " + strconv.Itoa(i) + "\n  oapx: " + string(left) + "\n  go:   " + string(right) + "\n")
	}
	return out.String()
}
