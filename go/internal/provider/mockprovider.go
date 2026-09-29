package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type MockProvider struct {
	Server  *httptest.Server
	Frames  []string
	Paths   []string
	Headers []http.Header
	Bodies  []string

	mu       sync.Mutex
	FailWith int
}

func NewMockProvider(frames ...string) *MockProvider {
	mock := &MockProvider{Frames: frames}
	mock.Server = httptest.NewServer(http.HandlerFunc(mock.serve))
	return mock
}

func (m *MockProvider) Close() { m.Server.Close() }

func httpGet(url string) (string, error) {
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

func (m *MockProvider) URL() string { return m.Server.URL }

func (m *MockProvider) LastBody() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.Bodies) == 0 {
		return ""
	}
	return m.Bodies[len(m.Bodies)-1]
}

func (m *MockProvider) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Bodies)
}

func (m *MockProvider) serve(w http.ResponseWriter, r *http.Request) {
	var body strings.Builder
	if r.Body != nil {
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			body.Write(buf[:n])
			if err != nil {
				break
			}
		}
	}
	m.mu.Lock()
	m.Paths = append(m.Paths, r.URL.Path)
	m.Headers = append(m.Headers, r.Header.Clone())
	m.Bodies = append(m.Bodies, body.String())
	fail := m.FailWith
	frames := m.Frames
	m.mu.Unlock()

	if fail != 0 {
		w.WriteHeader(fail)
		fmt.Fprint(w, `{"error":{"message":"mock failure","type":"invalid_request_error"}}`)
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		fmt.Fprint(w, frame)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func TextTurn(text string) []string {
	return []string{
		SSEFrame(`{"choices":[{"index":0,"delta":{"content":"` + text + `"}}]}`),
		SSEFrame(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		SSEFrame("[DONE]"),
	}
}

func ToolTurn(id, name, arguments string) []string {
	return []string{
		SSEFrame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"` + id + `","type":"function","function":{"name":"` + name + `"}}]}}]}`),
		SSEFrame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"` + arguments + `"}}]}}]}`),
		SSEFrame(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`),
		SSEFrame("[DONE]"),
	}
}

func UsageTurn(input, output int) []string {
	return []string{
		SSEFrame(`{"choices":[{"index":0,"delta":{"content":"counted"}}]}`),
		SSEFrame(fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}}`, input, output, input+output)),
		SSEFrame("[DONE]"),
	}
}

func ErrorTurn(message string) []string {
	return []string{
		SSEFrame(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
		SSEFrame("[DONE]"),
	}
}
