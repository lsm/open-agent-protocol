package inferenceserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/providercatalog"
)

const (
	CodeInvalidRequest     = "invalid_request"
	CodeModelNotFound      = "model_not_found"
	CodeUnsupportedFeature = "unsupported_feature"
	CodeCredentialMissing  = "credential_missing"

	maxErrorBody = 64 * 1024
)

var oapWires = map[string]string{
	"openai-chat-completions": "openai-completions",
	"anthropic-messages":      "anthropic-messages",
}

type CreateRequest struct {
	ModelRef              string            `json:"model_ref"`
	Messages              []RequestMessage  `json:"messages"`
	Tools                 []RequestTool     `json:"tools,omitempty"`
	MaxOutputTokens       int               `json:"max_output_tokens,omitempty"`
	Temperature           *float64          `json:"temperature,omitempty"`
	TopP                  *float64          `json:"top_p,omitempty"`
	Stream                *bool             `json:"stream,omitempty"`
	IncludeSnapshot       string            `json:"include_snapshot,omitempty"`
	AllowDegradedFeatures []string          `json:"allow_degraded_features,omitempty"`
	Headers               map[string]string `json:"headers,omitempty"`
	OutputSchema          json.RawMessage   `json:"output_schema,omitempty"`
	Reasoning             json.RawMessage   `json:"reasoning,omitempty"`
	ToolChoice            json.RawMessage   `json:"tool_choice,omitempty"`
	CredentialRef         string            `json:"credential_ref,omitempty"`
}

type RequestMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type RequestTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type ModelRef struct {
	ProviderID string
	Wire       string
	ModelID    string
}

type Runner struct {
	Catalog     providercatalog.Catalog
	Environment []providercatalog.EnvironmentValue
	Models      []provider.ListedModel
	KeyRefused  []string
	Client      *http.Client
	Ids         *Ids
	Now         func() int64
	NewID       func() string
}

func ParseModelRef(ref string) (ModelRef, bool) {
	slash := strings.IndexByte(ref, '/')
	if slash <= 0 {
		return ModelRef{}, false
	}
	rest := ref[slash+1:]
	at := strings.IndexByte(rest, '@')
	if at <= 0 || at+1 >= len(rest) {
		return ModelRef{}, false
	}
	wire, known := oapWires[rest[:at]]
	if !known {
		return ModelRef{}, false
	}
	return ModelRef{ProviderID: ref[:slash], Wire: wire, ModelID: rest[at+1:]}, true
}

func (r *Runner) Create(ctx context.Context, requestID string, request CreateRequest, emit func(Envelope)) {
	ids := r.Ids
	if ids == nil {
		ids = &Ids{}
	}
	refusal := NewState(ids, "", request.ModelRef)
	refuse := func(code, message string) {
		if envelope, err := refusal.Refused(requestID, code, message); err == nil {
			emit(envelope)
		}
	}
	ref, ok := ParseModelRef(request.ModelRef)
	if !ok {
		refuse(CodeInvalidRequest, "model_ref must be provider_id/wire@model_id")
		return
	}
	if !r.servesProvider(ref.ProviderID) {
		refuse(r.unservedRefusal(ref.ProviderID))
		return
	}
	if !r.serves(ref.ProviderID, ref.Wire) {
		refuse(CodeInvalidRequest, "model_ref names a wire this provider does not speak")
		return
	}
	if request.CredentialRef != "" && !r.anonymous(ref.ProviderID) {
		refuse(CodeCredentialMissing, "credential_ref names no credential this implementation holds")
		return
	}
	choice, ok := toolChoiceOf(request.ToolChoice)
	if !ok {
		refuse(CodeInvalidRequest, "tool_choice must be auto, none, required or an object naming a function")
		return
	}
	if reason := unforwarded(request); reason != "" {
		refuse(CodeUnsupportedFeature, reason)
		return
	}
	if reason := messageRefusal(request.Messages); reason != "" {
		refuse(CodeUnsupportedFeature, reason)
		return
	}
	conversation, err := conversationOf(request)
	if err != nil {
		refuse(CodeUnsupportedFeature, err.Error())
		return
	}
	if reason := unforwardedAfterMessages(request); reason != "" {
		refuse(CodeUnsupportedFeature, reason)
		return
	}
	state := NewState(ids, r.newID(), request.ModelRef)
	accepted, err := state.Accepted(requestID, Honoured{IncludeSnapshot: "never"})
	if err != nil {
		return
	}
	emit(accepted)
	model, found := r.model(ref)
	if !found {
		if envelope, err := state.Failed(CodeModelNotFound, "this endpoint serves no such model"); err == nil {
			emit(envelope)
		}
		return
	}
	credential, held := providercatalog.LookupCredential(r.Catalog, r.Environment, nil, ref.ProviderID)
	if !held && !model.AllowsAnonymous {
		if envelope, err := state.Failed(CodeCredentialMissing, "this provider needs a credential and none resolved"); err == nil {
			emit(envelope)
		}
		return
	}
	options := provider.StreamOptions{}
	anthropic := provider.AnthropicOptions{}
	if request.MaxOutputTokens > 0 {
		options.MaxTokens, options.HasMaxTokens = request.MaxOutputTokens, true
		anthropic.MaxTokens, anthropic.HasMaxTokens = request.MaxOutputTokens, true
	}
	if request.Temperature != nil {
		options.Temperature, options.HasTemperature = *request.Temperature, true
		anthropic.Temperature, anthropic.HasTemperature = *request.Temperature, true
	}
	if choice.Mode != "" {
		options.ToolChoice, options.HasToolChoice = choice, true
		anthropic.ToolChoiceType, anthropic.ToolChoiceName, anthropic.HasToolChoice = choice.Mode, choice.Function, true
	}
	r.stream(ctx, state, model, conversation, options, anthropic, credential.Key, emit)
}

