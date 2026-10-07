package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"strings"
	"time"
)

const (
	oapProtocol = "open-agent-protocol"
	oapVersion  = "0.1"
	oapAgent    = "open-agent-protocol.agent-control-core"
	oapProvider = "open-agent-protocol.model-provider-core"
)

const sdkParticipant = "sdk"

func oapFrame(profile, kind string, payload any) protocol.Envelope {
	return protocol.Envelope{Protocol: oapProtocol, Version: oapVersion, Profile: profile,
		Type: protocol.EnvelopeType(kind), ID: protocol.EnvelopeID(newULID()), Payload: mustMarshal(payload)}
}

func oapRequest(ctx context.Context, t *transport, sub *subscription, timeout time.Duration, request protocol.Envelope) (protocol.Envelope, error) {
	sub.correlate(string(request.ID))
	defer sub.uncorrelate(string(request.ID))
	if err := t.sendEnvelope(request); err != nil {
		return protocol.Envelope{}, err
	}
	for {
		result, err := sub.next(ctx, timeout, string(request.Type))
		if err != nil {
			return protocol.Envelope{}, err
		}
		envelope, err := asEnvelope(result)
		if err != nil {
			if result.InReplyTo != string(request.ID) {
				continue
			}
			return protocol.Envelope{}, err
		}
		if envelope.InReplyTo != request.ID {
			continue
		}
		if envelope.Type == "error" || envelope.Type == "error.response" {
			return protocol.Envelope{}, oapFailure(envelope, "")
		}
		return envelope, nil
	}
}

func asEnvelope(f *frame) (protocol.Envelope, error) {
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(f.raw, &members); err != nil {
		return protocol.Envelope{}, transportErrorf(err, "malformed OAP envelope %s", f.Type)
	}
	if _, isText := f.Version.(string); !isText {
		members["version"] = mustMarshal(oapVersion)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(mustMarshal(members), &envelope); err != nil {
		return protocol.Envelope{}, transportErrorf(err, "malformed OAP envelope %s: %v", f.Type, err)
	}
	return envelope, nil
}

func envelopeFailure(f *frame, providerID string) error {
	envelope, err := asEnvelope(f)
	if err != nil {
		return err
	}
	return oapFailure(envelope, providerID)
}

func envelopePayload(env protocol.Envelope) jsonObject {
	payload, _ := decodeObject(env.Payload)
	if payload == nil {
		return jsonObject{}
	}
	return payload
}

func oapFailure(env protocol.Envelope, providerID string) error {
	payload := envelopePayload(env)
	if nested := payload.obj("error"); nested != nil {
		payload = nested
	}
	code := payload.str("code")
	message := payload.strOrDefault("OAP request failed", "message")
	if strings.HasPrefix(code, "credential_") || code == "auth_required" {
		return newAuthRequiredError(providerID, message)
	}
	return &StreamError{Kind: KindProviderError, Code: code, Message: message, ProviderID: providerID}
}

func oapModelParts(ref string) (string, string, string) {
	left, model, _ := strings.Cut(ref, "@")
	provider, wire, _ := strings.Cut(left, "/")
	return provider, wire, model
}

func oapMessages(messages []Message) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if message.Role == "" {
			return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "message role is required"}
		}
		entry := map[string]any{"role": string(message.Role)}
		if len(message.Parts) > 0 {
			parts := make([]map[string]any, 0, len(message.Parts))
			for _, part := range message.Parts {
				switch part.Type {
				case PartText:
					if part.TextSignature != "" {
						return nil, &ProtocolError{Code: "unsupported_feature", Message: "text signature has no OAP 0.1 projection"}
					}
					parts = append(parts, map[string]any{"type": "text", "text": part.Text})
				case PartThinking:
					item := map[string]any{"type": "reasoning", "reasoning": part.Thinking}
					if part.ThinkingSignature != "" {
						item["carry"] = part.ThinkingSignature
					}
					parts = append(parts, item)
				case PartImage:
					image := map[string]any{"data": part.Data, "media_type": part.MimeType}
					if part.ImageURL != "" {
						image = map[string]any{"url": part.ImageURL}
					}
					parts = append(parts, map[string]any{"type": "image", "image": image})
				case PartToolCall:
					if !json.Valid([]byte(part.ArgumentsJSON)) {
						return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "tool call arguments_json is not valid JSON"}
					}
					call := map[string]any{"type": "tool_call", "tool_call_id": part.ToolCallID,
						"name": part.Name, "arguments_json": json.RawMessage(part.ArgumentsJSON)}
					if part.ToolCallCarry != "" {
						call["carry"] = part.ToolCallCarry
					}
					parts = append(parts, call)
				case PartToolResult:
					if part.DetailsJSON != "" {
						return nil, &ProtocolError{Code: "unsupported_feature", Message: "tool result details_json has no OAP 0.1 projection"}
					}
					result := make([]map[string]any, 0, len(part.Content))
					for _, nested := range part.Content {
						if nested.Type != PartText {
							return nil, &ProtocolError{Code: "unsupported_feature", Message: "nested non-text tool result content has no OAP 0.1 projection"}
						}
						result = append(result, map[string]any{"type": "text", "text": nested.Text})
					}
					parts = append(parts, map[string]any{"type": "tool_result", "tool_call_id": part.ToolCallID, "result": result, "is_error": part.IsError})
				default:
					return nil, &ProtocolError{Code: "unsupported_feature", Message: "this content part has no OAP 0.1 projection"}
				}
			}
			if message.Role == RoleTool && message.ToolCallID != "" {
				allText := true
				for _, part := range parts {
					if part["type"] != "text" {
						allText = false
						break
					}
				}
				if allText {
					entry["content"] = []map[string]any{{"type": "tool_result", "tool_call_id": message.ToolCallID, "result": parts}}
				} else {
					entry["content"] = parts
				}
			} else {
				entry["content"] = parts
			}
		} else if message.Role == RoleTool {
			if message.ToolCallID == "" {
				return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "tool result requires tool_call_id"}
			}
			entry["content"] = []map[string]any{{"type": "tool_result", "tool_call_id": message.ToolCallID, "result": message.Text}}
		} else {
			entry["content"] = message.Text
		}
		if message.Name != "" && message.Role != RoleTool {
			return nil, &ProtocolError{Code: "unsupported_feature", Message: "named non-tool messages have no OAP 0.1 projection"}
		}
		out = append(out, entry)
	}
	return out, nil
}

