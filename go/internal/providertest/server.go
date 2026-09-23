package providertest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type API string

const (
	OpenAIResponses      API = "openai-responses"
	AnthropicMessages    API = "anthropic-messages"
	OpenAIChatCompletion API = "openai-chat-completions"

	ResponsesPath      = "/v1/responses"
	MessagesPath       = "/v1/messages"
	ChatCompletionPath = "/v1/chat/completions"

	FixtureText          = "fixture response"
	FixtureToolName      = "fixture_tool"
	FixtureToolArguments = `{"value":"fixture"}`
)

type Case string

const (
	Success   Case = "success"
	Tool      Case = "tool"
	Error     Case = "error"
	Malformed Case = "malformed"
	Slow      Case = "slow"
)

type Config struct {
	OpenAIKey    string
	AnthropicKey string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type step struct {
	scenario Case
	call     ToolCall
}

type Request struct {
	API    API
	Method string
	Path   string
	Header http.Header
	Model  string
	Body   json.RawMessage
}

type gate struct {
	once sync.Once
	ch   chan struct{}
}

type Server struct {
	*httptest.Server
	mu       sync.Mutex
	config   Config
	queues   map[API][]step
	requests []Request
	gates    map[API]*gate
}

func New(t testing.TB, config Config) *Server {
	t.Helper()
	server := &Server{
		config: config,
		queues: make(map[API][]step),
		gates: map[API]*gate{
			OpenAIResponses:      {ch: make(chan struct{})},
			AnthropicMessages:    {ch: make(chan struct{})},
			OpenAIChatCompletion: {ch: make(chan struct{})},
		},
	}
	server.Server = httptest.NewServer(http.HandlerFunc(server.serveHTTP))
	t.Cleanup(func() {
		server.Release(OpenAIResponses)
		server.Release(AnthropicMessages)
		server.Release(OpenAIChatCompletion)
		server.Close()
	})
	return server
}

func (server *Server) OpenAIBaseURL() string    { return server.URL + "/v1" }
func (server *Server) AnthropicBaseURL() string { return server.URL }

func (server *Server) Enqueue(api API, cases ...Case) {
	server.mu.Lock()
	defer server.mu.Unlock()
	for _, scenario := range cases {
		server.queues[api] = append(server.queues[api], step{scenario: scenario, call: ToolCall{Name: FixtureToolName, Arguments: FixtureToolArguments}})
	}
}

func (server *Server) EnqueueToolCall(api API, call ToolCall) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.queues[api] = append(server.queues[api], step{scenario: Tool, call: call})
}

func (server *Server) Release(api API) {
	server.mu.Lock()
	current := server.gates[api]
	server.mu.Unlock()
	if current != nil {
		current.once.Do(func() { close(current.ch) })
	}
}

func (server *Server) Requests() []Request {
	server.mu.Lock()
	defer server.mu.Unlock()
	result := make([]Request, len(server.requests))
	for index, request := range server.requests {
		result[index] = cloneRequest(request)
	}
	return result
}

func (server *Server) RequestsFor(api API) []Request {
	var result []Request
	for _, request := range server.Requests() {
		if request.API == api {
			result = append(result, request)
		}
	}
	return result
}