func present(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func toolChoiceOf(raw json.RawMessage) (provider.ToolChoice, bool) {
	if !present(raw) {
		return provider.ToolChoice{}, true
	}
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		switch mode {
		case provider.ToolChoiceAuto, provider.ToolChoiceNone, provider.ToolChoiceRequired:
			return provider.ToolChoice{Mode: mode}, true
		}
		return provider.ToolChoice{}, false
	}
	var named struct {
		Function string `json:"function"`
	}
	if json.Unmarshal(raw, &named) != nil || named.Function == "" {
		return provider.ToolChoice{}, false
	}
	return provider.ToolChoice{Mode: provider.ToolChoiceFunction, Function: named.Function}, true
}

func (r *Runner) anonymous(providerID string) bool {
	for _, listed := range r.Models {
		if listed.Model.Provider == providerID && listed.Model.AllowsAnonymous {
			return true
		}
	}
	return false
}

func unforwarded(request CreateRequest) string {
	if request.IncludeSnapshot != "" && request.IncludeSnapshot != "never" && !slices.Contains(request.AllowDegradedFeatures, "include_snapshot") {
		return "this endpoint does not serve snapshots yet"
	}
	if request.Stream != nil && !*request.Stream {
		return "this endpoint streams every inference and cannot answer unary"
	}
	if request.TopP != nil {
		return "this endpoint does not forward top_p"
	}
	if len(request.Headers) > 0 {
		return "this endpoint does not forward request headers to a provider"
	}
	return ""
}

func unforwardedAfterMessages(request CreateRequest) string {
	if present(request.OutputSchema) {
		return "this endpoint does not forward a structured output schema"
	}
	if present(request.Reasoning) {
		return "this endpoint does not forward reasoning options yet"
	}
	return ""
}

func (r *Runner) newID() string {
	if r.NewID != nil {
		return r.NewID()
	}
	return fmt.Sprintf("i%d", time.Now().UnixNano())
}

func (r *Runner) now() int64 {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now().UnixMilli()
}

func (r *Runner) servesProvider(providerID string) bool {
	for _, listed := range r.Models {
		if listed.Model.Provider == providerID {
			return true
		}
	}
	return false
}

func (r *Runner) unservedRefusal(providerID string) (string, string) {
	row, catalogued := catalogRow(r.Catalog, providerID)
	if !catalogued || !awaitsCredential(row) {
		return CodeModelNotFound, "no such provider"
	}
	if _, held := providercatalog.LookupCredential(r.Catalog, r.Environment, nil, providerID); !held {
		return CodeCredentialMissing, "this provider is served once it has a credential: sign in or set its key"
	}
	if slices.Contains(r.KeyRefused, providerID) {
		return CodeCredentialRejected, "the provider refused this key: sign in again or set a new key"
	}
	return CodeProviderUnavailable, "this provider has a credential but its models are not loaded: retry after they reload"
}

