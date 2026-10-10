package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
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

func providerFrame(kind string, payload any) protocol.ProviderEnvelope {
	return protocol.ProviderEnvelope{Protocol: oapProtocol, Version: oapVersion, Profile: oapProvider,
		Type: protocol.EnvelopeType(kind), ID: protocol.EnvelopeID(newULID()), Payload: mustMarshal(payload)}
}

func oapExchange(ctx context.Context, sub *subscription, timeout time.Duration, id protocol.EnvelopeID, operation string, send func() error) (*inbound, error) {
	sub.correlate(string(id))
	defer sub.uncorrelate(string(id))
	if err := send(); err != nil {
		return nil, err
	}
	for {
		answer, err := sub.next(ctx, timeout, operation)
		if err != nil {
			return nil, err
		}
		if answer.replyTo() != string(id) {
			continue
		}
		if answer.broken != nil {
			return nil, answer.broken
		}
		if kind := answer.kind(); kind == "error" || kind == "error.response" {
			return nil, answer.failure("")
		}
		return answer, nil
	}
}

func oapRequest(ctx context.Context, t *transport, sub *subscription, timeout time.Duration, request protocol.Envelope) (protocol.Envelope, error) {
	answer, err := oapExchange(ctx, sub, timeout, request.ID, string(request.Type), func() error { return t.sendEnvelope(request) })
	if err != nil {
		return protocol.Envelope{}, err
	}
	if answer.agent == nil {
		return protocol.Envelope{}, &ProtocolError{Code: CodeMalformedResponse, Message: fmt.Sprintf("%s was answered outside the agent profile", request.Type)}
	}
	return *answer.agent, nil
}

func providerRequest(ctx context.Context, t *transport, sub *subscription, timeout time.Duration, request protocol.ProviderEnvelope) (protocol.ProviderEnvelope, error) {
	answer, err := oapExchange(ctx, sub, timeout, request.ID, string(request.Type), func() error { return t.sendProviderEnvelope(request) })
	if err != nil {
		return protocol.ProviderEnvelope{}, err
	}
	if answer.provider == nil {
		return protocol.ProviderEnvelope{}, &ProtocolError{Code: CodeMalformedResponse, Message: fmt.Sprintf("%s was answered outside the provider profile", request.Type)}
	}
	return *answer.provider, nil
}

func envelopePayload(env protocol.Envelope) jsonObject {
	return payloadObject(env.Payload)
}

func payloadObject(raw json.RawMessage) jsonObject {
	payload, _ := decodeObject(raw)
	if payload == nil {
		return jsonObject{}
	}
	return payload
}

func oapFailure(payload jsonObject, providerID string) error {
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

func oapResponse(payload jsonObject, modelRef, messageKey string) (*CompletionResponse, error) {
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
	permit    PermissionHandler
	callNames map[string]string
}

func (s *AgentService) OpenSession(ctx context.Context, sessionID string) (string, error) {
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
	opening, err := oapOpenPayload(s.transport, sessionID, req)
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
		sessionID: sessionID, runID: runID, modelRef: selectedModelRef, tools: req.Tools, permit: req.Permit, callNames: map[string]string{}}, nil
}

const oapxAgentEndpoint = "oapx.agent"

func declaredToolSource(sources []any) string {
	first := ""
	for _, entry := range sources {
		source, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		id, _ := source["id"].(string)
		if id == "" {
			continue
		}
		if source["kind"] == "native" {
			return id
		}
		if first == "" {
			first = id
		}
	}
	return first
}

