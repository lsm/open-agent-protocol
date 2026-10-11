package inferenceserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	ModelRef        string           `json:"model_ref"`
	Messages        []RequestMessage `json:"messages"`
	Tools           []RequestTool    `json:"tools,omitempty"`
	MaxOutputTokens int              `json:"max_output_tokens,omitempty"`
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
	if !r.serves(ref.ProviderID, ref.Wire) {
		refuse(CodeModelNotFound, "no such provider")
		return
	}
	conversation, err := conversationOf(request)
	if err != nil {
		refuse(CodeUnsupportedFeature, err.Error())
		return
	}
	credential, held := providercatalog.LookupCredential(r.Catalog, r.Environment, nil, ref.ProviderID)
	if !held {
		refuse(CodeCredentialMissing, "this provider needs a credential and none resolved")
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
	options := provider.StreamOptions{}
	if request.MaxOutputTokens > 0 {
		options.MaxTokens, options.HasMaxTokens = request.MaxOutputTokens, true
	}
	r.stream(ctx, state, model, conversation, options, credential.Key, emit)
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

func conversationOf(request CreateRequest) (provider.Context, error) {
	var conversation provider.Context
	for _, message := range request.Messages {
		text, err := textOf(message.Content)
		if err != nil {
			return provider.Context{}, err
		}
		switch message.Role {
		case "system":
			conversation.SystemPrompt, conversation.HasSystem = text, true
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

func (r *Runner) stream(ctx context.Context, state *State, model provider.Model, conversation provider.Context, options provider.StreamOptions, key string, emit func(Envelope)) {
	sink := &provider.EventSink{OnEvent: func(event provider.Event) {
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
		body, _ = provider.BuildAnthropicRequestBody(model, conversation, provider.AnthropicOptions{}, key)
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
		emitFailure(state, emit, model.Provider+" request failed: "+err.Error())
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
		emitFailure(state, emit, strings.TrimSpace(fmt.Sprintf("%s request failed: HTTP %d %s", model.Provider, response.StatusCode, bytes.TrimSpace(detail))))
		return
	}
	read := bodyReader(response.Body)
	if model.API == provider.AnthropicWire {
		provider.StreamAnthropic(sink, model, conversation, provider.AnthropicOptions{}, read, nil, func() string { return "" })
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