func oapTools(tools []Tool) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		var schema any
		if err := json.Unmarshal([]byte(tool.ParametersSchemaJSON), &schema); err != nil {
			return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "tool input schema is not JSON"}
		}
		out = append(out, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": schema})
	}
	return out, nil
}

func oapUsage(value jsonObject) *Usage {
	if value == nil {
		return nil
	}
	input := value.intOr(0, "input_tokens")
	output := value.intOr(0, "output_tokens")
	return &Usage{Input: int64(input), Output: int64(output)}
}

func oapResponse(f *frame, modelRef, messageKey string) (*CompletionResponse, error) {
	payload := f.payload()
	provider, wire, model := oapModelParts(modelRef)
	message := payload.obj(messageKey)
	if message == nil || message.str("role") != string(RoleAssistant) {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP completion omitted an assistant message"}
	}
	parsed, err := oapResponseMessage(message["content"])
	if err != nil {
		return nil, err
	}
	result := &CompletionResponse{ProviderID: provider, API: wire, ModelID: model,
		StopReason: payload.str("stop_reason"), Usage: oapUsage(payload.obj("usage")),
		Message: parsed}
	return result, nil
}

func oapResponseMessage(content any) (ResponseMessage, error) {
	message := ResponseMessage{Role: RoleAssistant}
	if text, ok := content.(string); ok {
		message.Text = text
		return message, nil
	}
	rawParts, ok := content.([]any)
	if !ok || len(rawParts) == 0 {
		return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP assistant content is not text or a nonempty part list"}
	}
	message.Parts = make([]ContentPart, 0, len(rawParts))
	for _, raw := range rawParts {
		entry, ok := raw.(map[string]any)
		if !ok {
			return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP assistant part is not an object"}
		}
		part := jsonObject(entry)
		switch part.str("type") {
		case "text":
			text, ok := part["text"].(string)
			if !ok {
				return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP text part omitted text"}
			}
			message.Text += text
			message.Parts = append(message.Parts, ContentPart{Type: PartText, Text: text})
		case "reasoning":
			thinking, ok := part["reasoning"].(string)
			if !ok {
				return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP reasoning part omitted reasoning"}
			}
			message.Parts = append(message.Parts, ContentPart{Type: PartThinking, Thinking: thinking, ThinkingSignature: part.str("carry")})
		case "image":
			image := part.obj("image")
			if image == nil {
				return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP image part omitted image"}
			}
			if url := image.str("url"); url != "" {
				message.Parts = append(message.Parts, ContentPart{Type: PartImage, ImageURL: url})
			} else if data, media := image.str("data"), image.str("media_type"); data != "" && media != "" {
				message.Parts = append(message.Parts, ContentPart{Type: PartImage, Data: data, MimeType: media})
			} else {
				return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP image has no supported source"}
			}
		case "tool_call":
			if part.str("tool_call_id") == "" || part.str("name") == "" {
				return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP tool call omitted identity"}
			}
			arguments, present := part["arguments_json"]
			if !present {
				return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP tool call omitted arguments_json"}
			}
			encoded, err := json.Marshal(arguments)
			if err != nil {
				return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP tool call arguments could not be encoded"}
			}
			message.Parts = append(message.Parts, ContentPart{Type: PartToolCall, ToolCallID: part.str("tool_call_id"), Name: part.str("name"), ArgumentsJSON: string(encoded), ToolCallCarry: part.str("carry")})
		case "tool_result":
			if part.str("tool_call_id") == "" {
				return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP tool result omitted identity"}
			}
			result := ContentPart{Type: PartToolResult, ToolCallID: part.str("tool_call_id")}
			result.IsError, _ = part.boolean("is_error")
			switch value := part["result"].(type) {
			case string:
				result.Content = []ContentPart{{Type: PartText, Text: value}}
			case []any:
				for _, raw := range value {
					textPart, ok := raw.(map[string]any)
					if !ok || textPart["type"] != "text" {
						return message, &ProtocolError{Code: "unsupported_feature", Message: "OAP tool result contains unrepresentable content"}
					}
					text, ok := textPart["text"].(string)
					if !ok {
						return message, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP tool result text is malformed"}
					}
					result.Content = append(result.Content, ContentPart{Type: PartText, Text: text})
				}
			default:
				return message, &ProtocolError{Code: "unsupported_feature", Message: "OAP tool result contains an unrepresentable JSON value"}
			}
			message.Parts = append(message.Parts, result)
		default:
			return message, &ProtocolError{Code: "unsupported_feature", Message: "OAP assistant part has no Go SDK projection"}
		}
	}
	return message, nil
}