func catalogRow(catalog providercatalog.Catalog, id string) (providercatalog.Provider, bool) {
	for _, row := range catalog.Providers {
		if row.ID == id {
			return row, true
		}
	}
	return providercatalog.Provider{}, false
}

func awaitsCredential(row providercatalog.Provider) bool {
	if slices.Contains(row.Auth, "none") {
		return false
	}
	return providercatalog.FirstImplementedWire(row) != ""
}

func (r *Runner) serves(providerID, wire string) bool {
	for _, listed := range r.Models {
		if listed.Model.Provider == providerID && listed.Model.API == wire {
			return true
		}
	}
	return false
}

func (r *Runner) model(ref ModelRef) (provider.Model, bool) {
	for _, listed := range r.Models {
		if listed.Model.Provider == ref.ProviderID && listed.Model.API == ref.Wire && listed.Model.ID == ref.ModelID {
			return listed.Model, true
		}
	}
	return provider.Model{}, false
}

const (
	refusalTextOnly      = "this endpoint forwards text content only"
	refusalMisplacedPart = "a reasoning or tool_call part belongs to an assistant message, a tool_result to a user or tool message, and a tool-role message must carry one"
)

func messageRefusal(messages []RequestMessage) string {
	for _, message := range messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(message.Content, &parts) != nil {
			if message.Role == "tool" {
				return refusalMisplacedPart
			}
			continue
		}
		for _, part := range parts {
			if part.Type == "image" {
				return refusalTextOnly
			}
		}
		carriesResult := false
		for _, part := range parts {
			switch part.Type {
			case "reasoning", "tool_call":
				if message.Role != "assistant" {
					return refusalMisplacedPart
				}
			case "tool_result":
				if message.Role != "user" && message.Role != "tool" {
					return refusalMisplacedPart
				}
				carriesResult = true
			}
		}
		if message.Role == "tool" && !carriesResult {
			return refusalMisplacedPart
		}
	}
	return ""
}

func conversationOf(request CreateRequest) (provider.Context, error) {
	var conversation provider.Context
	for _, message := range request.Messages {
		text, err := textOf(message.Content)
		if err != nil {
			return provider.Context{}, err
		}
		switch message.Role {
		case "system", "developer":
			if conversation.HasSystem {
				conversation.SystemPrompt += "\n\n"
			}
			conversation.SystemPrompt += text
			conversation.HasSystem = true
		case "user":
			conversation.Messages = append(conversation.Messages, provider.Message{User: &provider.UserContent{Text: text, HasText: true}})
		case "assistant":
			conversation.Messages = append(conversation.Messages, provider.Message{Assistant: &provider.AssistantContent{Parts: []provider.ContentPart{{Text: &provider.TextPart{Text: text}}}}})
		default:
			return provider.Context{}, fmt.Errorf("this endpoint does not carry a %s message yet", message.Role)
		}
	}
	for _, tool := range request.Tools {
		conversation.Tools = append(conversation.Tools, provider.Tool{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema})
	}
	return conversation, nil
}

func textOf(content json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &parts); err != nil {
		return "", errors.New("this endpoint carries only text content yet")
	}
	var joined strings.Builder
	for _, part := range parts {
		if part.Type != "text" {
			return "", fmt.Errorf("this endpoint does not carry a %s part yet", part.Type)
		}
		joined.WriteString(part.Text)
	}
	return joined.String(), nil
}