func oapOpenPayload(t *transport, sessionID string, req AgentRequest) (map[string]any, error) {
	payload := map[string]any{"session_id": sessionID}
	if len(req.Tools) > 0 && !t.agentFeatures["action.tools.provide"] {
		return nil, &ProtocolError{Code: "unsupported_feature", Message: "this endpoint does not advertise action.tools.provide, so it cannot run client tools"}
	}
	if len(req.Tools) > 0 {
		provided, err := oapTools(req.Tools)
		if err != nil {
			return nil, err
		}
		for _, tool := range provided {
			tool["execution_owner"] = sdkParticipant
			if t.agentSource != "" {
				tool["source"] = t.agentSource
			}
		}
		payload["tools"] = provided
	}
	settings := map[string]any{"user_input": false}
	if req.Permit != nil {
		settings["permission_mode"] = "ask"
	}
	if t.agentEndpoint == oapxAgentEndpoint {
		payload["metadata"] = map[string]any{"oapx": settings}
	}
	if req.Options == nil {
		return payload, nil
	}
	if req.Options.ReasoningEffort == ReasoningMinimal {
		return nil, &ProtocolError{Code: "unsupported_feature", Message: "the agent loop runs minimal reasoning as low, so it refuses minimal"}
	}
	if req.Options.ReasoningEffort != "" {
		payload["reasoning_level"] = string(req.Options.ReasoningEffort)
	}
	if req.Options.MaxTokens != nil {
		if *req.Options.MaxTokens < 1 || int64(*req.Options.MaxTokens) > math.MaxUint32 {
			return nil, &ProtocolError{Code: CodeInvalidRequest, Message: "MaxTokens must be between 1 and 4294967295"}
		}
		if t.agentEndpoint != oapxAgentEndpoint {
			return nil, &ProtocolError{Code: "unsupported_feature", Message: "only the oapx agent loop takes an output limit at open"}
		}
		settings["output"] = *req.Options.MaxTokens
	}
	return payload, nil
}

var (
	grantChoiceIDs = []string{"approve", "accept", "once", "allow_once", "allow"}
	denyChoiceIDs  = []string{"deny", "decline", "reject", "reject_once"}
)

func (state *oapAgentState) resolvePermission(p jsonObject) error {
	request := PermissionRequest{ToolCallID: p.str("tool_call_id"), Title: p.str("title"), Description: p.str("description")}
	request.ToolName = state.callNames[request.ToolCallID]
	if arguments, present := p["arguments_json"]; present && arguments != nil {
		if text, isText := arguments.(string); isText {
			request.ArgumentsJSON = text
		} else {
			request.ArgumentsJSON = string(mustMarshal(arguments))
		}
	}
	granted := state.permit != nil && state.permit(state.ctx, request)
	vocabulary := denyChoiceIDs
	if granted {
		vocabulary = grantChoiceIDs
	}
	offered := map[string]bool{}
	choices, _ := p["choices"].([]any)
	for _, raw := range choices {
		if choice, isObject := raw.(map[string]any); isObject {
			if id, isText := choice["id"].(string); isText {
				offered[id] = true
			}
		}
	}
	wanted := ""
	for _, id := range vocabulary {
		if offered[id] {
			wanted = id
			break
		}
	}
	if wanted == "" {
		return &ProtocolError{Code: CodeMalformedResponse, Message: fmt.Sprintf("the endpoint's permission request offers none of %v", vocabulary)}
	}
	answer := map[string]any{
		"interaction_id": p.str("interaction_id"),
		"session_id":     state.sessionID,
		"run_id":         state.runID,
		"requested_by":   p.str("requested_by"),
		"responded_by":   sdkParticipant,
		"choice_id":      wanted,
		"granted":        granted,
	}
	resolve := oapFrame(oapAgent, "action.permission.resolve.request", answer)
	resolve.SessionID = protocol.SessionID(state.sessionID)
	resolve.RunID = protocol.RunID(state.runID)
	return state.transport.sendEnvelope(resolve)
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
			message := err.Error()
			if message == "" {
				message = fmt.Sprintf("%T", err)
			}
			answer["error"] = map[string]any{"code": "tool_failed", "message": message}
		} else {
			answer["result"] = result
		}
	}
	resolve := oapFrame(oapAgent, "action.call.resolve.request", answer)
	resolve.SessionID = protocol.SessionID(state.sessionID)
	resolve.RunID = protocol.RunID(state.runID)
	return state.transport.sendEnvelope(resolve)
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

func (s *AuthService) oapCancelFlow(flowID string) {
	if flowID == "" {
		return
	}
	s.transport.sendEnvelopeBestEffort(oapFrame(oapAgent, "auth.login.cancel.request", map[string]any{"flow_id": flowID}))
}