func (s *ModelsService) oapList(ctx context.Context, req ListModelsRequest) (*ListModelsResponse, error) {
	request := oapFrame(oapProvider, "provider.models.list.request", map[string]any{})
	if req.ProviderID != "" {
		request.Payload = mustMarshal(map[string]any{"provider_id": req.ProviderID})
	}
	sub := s.transport.subscribeStream(string(request.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, request)
	if err != nil {
		return nil, err
	}
	if response.Type != "provider.models.list.response" {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "expected provider.models.list.response"}
	}
	result := &ListModelsResponse{Models: []ModelDescriptor{}, FetchedAt: time.Now()}
	for _, raw := range envelopePayload(response).arr("models") {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "model entry is not an object"}
		}
		model := jsonObject(entry)
		source, err := oapModelSource(model)
		if err != nil {
			return nil, err
		}
		lifecycle, err := oapModelLifecycle(model)
		if err != nil {
			return nil, err
		}
		auth, err := oapModelAuth(model)
		if err != nil {
			return nil, err
		}
		if req.API != "" && model.str("wire") != req.API {
			continue
		}
		if req.ModelID != "" && model.str("model_id") != req.ModelID {
			continue
		}
		if req.IncludeDeprecated != nil && !*req.IncludeDeprecated && lifecycle != nil && *lifecycle == LifecycleDeprecated {
			continue
		}
		if req.IncludeLoginRequired != nil && !*req.IncludeLoginRequired && auth == AuthLoginRequired {
			continue
		}
		descriptor := ModelDescriptor{ModelRef: model.str("model_ref"), ModelID: model.str("model_id"),
			DisplayName: model.str("display_name"), ProviderID: model.str("provider_id"), API: model.str("wire"),
			AuthStatus: auth, Lifecycle: lifecycle,
			Source: source, ContextWindow: model.intOr(0, "context_window"),
			MaxOutputTokens: model.intOr(0, "max_output_tokens"), ReasoningDefault: ReasoningLevel(model.str("reasoning_default"))}
		if descriptor.DisplayName == "" {
			descriptor.DisplayName = descriptor.ModelID
		}
		for _, item := range model.arr("capabilities") {
			if name, ok := item.(string); ok {
				descriptor.Capabilities = append(descriptor.Capabilities, ModelCapability(name))
			}
		}
		if published, ok := model["cost"].(map[string]any); ok {
			rates := jsonObject(published)
			descriptor.Cost = &ModelCost{}
			if value, ok := rates.num("input"); ok {
				descriptor.Cost.Input = value
			}
			if value, ok := rates.num("output"); ok {
				descriptor.Cost.Output = value
			}
			if value, ok := rates.num("cache_read"); ok {
				descriptor.Cost.CacheRead = value
			}
			if value, ok := rates.num("cache_write"); ok {
				descriptor.Cost.CacheWrite = value
			}
		}
		for _, item := range model.arr("input_modalities") {
			if name, ok := item.(string); ok {
				descriptor.InputModalities = append(descriptor.InputModalities, name)
			}
		}
		for _, item := range model.arr("output_modalities") {
			if name, ok := item.(string); ok {
				descriptor.OutputModalities = append(descriptor.OutputModalities, name)
			}
		}
		for _, item := range model.arr("reasoning_levels") {
			if name, ok := item.(string); ok {
				descriptor.ReasoningLevels = append(descriptor.ReasoningLevels, ReasoningLevel(name))
			}
		}
		descriptor.ReleaseDate = model.str("release_date")
		descriptor.Family = model.str("family")
		result.Models = append(result.Models, descriptor)
	}
	if published, ok := envelopePayload(response)["catalog"].(map[string]any); ok {
		catalog := &ModelCatalog{}
		if value, ok := jsonObject(published).num("observed_at_ms"); ok {
			catalog.ObservedAtMS = int64(value)
		}
		if value, ok := published["complete"].(bool); ok {
			catalog.Complete = value
		}
		result.Catalog = catalog
	}
	return result, nil
}