func (server *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	api, ok := apiForPath(request.URL.Path)
	if !ok {
		http.Error(writer, "not found", http.StatusNotFound)
		return
	}
	body, readErr := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	model, stream, decodeErr := decodeRequest(body)
	server.record(Request{API: api, Method: request.Method, Path: request.URL.Path, Header: request.Header.Clone(), Model: model, Body: append(json.RawMessage(nil), body...)})
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if readErr != nil || decodeErr != nil {
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		http.Error(writer, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	if model == "" || !stream {
		http.Error(writer, "model and stream=true are required", http.StatusBadRequest)
		return
	}
	if !server.authorized(api, request.Header) {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	current, gate := server.next(api)
	if current.scenario == "" {
		http.Error(writer, "providertest: no queued case", http.StatusInternalServerError)
		return
	}
	switch current.scenario {
	case Error:
		server.writeError(writer, api)
	case Success, Tool, Malformed, Slow:
		server.writeStream(writer, request, api, current, model, gate)
	default:
		http.Error(writer, "providertest: unknown case", http.StatusInternalServerError)
	}
}

func apiForPath(path string) (API, bool) {
	switch path {
	case ResponsesPath:
		return OpenAIResponses, true
	case MessagesPath:
		return AnthropicMessages, true
	case ChatCompletionPath:
		return OpenAIChatCompletion, true
	default:
		return "", false
	}
}

func decodeRequest(body []byte) (string, bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := decoder.Decode(&request); err != nil {
		return "", false, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", false, fmt.Errorf("trailing JSON value")
	}
	return request.Model, request.Stream, nil
}

func (server *Server) authorized(api API, header http.Header) bool {
	switch api {
	case AnthropicMessages:
		return server.config.AnthropicKey != "" && header.Get("x-api-key") == server.config.AnthropicKey && header.Get("anthropic-version") != "" && header.Get("Authorization") == ""
	default:
		return server.config.OpenAIKey != "" && header.Get("Authorization") == "Bearer "+server.config.OpenAIKey && header.Get("x-api-key") == ""
	}
}

func (server *Server) record(request Request) {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.requests = append(server.requests, cloneRequest(request))
}

func (server *Server) next(api API) (step, *gate) {
	server.mu.Lock()
	defer server.mu.Unlock()
	queue := server.queues[api]
	if len(queue) == 0 {
		return step{}, server.gates[api]
	}
	current := queue[0]
	server.queues[api] = append([]step(nil), queue[1:]...)
	return current, server.gates[api]
}

func cloneRequest(request Request) Request {
	request.Header = request.Header.Clone()
	request.Body = append(json.RawMessage(nil), request.Body...)
	return request
}

func (server *Server) writeError(writer http.ResponseWriter, api API) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Retry-After", "1")
	writer.WriteHeader(http.StatusTooManyRequests)
	if api == AnthropicMessages {
		_, _ = io.WriteString(writer, `{"type":"error","error":{"type":"rate_limit_error","message":"fixture rate limit"}}`)
		return
	}
	_, _ = io.WriteString(writer, `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"fixture rate limit"}}`)
}

func (server *Server) writeStream(writer http.ResponseWriter, request *http.Request, api API, current step, model string, release *gate) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	write := func(frame string) bool {
		if _, err := io.WriteString(writer, frame); err != nil {
			return false
		}
		return http.NewResponseController(writer).Flush() == nil
	}
	var call *ToolCall
	if current.scenario == Tool {
		call = &current.call
	}
	frames := streamFrames(api, call, model)
	if len(frames) == 0 || !write(frames[0]) {
		return
	}
	if current.scenario == Malformed {
		_, _ = io.WriteString(writer, "data: {\"type\":\n\n")
		return
	}
	if current.scenario == Slow {
		select {
		case <-release.ch:
		case <-request.Context().Done():
			return
		}
	}
	for _, frame := range frames[1:] {
		if !write(frame) {
			return
		}
	}
}

func streamFrames(api API, call *ToolCall, model string) []string {
	switch api {
	case OpenAIResponses:
		return responsesFrames(call, model)
	case AnthropicMessages:
		return messagesFrames(call, model)
	case OpenAIChatCompletion:
		return chatFrames(call, model)
	default:
		return nil
	}
}

func sse(event, data string) string {
	if event == "" {
		return "data: " + data + "\n\n"
	}
	return "event: " + event + "\ndata: " + data + "\n\n"
}

func responsesFrames(call *ToolCall, model string) []string {
	created := sse("response.created", fmt.Sprintf(`{"type":"response.created","sequence_number":0,"response":{"id":"resp_fixture","object":"response","status":"in_progress","model":%q,"output":[]}}`, model))
	if call != nil {
		id, name, arguments := jsonString(callID(call, "call_fixture")), jsonString(call.Name), jsonString(call.Arguments)
		item := `{"id":"fc_fixture","type":"function_call","call_id":` + id + `,"name":` + name + `,"arguments":""}`
		done := `{"id":"fc_fixture","type":"function_call","call_id":` + id + `,"name":` + name + `,"arguments":` + arguments + `}`
		return []string{
			created,
			sse("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":`+item+`}`),
			sse("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_fixture","delta":`+arguments+`}`),
			sse("response.function_call_arguments.done", `{"type":"response.function_call_arguments.done","sequence_number":3,"output_index":0,"item_id":"fc_fixture","arguments":`+arguments+`}`),
			sse("response.output_item.done", `{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":`+done+`}`),
			sse("response.completed", fmt.Sprintf(`{"type":"response.completed","sequence_number":5,"response":{"id":"resp_fixture","object":"response","status":"completed","model":%q,"output":[%s]}}`, model, done)),
		}
	}
	item := `{"id":"msg_fixture","type":"message","role":"assistant","status":"in_progress","content":[]}`
	part := `{"type":"output_text","text":"","annotations":[]}`
	return []string{
		created,
		sse("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":`+item+`}`),
		sse("response.content_part.added", `{"type":"response.content_part.added","sequence_number":2,"output_index":0,"item_id":"msg_fixture","content_index":0,"part":`+part+`}`),
		sse("response.output_text.delta", `{"type":"response.output_text.delta","sequence_number":3,"output_index":0,"item_id":"msg_fixture","content_index":0,"delta":"fixture response"}`),
		sse("response.output_text.done", `{"type":"response.output_text.done","sequence_number":4,"output_index":0,"item_id":"msg_fixture","content_index":0,"text":"fixture response"}`),
		sse("response.content_part.done", `{"type":"response.content_part.done","sequence_number":5,"output_index":0,"item_id":"msg_fixture","content_index":0,"part":{"type":"output_text","text":"fixture response","annotations":[]}}`),
		sse("response.output_item.done", `{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg_fixture","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"fixture response","annotations":[]}]}}`),
		sse("response.completed", fmt.Sprintf(`{"type":"response.completed","sequence_number":7,"response":{"id":"resp_fixture","object":"response","status":"completed","model":%q,"output":[{"id":"msg_fixture","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"fixture response","annotations":[]}]}]}}`, model)),
	}
}

func messagesFrames(call *ToolCall, model string) []string {
	start := sse("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`, model))
	if call != nil {
		return []string{
			start,
			sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":`+jsonString(callID(call, "toolu_fixture"))+`,"name":`+jsonString(call.Name)+`,"input":{}}}`),
			sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+jsonString(call.Arguments)+`}}`),
			sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
			sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":1}}`),
			sse("message_stop", `{"type":"message_stop"}`),
		}
	}
	return []string{
		start,
		sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"fixture response"}}`),
		sse("content_block_stop", `{"type":"content_block_stop","index":0}`),
		sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`),
		sse("message_stop", `{"type":"message_stop"}`),
	}
}

