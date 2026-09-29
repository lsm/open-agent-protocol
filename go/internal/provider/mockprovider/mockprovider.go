package mockprovider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

func frame(payload any) string {
	return "data: " + mustJSON(payload) + "\n\n"
}

func doneFrame() string {
	return "data: [DONE]\n\n"
}

type Provider struct {
	Server  *httptest.Server
	Frames  []string
	Paths   []string
	Headers []http.Header
	Bodies  []string

	mu          sync.Mutex
	failWith    int
	failMessage string
}

func New(frames ...string) *Provider {
	mock := &Provider{Frames: frames, failMessage: "mock failure"}
	mock.Server = httptest.NewServer(http.HandlerFunc(mock.serve))
	return mock
}

func (m *Provider) Refuse(status int, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failWith = status
	m.failMessage = message
}

func (m *Provider) Close() { m.Server.Close() }

func (m *Provider) URL() string { return m.Server.URL }

func (m *Provider) LastBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Bodies) == 0 {
		return ""
	}
	return m.Bodies[len(m.Bodies)-1]
}

func (m *Provider) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Bodies)
}

func (m *Provider) serve(w http.ResponseWriter, r *http.Request) {
	var body strings.Builder
	if r.Body != nil {
		chunk := make([]byte, 4096)
		for {
			n, err := r.Body.Read(chunk)
			body.Write(chunk[:n])
			if err != nil {
				break
			}
		}
	}
	m.mu.Lock()
	m.Paths = append(m.Paths, r.URL.Path)
	m.Headers = append(m.Headers, r.Header.Clone())
	m.Bodies = append(m.Bodies, body.String())
	fail := m.failWith
	message := m.failMessage
	frames := m.Frames
	m.mu.Unlock()

	if fail != 0 {
		w.WriteHeader(fail)
		fmt.Fprint(w, Error(message))
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, held := range frames {
		fmt.Fprint(w, held)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func Get(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func mustJSON(payload any) string {
	held, err := json.Marshal(payload)
	if err != nil {
		panic("mockprovider: a fixed payload cannot be marshalled: " + err.Error())
	}
	return string(held)
}

func TextTurn(text string) []string {
	return []string{
		frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}}),
		frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}}),
		doneFrame(),
	}
}

func ToolTurn(id, name, arguments string) []string {
	open := map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]any{"name": name}}
	arguments_ := map[string]any{"index": 0, "function": map[string]any{"arguments": arguments}}
	return []string{
		frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{open}}}}}),
		frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{arguments_}}}}}),
		frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}}),
		doneFrame(),
	}
}

func UsageTurn(input, output int) []string {
	return []string{
		frame(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "counted"}}}}),
		frame(map[string]any{"usage": map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}}),
		doneFrame(),
	}
}

func Error(message string) string {
	return mustJSON(map[string]any{"error": map[string]any{"message": message, "type": "invalid_request_error"}})
}