func (s *ProviderService) oapStream(ctx context.Context, req CompletionRequest) (*ProviderStream, error) {
	if err := validateExecutionRequest(req.ModelRef, req.Messages); err != nil {
		return nil, err
	}
	messages, err := oapMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := oapTools(req.Tools)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"model_ref": req.ModelRef, "messages": messages,
		"stream": true, "include_snapshot": "never", "tools": tools}
	if req.Options != nil {
		if req.Options.Temperature != nil {
			payload["temperature"] = *req.Options.Temperature
		}
		if req.Options.MaxTokens != nil {
			payload["max_output_tokens"] = *req.Options.MaxTokens
		}
		if req.Options.ReasoningEffort != "" {
			payload["reasoning"] = map[string]any{"enabled": req.Options.ReasoningEffort != ReasoningOff, "effort": string(req.Options.ReasoningEffort)}
		}
		if req.Options.Metadata != nil {
			payload["metadata"] = req.Options.Metadata
		}
	}
	request := oapFrame(oapProvider, "inference.create.request", payload)
	sub := s.transport.subscribeStream(string(request.ID))
	sub.correlate(string(request.ID))
	if err := s.transport.sendEnvelope(request); err != nil {
		sub.close()
		return nil, err
	}
	return &ProviderStream{ctx: ctx, transport: s.transport, sub: sub, streamID: string(request.ID),
		timeout: s.timeout, fallbackProvider: providerIDFromRef(req.ModelRef),
		oap: true, oapModelRef: req.ModelRef, oapPartKinds: make(map[int]string)}, nil
}

func (s *ProviderService) oapComplete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	stream, err := s.oapStream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if stream.oapResponse == nil {
		return nil, &StreamError{Kind: KindTransportError, Message: "inference ended without completion"}
	}
	return stream.oapResponse, nil
}

func (s *ProviderStream) oapNext() bool {
	if s.done {
		return false
	}
	for {
		f, err := s.sub.next(s.ctx, s.timeout, "OAP inference")
		if err != nil {
			s.fail(err)
			return false
		}
		p := f.payload()
		switch f.Type {
		case "inference.create.response":
			if accepted, _ := p.boolean("accepted"); !accepted {
				s.fail(envelopeFailure(f, s.fallbackProvider))
				return false
			}
			s.oapInferenceID = f.InferenceID
		case "inference.started":
			provider, wire, model := oapModelParts(s.oapModelRef)
			s.current = &MessageStart{ProviderID: provider, API: wire, ModelID: model}
			return true
		case "inference.part.started":
			s.oapPartKinds[p.intOr(0, "part_index")] = p.str("part_kind")
		case "inference.part.delta":
			kind := s.oapPartKinds[p.intOr(0, "part_index")]
			if kind == "reasoning" {
				s.current = &ThinkingDelta{Delta: p.str("delta")}
				return true
			}
			if kind == "text" {
				s.current = &TextDelta{Delta: p.str("delta")}
				return true
			}
		case "inference.part.ended":
			if p.str("part_kind") == "tool_call" {
				call := p.obj("tool_call")
				args := call["arguments_json"]
				encoded, _ := json.Marshal(args)
				if text, ok := args.(string); ok {
					encoded = []byte(text)
				}
				s.current = &ToolCallEvent{ToolCallID: call.str("tool_call_id"), Name: call.str("name"), ArgumentsJSON: string(encoded)}
				return true
			}
		case "inference.completed":
			response, err := oapResponse(f, s.oapModelRef, "message")
			if err != nil {
				s.fail(err)
				return false
			}
			s.oapResponse = response
			s.current = &MessageEnd{Usage: s.oapResponse.Usage, StopReason: s.oapResponse.StopReason}
			s.done = true
			s.finished = true
			return true
		case "inference.failed", "error":
			s.fail(envelopeFailure(f, s.fallbackProvider))
			return false
		default:
			s.fail(&StreamError{Kind: KindTransportError, Message: fmt.Sprintf("unexpected OAP inference event %q", f.Type)})
			return false
		}
	}
}

type oapAgentState struct {
	ctx       context.Context
	transport *transport
	sub       *subscription
	timeout   time.Duration
	sessionID string
	runID     string
	modelRef  string
	response  *CompletionResponse
	settled   bool
	tools     []Tool
}