func (r *Runner) stream(ctx context.Context, state *State, model provider.Model, conversation provider.Context, options provider.StreamOptions, anthropic provider.AnthropicOptions, key string, emit func(Envelope)) {
	sink := &provider.EventSink{OnEvent: func(event provider.Event) {
		if event.Kind == provider.EventError && ctx.Err() != nil {
			event.Reason = ReasonCancelled
		}
		out, err := state.Pump(event, r.now())
		if err != nil {
			return
		}
		for _, envelope := range out {
			emit(envelope)
		}
	}}
	var body []byte
	var headers []provider.Header
	if model.API == provider.AnthropicWire {
		body, _ = provider.BuildAnthropicRequestBody(model, conversation, anthropic, key)
		headers = provider.BuildAnthropicHeaders(key, nil)
	} else {
		body = provider.BuildRequestBody(model, conversation, options)
		headers = []provider.Header{{Name: "authorization", Value: provider.BearerValue(key)}}
	}
	url, ok := providercatalog.RequestURLForWire(r.Catalog, model.Provider, model.API, model.BaseURL)
	if !ok {
		if envelope, err := state.Failed(CodeEndpointError, "the endpoint has no request url for this wire"); err == nil {
			emit(envelope)
		}
		return
	}
	response, err := r.send(ctx, url, body, headers)
	if err != nil {
		if ctx.Err() != nil {
			if envelope, err := state.Failed(CodeAborted, "the caller cancelled this inference"); err == nil {
				emit(envelope)
			}
			return
		}
		emitFailure(state, emit, model.Provider+" request failed: "+err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		emitFailure(state, emit, httpFailure(model, response.StatusCode, detail))
		return
	}
	var seen bytes.Buffer
	read := bodyReader(io.TeeReader(response.Body, &limitedWriter{held: &seen, room: maxErrorBody}))
	if model.API == provider.AnthropicWire {
		provider.StreamAnthropic(sink, model, conversation, anthropic, read, nil, seen.String)
	} else {
		provider.Stream(sink, model, conversation, options, read, nil)
	}
	if !state.terminal {
		if envelope, err := state.Failed(CodeProviderUnavailable, "the provider stream ended with no result"); err == nil {
			emit(envelope)
		}
	}
}

func emitFailure(state *State, emit func(Envelope), message string) {
	if envelope, err := state.Failed(FailureCode(message), message); err == nil {
		emit(envelope)
	}
}

func (r *Runner) send(ctx context.Context, url string, body []byte, headers []provider.Header) (*http.Response, error) {
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("accept", "text/event-stream")
	for _, header := range headers {
		request.Header.Set(header.Name, header.Value)
	}
	return client.Do(request)
}

func bodyReader(body io.Reader) provider.ReadChunkFunc {
	return func() ([]byte, error) {
		chunk := make([]byte, 4096)
		n, err := body.Read(chunk)
		if n > 0 {
			return chunk[:n], nil
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return nil, nil
	}
}

func httpFailure(model provider.Model, status int, body []byte) string {
	if model.API == provider.AnthropicWire {
		hint := ""
		if status == http.StatusUnauthorized {
			hint = " (check ANTHROPIC_API_KEY is valid)"
		}
		return fmt.Sprintf("anthropic request failed: HTTP %d%s%s", status, hint, anthropicErrorDetail(body))
	}
	return fmt.Sprintf("%s request failed: HTTP %d%s", model.Provider, status, errorDetail(body))
}

func nonEmptyString(object map[string]json.RawMessage, key string) string {
	var text string
	if json.Unmarshal(object[key], &text) != nil {
		return ""
	}
	return text
}

func errorDetail(body []byte) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return ""
	}
	if raw, present := root["error"]; present {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) == nil && object != nil {
			message, kind := nonEmptyString(object, "message"), nonEmptyString(object, "type")
			switch {
			case message != "" && kind != "":
				return " (" + kind + ": " + message + ")"
			case message != "":
				return " (" + message + ")"
			case kind != "":
				return " (" + kind + ")"
			}
			return ""
		}
		var text string
		if json.Unmarshal(raw, &text) == nil && text != "" {
			return " (" + text + ")"
		}
		return ""
	}
	if text := nonEmptyString(root, "detail"); text != "" {
		return " (" + text + ")"
	}
	if text := nonEmptyString(root, "message"); text != "" {
		return " (" + text + ")"
	}
	return ""
}

func anthropicErrorDetail(body []byte) string {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil {
		return ""
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(root["error"], &object) != nil || object == nil {
		return ""
	}
	message := nonEmptyString(object, "message")
	if message == "" {
		return ""
	}
	if kind := nonEmptyString(object, "type"); kind != "" {
		return " (" + kind + ": " + message + ")"
	}
	return " (" + message + ")"
}

type limitedWriter struct {
	held *bytes.Buffer
	room int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if keep := min(len(p), w.room-w.held.Len()); keep > 0 {
		w.held.Write(p[:keep])
	}
	return len(p), nil
}