func chatFrames(call *ToolCall, model string) []string {
	chunk := func(delta, finish string) string {
		return sse("", fmt.Sprintf(`{"id":"chatcmpl_fixture","object":"chat.completion.chunk","created":1,"model":%q,"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`, model, delta, finish))
	}
	if call != nil {
		return []string{
			chunk(`{"role":"assistant","content":null}`, "null"),
			chunk(`{"tool_calls":[{"index":0,"id":`+jsonString(callID(call, "call_fixture"))+`,"type":"function","function":{"name":`+jsonString(call.Name)+`,"arguments":`+jsonString(call.Arguments)+`}}]}`, "null"),
			chunk(`{}`, `"tool_calls"`),
			sse("", "[DONE]"),
		}
	}
	return []string{
		chunk(`{"role":"assistant","content":""}`, "null"),
		chunk(`{"content":"fixture response"}`, "null"),
		chunk(`{}`, `"stop"`),
		sse("", "[DONE]"),
	}
}

func EventNames(data string) []string {
	var names []string
	for _, block := range strings.Split(data, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if strings.HasPrefix(line, "event: ") {
				names = append(names, strings.TrimPrefix(line, "event: "))
			}
		}
	}
	return names
}

func callID(call *ToolCall, fallback string) string {
	if call.ID == "" {
		return fallback
	}
	return call.ID
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