func (s *AgentService) OpenSession(ctx context.Context, sessionID string) (string, error) {
	if s.transport == nil || s.transport.legacyWire {
		return "", &ProtocolError{Code: "unsupported_feature", Message: "session.open requires the OAP agent profile"}
	}
	payload := map[string]any{}
	if sessionID != "" {
		payload["session_id"] = sessionID
	}
	request := oapFrame(oapAgent, "session.open.request", payload)
	if sessionID != "" {
		request.SessionID = protocol.SessionID(sessionID)
	}
	sub := s.transport.subscribeStream(string(request.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, request)
	if err != nil {
		return "", err
	}
	if response.Type != "session.open.response" {
		return "", &ProtocolError{Code: CodeMalformedResponse, Message: "expected session.open.response"}
	}
	id := envelopePayload(response).str("session_id")
	if id == "" {
		return "", &ProtocolError{Code: CodeMalformedResponse, Message: "session.open.response omitted session_id"}
	}
	return id, nil
}

type SessionModel struct {
	ID          string
	DisplayName string
	ProviderID  string
	Default     bool
}

func (s *AgentService) ListSessionModels(ctx context.Context, sessionID string) ([]SessionModel, string, error) {
	if s.transport == nil || s.transport.legacyWire {
		return nil, "", &ProtocolError{Code: "unsupported_feature", Message: "agent model discovery requires the OAP agent profile"}
	}
	if sessionID == "" {
		return nil, "", &ProtocolError{Code: CodeInvalidRequest, Message: "session_id is required"}
	}
	request := oapFrame(oapAgent, "models.request", map[string]any{"session_id": sessionID})
	request.SessionID = protocol.SessionID(sessionID)
	sub := s.transport.subscribeStream(string(request.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, request)
	if err != nil {
		return nil, "", err
	}
	if response.Type != "models.response" {
		return nil, "", &ProtocolError{Code: CodeMalformedResponse, Message: "expected models.response"}
	}
	p := envelopePayload(response)
	models := make([]SessionModel, 0, len(p.arr("models")))
	for _, raw := range p.arr("models") {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, "", &ProtocolError{Code: CodeMalformedResponse, Message: "agent model entry is not an object"}
		}
		obj := jsonObject(entry)
		isDefault, _ := obj.boolean("default")
		models = append(models, SessionModel{ID: obj.str("id"), DisplayName: obj.str("display_name"), ProviderID: obj.str("provider_id"), Default: isDefault})
	}
	return models, p.str("current_model_id"), nil
}

type ModelSwitchResult struct {
	SessionID       string
	ModelID         string
	PreviousModelID string
}

type ProviderAttachment struct {
	ID         string
	ProviderID string
	ServiceID  string
}

func (s *AgentService) AttachProvider(ctx context.Context, sessionID string, provider ProviderAttachment) (string, error) {
	if s.transport == nil || s.transport.legacyWire {
		return "", &ProtocolError{Code: "unsupported_feature", Message: "provider attachment requires the OAP agent profile"}
	}
	if sessionID == "" || provider.ID == "" || provider.ProviderID == "" {
		return "", &ProtocolError{Code: CodeInvalidRequest, Message: "session_id, provider.id, and provider.provider_id are required"}
	}
	binding := map[string]any{"id": provider.ID, "provider_id": provider.ProviderID}
	if provider.ServiceID != "" {
		binding["service_id"] = provider.ServiceID
	}
	request := oapFrame(oapAgent, "session.provider.attach.request", map[string]any{"session_id": sessionID, "provider": binding})
	request.SessionID = protocol.SessionID(sessionID)
	sub := s.transport.subscribeStream(string(request.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, request)
	if err != nil {
		return "", err
	}
	if response.Type != "session.provider.attach.response" {
		return "", &ProtocolError{Code: CodeMalformedResponse, Message: "expected session.provider.attach.response"}
	}
	return envelopePayload(response).str("provider_id"), nil
}

func (s *AgentService) SwitchModel(ctx context.Context, sessionID, modelRef string) (*ModelSwitchResult, error) {
	if s.transport == nil || s.transport.legacyWire {
		return nil, &ProtocolError{Code: "unsupported_feature", Message: "mid-session model switching requires the OAP agent profile"}
	}
	if sessionID == "" || modelRef == "" {
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "session_id and model_ref are required"}
	}
	request := oapFrame(oapAgent, "session.model.switch.request", map[string]any{
		"session_id": sessionID, "model_id": modelRef})
	request.SessionID = protocol.SessionID(sessionID)
	sub := s.transport.subscribeStream(string(request.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, request)
	if err != nil {
		return nil, err
	}
	if response.Type != "session.model.switch.response" {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "expected session.model.switch.response"}
	}
	p := envelopePayload(response)
	return &ModelSwitchResult{SessionID: p.str("session_id"), ModelID: p.str("model_id"), PreviousModelID: p.str("previous_model_id")}, nil
}

func (s *AgentService) oapBegin(ctx context.Context, req AgentRequest) (*oapAgentState, error) {
	if req.Options != nil && req.Options.Temperature != nil {
		return nil, &ProtocolError{Code: "unsupported_feature", Message: "the agent loop takes no temperature"}
	}
	if req.ModelRef != "" {
		if err := validateExecutionRequest(req.ModelRef, req.Messages); err != nil {
			return nil, err
		}
	} else if req.Options == nil || req.Options.SessionID == "" {
		return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "model_ref or an existing session_id is required"}
	}
	sessionID := newNanoID()
	if req.Options != nil && req.Options.SessionID != "" {
		sessionID = req.Options.SessionID
	}
	messages, err := oapMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	sub := s.transport.subscribeSession(sessionID)
	cleanup := true
	defer func() {
		if cleanup {
			sub.close()
		}
	}()
	opening, err := oapOpenPayload(sessionID, req)
	if err != nil {
		return nil, err
	}
	open := oapFrame(oapAgent, "session.open.request", opening)
	open.SessionID = protocol.SessionID(sessionID)
	opened, err := oapRequest(ctx, s.transport, sub, s.timeout, open)
	if err != nil {
		return nil, err
	}
	if opened.Type != "session.open.response" {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "expected session.open.response"}
	}
	payload := map[string]any{"session_id": sessionID, "messages": messages, "delivery": "auto"}
	if req.Options != nil && len(req.Options.Metadata) > 0 {
		payload["metadata"] = req.Options.Metadata
	}
	if req.ModelRef != "" {
		payload["model_id"] = req.ModelRef
	}
	submit := oapFrame(oapAgent, "session.message.submit.request", payload)
	submit.SessionID = protocol.SessionID(sessionID)
	admission, err := oapRequest(ctx, s.transport, sub, s.timeout, submit)
	if err != nil {
		return nil, err
	}
	if admission.Type != "session.message.submit.response" {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "expected session.message.submit.response"}
	}
	runID := envelopePayload(admission).str("run_id")
	if runID == "" {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "admission omitted run_id"}
	}
	cleanup = false
	selectedModelRef := req.ModelRef
	if selectedModelRef == "" {
		selectedModelRef = envelopePayload(admission).str("model_id")
	}
	return &oapAgentState{ctx: ctx, transport: s.transport, sub: sub, timeout: s.timeout,
		sessionID: sessionID, runID: runID, modelRef: selectedModelRef, tools: req.Tools}, nil
}

func oapOpenPayload(sessionID string, req AgentRequest) (map[string]any, error) {
	payload := map[string]any{"session_id": sessionID}
	if len(req.Tools) > 0 {
		provided, err := oapTools(req.Tools)
		if err != nil {
			return nil, err
		}
		for _, tool := range provided {
			tool["execution_owner"] = sdkParticipant
		}
		payload["tools"] = provided
	}
	if req.Options == nil {
		return payload, nil
	}
	if req.Options.ReasoningEffort != "" {
		payload["reasoning_level"] = string(req.Options.ReasoningEffort)
	}
	if req.Options.MaxTokens != nil {
		payload["metadata"] = map[string]any{"oapx": map[string]any{"output": *req.Options.MaxTokens}}
	}
	return payload, nil
}

func (state *oapAgentState) resolveCall(p jsonObject) error {
	invocation := ToolInvocation{ToolCallID: p.str("tool_call_id"), ToolName: p.str("name"), ArgumentsJSON: "{}"}
	if arguments, present := p["arguments_json"]; present && arguments != nil {
		if text, isText := arguments.(string); isText {
			invocation.ArgumentsJSON = text
		} else {
			invocation.ArgumentsJSON = string(mustMarshal(arguments))
		}
	}
	answer := map[string]any{
		"interaction_id": p.str("interaction_id"),
		"session_id":     state.sessionID,
		"run_id":         state.runID,
		"tool_call_id":   invocation.ToolCallID,
		"requested_by":   p.str("requested_by"),
		"responded_by":   sdkParticipant,
	}
	tool := findTool(state.tools, invocation.ToolName)
	switch {
	case tool == nil || tool.Execute == nil:
		answer["error"] = map[string]any{"code": "tool_unavailable", "message": fmt.Sprintf("Tool %q is not executable by this client", invocation.ToolName)}
	default:
		result, err := tool.Execute(state.ctx, invocation)
		if err != nil {
			answer["error"] = map[string]any{"code": "tool_failed", "message": err.Error()}
		} else {
			answer["result"] = result
		}
	}
	resolve := oapFrame(oapAgent, "action.call.resolve.request", answer)
	resolve.SessionID = protocol.SessionID(state.sessionID)
	resolve.RunID = protocol.RunID(state.runID)
	return state.transport.sendEnvelope(resolve)
}

func (s *AgentService) oapRun(ctx context.Context, req AgentRequest) (*CompletionResponse, error) {
	stream, err := s.oapStream(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for stream.Next() {
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	if stream.oapState == nil || stream.oapState.response == nil {
		return nil, &StreamError{Kind: KindTransportError, Message: "agent run ended without completion"}
	}
	return stream.oapState.response, nil
}

func (s *AgentService) oapStream(ctx context.Context, req AgentRequest) (*AgentStream, error) {
	state, err := s.oapBegin(ctx, req)
	if err != nil {
		return nil, err
	}
	return &AgentStream{oapState: state}, nil
}

func (s *AgentStream) oapNext() bool {
	if s.done || s.oapState == nil {
		return false
	}
	state := s.oapState
	for {
		f, err := state.sub.next(state.ctx, state.timeout, "OAP agent run")
		if err != nil {
			s.fail(err)
			return false
		}
		if f.RunID != "" && f.RunID != state.runID {
			continue
		}
		p := f.payload()
		switch f.Type {
		case "run.started":
			if modelRef := p.str("model_id"); modelRef != "" {
				state.modelRef = modelRef
			}
			s.current = &AgentStart{SessionID: state.sessionID}
			return true
		case "content.delta":
			part := p.obj("part")
			switch part.str("type") {
			case "text":
				s.current = &TextDelta{Delta: part.str("text")}
				return true
			case "reasoning":
				s.current = &ThinkingDelta{Delta: part.str("reasoning")}
				return true
			}
		case "run.completed":
			modelRef := state.modelRef
			if modelRef == "" {
				modelRef = p.str("model_id")
			}
			response, err := oapResponse(f, modelRef, "final_response")
			if err != nil {
				state.settled = true
				s.fail(err)
				return false
			}
			state.response = response
			state.settled = true
			s.current = agentEndFromResponse(state.response)
			s.done = true
			return true
		case "run.failed", "run.cancelled", "error.response":
			state.settled = true
			s.fail(envelopeFailure(f, providerIDFromRef(state.modelRef)))
			return false
		case "action.call.requested":
			if p.str("execution_owner") != sdkParticipant {
				continue
			}
			if err := state.resolveCall(p); err != nil {
				s.fail(err)
				return false
			}
			continue
		case "action.call.started":
			s.current = &ToolExecutionStart{ToolCallID: p.str("tool_call_id"), ToolName: p.str("name")}
			return true
		case "action.call.completed", "action.call.failed", "action.call.cancelled":
			s.current = &ToolExecutionEnd{ToolCallID: p.str("tool_call_id"), IsError: f.Type != "action.call.completed"}
			return true
		case "action.call.resolve.response":
			if accepted, _ := p["accepted"].(bool); !accepted && p.str("reason") != "already_resolved" {
				state.settled = true
				s.fail(&ProtocolError{Code: CodeMalformedResponse, Message: "the endpoint refused a tool result: " + p.str("reason")})
				return false
			}
			continue
		case "session.state.updated", "run.status.updated":
			continue
		default:
			s.fail(&StreamError{Kind: KindTransportError, Message: fmt.Sprintf("unexpected OAP agent event %q", f.Type)})
			return false
		}
	}
}

func (s *AgentStream) oapClose() error {
	state := s.oapState
	if state == nil {
		return s.err
	}
	if !state.settled {
		cancel := oapFrame(oapAgent, "run.cancel.request", map[string]any{
			"session_id": state.sessionID, "run_id": state.runID, "reason": "caller_closed"})
		cancel.SessionID = protocol.SessionID(state.sessionID)
		cancel.RunID = protocol.RunID(state.runID)
		state.transport.sendEnvelopeBestEffort(cancel)
	}
	state.sub.close()
	s.oapState = nil
	s.done = true
	return s.err
}

func oapModelLifecycle(model jsonObject) (*ModelLifecycle, error) {
	raw, stated := model["lifecycle"]
	if !stated {
		return nil, nil
	}
	if raw == nil {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "model lifecycle must be a string when present, not null"}
	}
	name, ok := raw.(string)
	if !ok || name == "" {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "model lifecycle must be a non-empty string when present"}
	}
	switch ModelLifecycle(name) {
	case LifecycleStable, LifecyclePreview, LifecycleDeprecated:
		mapped := ModelLifecycle(name)
		return &mapped, nil
	default:
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "model lifecycle has unknown value: " + name}
	}
}

func oapModelSource(model jsonObject) (*ModelSource, error) {
	raw, stated := model["source"]
	if !stated {
		return nil, nil
	}
	name, ok := raw.(string)
	if !ok || name == "" {
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "model source must be a non-empty string when present"}
	}
	var mapped ModelSource
	switch name {
	case "discovered":
		mapped = SourceDynamic
	case "fallback":
		mapped = SourceStaticFallback
	default:
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "model source has unknown value: " + name}
	}
	return &mapped, nil
}

func oapModelAuth(model jsonObject) (AuthStatus, error) {
	held, present := model["auth_status"]
	if !present {
		return AuthUnknown, nil
	}
	text, ok := held.(string)
	if !ok {
		return "", &ProtocolError{Code: CodeMalformedResponse, Message: "auth_status is not a string"}
	}
	switch AuthStatus(text) {
	case AuthAuthenticated, AuthLoginRequired, AuthExpired, AuthRefreshing, AuthLoginInProgress, AuthFailed, AuthUnknown:
		return AuthStatus(text), nil
	}
	return "", &ProtocolError{Code: CodeMalformedResponse, Message: "auth_status is outside the enum"}
}

func (s *AuthService) oapListProviders(ctx context.Context) ([]ProviderAuthInfo, error) {
	request := oapFrame(oapAgent, "auth.providers.request", map[string]any{})
	sub := s.transport.subscribeStream(string(request.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, request)
	if err != nil {
		if failure, ok := err.(*StreamError); ok {
			return nil, &AuthError{Kind: AuthKindProviderError, Code: failure.Code, Message: failure.Message}
		}
		return nil, authErrorFrom(err, "", "")
	}
	if response.Type != "auth.providers.response" {
		return nil, &AuthError{Kind: AuthKindTransportError, Message: "expected auth.providers.response"}
	}
	result := make([]ProviderAuthInfo, 0, len(envelopePayload(response).arr("providers")))
	for _, raw := range envelopePayload(response).arr("providers") {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, &AuthError{Kind: AuthKindTransportError, Message: "auth provider entry is not an object"}
		}
		p := jsonObject(entry)
		raw := make([]protocol.CredentialKind, 0, len(p.arr("auth_kinds")))
		for _, kind := range p.arr("auth_kinds") {
			if name, ok := kind.(string); ok {
				raw = append(raw, protocol.CredentialKind(name))
			}
		}
		result = append(result, ProviderAuthInfo{ID: p.str("id"), Name: p.str("name"), AuthKinds: knownCredentialKinds(raw), Status: AuthStatus(p.str("auth_status")), LastError: p.str("last_error"), OverrideHost: p.str("override_host")})
	}
	return result, nil
}

func (s *AuthService) oapCancelFlow(flowID string) {
	if flowID == "" {
		return
	}
	s.transport.sendEnvelopeBestEffort(oapFrame(oapAgent, "auth.login.cancel.request", map[string]any{"flow_id": flowID}))
}

func (s *AuthService) oapLogin(ctx context.Context, providerID string, handlers LoginHandlers) error {
	if providerID == "" {
		return &AuthError{Kind: AuthKindProviderError, Code: CodeInvalidRequest, Message: "login requires a provider id"}
	}
	start := oapFrame(oapAgent, "auth.login.start.request", map[string]any{"provider_id": providerID})
	sub := s.transport.subscribeStream(string(start.ID))
	defer sub.close()
	response, err := oapRequest(ctx, s.transport, sub, s.timeout, start)
	if err != nil {
		if failure, ok := err.(*StreamError); ok {
			return &AuthError{Kind: AuthKindProviderError, Code: failure.Code, Message: failure.Message, ProviderID: providerID}
		}
		return authErrorFrom(err, providerID, "")
	}
	if response.Type != "auth.login.start.response" {
		return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, Message: "expected auth.login.start.response"}
	}
	flowID := envelopePayload(response).str("flow_id")
	if flowID == "" {
		return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, Message: "auth login start omitted flow_id"}
	}
	settled := false
	defer func() {
		if !settled {
			s.oapCancelFlow(flowID)
		}
	}()
	nextSequence := int64(1)
	for {
		f, err := sub.next(ctx, s.timeout, "OAP auth login")
		if err != nil {
			if ctx.Err() != nil {
				return &AuthError{Kind: AuthKindCancelled, ProviderID: providerID, FlowID: flowID, Message: "auth login aborted", err: ctx.Err()}
			}
			return authErrorFrom(err, providerID, flowID)
		}
		if f.Type != "auth.login.event" && f.Type != "auth.login.completed" {
			return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, FlowID: flowID, Message: fmt.Sprintf("unexpected OAP auth flow event %q", f.Type)}
		}
		if f.Sequence != nextSequence {
			return &AuthError{Kind: AuthKindTransportError, Code: "protocol_violation", ProviderID: providerID, FlowID: flowID, Message: "auth flow sequence gap"}
		}
		nextSequence++
		p := f.payload()
		if p.str("flow_id") != flowID || p.str("provider_id") != providerID {
			return &AuthError{Kind: AuthKindTransportError, Code: "protocol_violation", ProviderID: providerID, FlowID: flowID, Message: "auth flow identity changed"}
		}
		if f.Type == "auth.login.completed" {
			settled = true
			switch p.str("status") {
			case "success":
				if handlers.OnEvent != nil {
					handlers.OnEvent(AuthEvent{Type: AuthEventSuccess, FlowID: flowID, ProviderID: providerID})
				}
				return nil
			case "cancelled":
				failure := p.obj("error")
				message := failure.strOrDefault("auth login cancelled", "message")
				return &AuthError{Kind: AuthKindCancelled, Code: failure.str("code"), Message: message, ProviderID: providerID, FlowID: flowID}
			case "failed":
				failure := p.obj("error")
				return &AuthError{Kind: AuthKindProviderError, Code: failure.str("code"), Message: failure.strOrDefault("auth login failed", "message"), ProviderID: providerID, FlowID: flowID}
			default:
				return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, FlowID: flowID, Message: "unknown auth login terminal status"}
			}
		}
		event := AuthEvent{FlowID: flowID, ProviderID: providerID}
		switch p.str("kind") {
		case "url":
			event.Type = AuthEventURL
			event.URL = p.str("url")
			event.Instructions = p.str("instructions")
		case "progress":
			event.Type = AuthEventProgress
			event.Message = p.str("message")
		case "prompt":
			return &AuthError{Kind: AuthKindProviderError, Code: "auth_input_unavailable", ProviderID: providerID, FlowID: flowID, Message: "manual login input cannot be sent over OAP"}
		default:
			return &AuthError{Kind: AuthKindTransportError, ProviderID: providerID, FlowID: flowID, Message: "unknown OAP auth event kind"}
		}
		if handlers.OnEvent != nil {
			handlers.OnEvent(event)
		}
	}
}
