package provider

import (
	"encoding/json"
	"strconv"
	"strings"
)

type jsonValue interface{ encode() string }

type jsonMember struct {
	name  string
	value jsonValue
}

type jsonObject []jsonMember

type jsonArray []jsonValue

type jsonString string

type jsonBool bool

type jsonInt int

type jsonFloat float64

type jsonNull struct{}

type jsonRaw string

func (o jsonObject) encode() string {
	parts := make([]string, 0, len(o))
	for _, m := range o {
		parts = append(parts, encodeString(m.name)+":"+m.value.encode())
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func (a jsonArray) encode() string {
	parts := make([]string, 0, len(a))
	for _, v := range a {
		parts = append(parts, v.encode())
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (s jsonString) encode() string { return encodeString(string(s)) }
func (b jsonBool) encode() string {
	if b {
		return "true"
	}
	return "false"
}
func (i jsonInt) encode() string  { return strconv.Itoa(int(i)) }
func (n jsonNull) encode() string { return "null" }
func (r jsonRaw) encode() string  { return string(r) }

func (f jsonFloat) encode() string {
	encoded, err := json.Marshal(float64(f))
	if err != nil {
		return "0"
	}
	return string(encoded)
}

func encodeString(s string) string {
	encoded, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func (o jsonObject) get(name string) (jsonValue, bool) {
	for _, m := range o {
		if m.name == name {
			return m.value, true
		}
	}
	return nil, false
}

func (o jsonObject) with(members ...jsonMember) jsonObject {
	return append(o, members...)
}

func member(name string, value jsonValue) jsonMember {
	return jsonMember{name: name, value: value}
}

func textPart(text string) jsonObject {
	return jsonObject{member("type", jsonString("text")), member("text", jsonString(text))}
}

func cachedTextPart(text string) jsonObject {
	return textPart(text).with(member("cache_control", jsonRaw(`{"type":"ephemeral"}`)))
}

func imageUrlPart(img *ImagePart) jsonObject {
	return jsonObject{
		member("type", jsonString("image_url")),
		member("image_url", jsonObject{member("url", jsonString(img.DataURL()))}),
	}
}

func visibleTexts(parts []ContentPart) []string {
	texts := []string{}
	for _, part := range parts {
		if partIsVisibleText(part) {
			texts = append(texts, part.Text.Text)
		}
	}
	return texts
}

func visibleThinkings(parts []ContentPart) []string {
	texts := []string{}
	for _, part := range parts {
		if partIsVisibleThinking(part) {
			texts = append(texts, part.Thinking.Thinking)
		}
	}
	return texts
}

func hasImage(parts []ContentPart) bool {
	for _, part := range parts {
		if part.Image != nil {
			return true
		}
	}
	return false
}

func reasoningFieldName(parts []ContentPart) string {
	for _, part := range parts {
		if part.Thinking == nil {
			continue
		}
		if part.Thinking.Signature != "" {
			return part.Thinking.Signature
		}
		break
	}
	return "reasoning_content"
}

type assistantShape struct {
	hasText     bool
	hasThinking bool
	hasToolCall bool
}

func inspectAssistant(parts []ContentPart) assistantShape {
	shape := assistantShape{}
	for _, part := range parts {
		switch {
		case partIsVisibleText(part):
			shape.hasText = true
		case partIsVisibleThinking(part):
			shape.hasThinking = true
		case part.ToolCall != nil:
			shape.hasToolCall = true
		}
	}
	return shape
}

func assistantContentValue(a *AssistantContent, model Model, merged MergedCompat, shape assistantShape) jsonValue {
	isCopilot := model.Provider == "github-copilot"
	switch {
	case shape.hasText && isCopilot:
		return jsonString(strings.Join(visibleTexts(a.Parts), ""))
	case shape.hasText:
		texts := visibleTexts(a.Parts)
		if shape.hasThinking && merged.RequiresThinkingAsText {
			texts = append(visibleThinkings(a.Parts), texts...)
		}
		return textPartArray(texts)
	case merged.RequiresThinkingAsText && shape.hasThinking:
		return textPartArray(visibleThinkings(a.Parts))
	case merged.RequiresThinkingAsText:
		return jsonString("")
	default:
		return jsonNull{}
	}
}

func textPartArray(texts []string) jsonArray {
	out := jsonArray{}
	for _, text := range texts {
		out = append(out, textPart(text))
	}
	return out
}

func toolCallValue(tc *ToolCall) jsonObject {
	return jsonObject{
		member("id", jsonString(tc.ID)),
		member("type", jsonString("function")),
		member("function", jsonObject{
			member("name", jsonString(tc.Name)),
			member("arguments", jsonString(tc.Arguments)),
		}),
	}
}

func assistantMessage(a *AssistantContent, model Model, merged MergedCompat) (jsonObject, bool) {
	shape := inspectAssistant(a.Parts)
	if !shape.hasText && !shape.hasThinking && !shape.hasToolCall {
		return nil, false
	}
	out := jsonObject{
		member("role", jsonString(string(RoleAssistant))),
		member("content", assistantContentValue(a, model, merged, shape)),
	}

	if shape.hasThinking && !merged.RequiresThinkingAsText {
		thinking := visibleThinkings(a.Parts)
		if len(thinking) > 0 {
			out = out.with(member(reasoningFieldName(a.Parts), jsonString(strings.Join(thinking, "\n"))))
		}
	}

	if shape.hasToolCall {
		calls := jsonArray{}
		var details jsonArray
		hasDetails := false
		for _, part := range a.Parts {
			if part.ToolCall == nil {
				continue
			}
			calls = append(calls, toolCallValue(part.ToolCall))
			if part.ToolCall.HasThought {
				hasDetails = true
				if part.ToolCall.ThoughtSig != "" {
					details = append(details, jsonRaw(part.ToolCall.ThoughtSig))
				}
			}
		}
		out = out.with(member("tool_calls", calls))
		if hasDetails {
			out = out.with(member("reasoning_details", details))
		}
	}
	return out, true
}

func emptyAssistant() jsonObject {
	return jsonObject{member("role", jsonString(string(RoleAssistant))), member("content", jsonString(""))}
}

func toolResultRun(results []*ToolResult, merged MergedCompat, prevRole string) (jsonArray, string) {
	out := jsonArray{}
	var imageBlocks []*ImagePart
	for _, tr := range results {
		if merged.RequiresAssistantAfterToolResult && prevRole == string(RoleTool) {
			out = append(out, emptyAssistant())
		}
		texts := []string{}
		for _, part := range tr.Parts {
			if part.Text != nil {
				texts = append(texts, part.Text.Text)
			}
		}
		hasImages := hasImage(tr.Parts)

		msg := jsonObject{
			member("role", jsonString(string(RoleTool))),
			member("tool_call_id", jsonString(tr.ToolCallID)),
		}
		if merged.RequiresToolResultName {
			msg = msg.with(member("name", jsonString(tr.ToolName)))
		}
		content := strings.Join(texts, "\n")
		if content == "" && hasImages {
			content = "(see attached image)"
		}
		out = append(out, msg.with(member("content", jsonString(content))))
		prevRole = string(RoleTool)

		for _, part := range tr.Parts {
			if part.Image != nil {
				imageBlocks = append(imageBlocks, part.Image)
			}
		}
	}

	if len(imageBlocks) == 0 {
		return out, prevRole
	}
	if merged.RequiresAssistantAfterToolResult {
		out = append(out, emptyAssistant())
	}
	parts := jsonArray{}
	for _, img := range imageBlocks {
		parts = append(parts, imageUrlPart(img))
	}
	out = append(out, jsonObject{
		member("role", jsonString(string(RoleUser))),
		member("content", parts),
	})
	return out, string(RoleUser)
}

func userMessage(u *UserContent, isLast bool) jsonObject {
	out := jsonObject{member("role", jsonString(string(RoleUser)))}
	if !u.UseParts {
		if isLast {
			return out.with(member("content", jsonArray{cachedTextPart(u.Text)}))
		}
		return out.with(member("content", jsonString(u.Text)))
	}
	if !hasImage(u.Parts) && !isLast {
		texts := []string{}
		for _, part := range u.Parts {
			if part.Text != nil {
				texts = append(texts, part.Text.Text)
			}
		}
		return out.with(member("content", jsonString(strings.Join(texts, "\n"))))
	}
	parts := jsonArray{}
	for idx, part := range u.Parts {
		switch {
		case part.Text != nil:
			if isLast && idx == len(u.Parts)-1 {
				parts = append(parts, cachedTextPart(part.Text.Text))
			} else {
				parts = append(parts, textPart(part.Text.Text))
			}
		case part.Image != nil:
			parts = append(parts, imageUrlPart(part.Image))
		}
	}
	return out.with(member("content", parts))
}

func messagesValue(ctx Context, model Model, merged MergedCompat) jsonArray {
	shouldCache := IsOpenRouterAnthropic(model)
	lastUserIdx := -1
	if shouldCache {
		for i, msg := range ctx.Messages {
			if msg.User != nil {
				lastUserIdx = i
			}
		}
	}
	toolCallIDs := collectToolCallIDs(ctx.Messages)

	out := jsonArray{}
	if ctx.HasSystem {
		role := RoleSystem
		if model.Reasoning && merged.SupportsDeveloperRole {
			role = RoleDeveloper
		}
		out = append(out, jsonObject{
			member("role", jsonString(string(role))),
			member("content", jsonString(ctx.SystemPrompt)),
		})
	}

	prevRole := ""
	for i := 0; i < len(ctx.Messages); {
		msg := ctx.Messages[i]
		switch {
		case shouldSkipAssistant(msg):
			i++
			continue
		case isOrphanedToolResult(msg, toolCallIDs):
			i++
			continue
		}

		switch {
		case msg.User != nil:
			out = append(out, userMessage(msg.User, i == lastUserIdx))
			prevRole = string(RoleUser)
			i++
		case msg.Assistant != nil:
			if written, ok := assistantMessage(msg.Assistant, model, merged); ok {
				out = append(out, written)
				prevRole = string(RoleAssistant)
			}
			i++
		case msg.ToolResult != nil:
			run := []*ToolResult{}
			for i < len(ctx.Messages) && ctx.Messages[i].ToolResult != nil {
				if !isOrphanedToolResult(ctx.Messages[i], toolCallIDs) {
					run = append(run, ctx.Messages[i].ToolResult)
				}
				i++
			}
			written, after := toolResultRun(run, merged, prevRole)
			out = append(out, written...)
			prevRole = after
		default:
			i++
		}
	}
	return out
}

func IsKimiModel(model Model) bool { return model.Provider == "kimi" }

func maxToolIDLen(model Model) int {
	if IsOpenAIHost(model.BaseURL, model.HasBaseURL) || IsTransparentOpenAIProxy(model) {
		return 40
	}
	return 0
}

func BuildRequestBody(model Model, ctx Context, options StreamOptions) []byte {
	merged := MergeCompat(model)
	transformed := PreTransform(ctx.Messages, TransformConfig{
		TargetAPI:             model.API,
		TargetProvider:        model.Provider,
		TargetModelID:         model.ID,
		MaxToolIDLen:          maxToolIDLen(model),
		MistralToolIDs:        merged.RequiresMistralToolIDs,
		InsertSyntheticResult: true,
		Tools:                 ctx.Tools,
	})
	prepared := ctx
	prepared.Messages = transformed
	body := jsonObject{
		member("model", jsonString(model.ID)),
		member("messages", messagesValue(prepared, model, merged)),
		member("stream", jsonBool(true)),
	}
	if merged.SupportsUsageInStreaming {
		body = body.with(member("stream_options", jsonObject{member("include_usage", jsonBool(true))}))
	}
	maxTokens := model.MaxTokens
	if options.HasMaxTokens {
		maxTokens = options.MaxTokens
	}
	body = body.with(member(merged.MaxTokensField, jsonInt(maxTokens)))
	if options.HasTemperature {
		if !IsKimiModel(model) || options.Temperature == 1.0 {
			body = body.with(member("temperature", jsonFloat(options.Temperature)))
		}
	}
	if options.ReasoningEffort != "" {
		if model.Reasoning && merged.SupportsReasoningEffort {
			effort := options.ReasoningEffort
			if IsDeepSeekModel(model) {
				effort = deepSeekEffort(effort)
			}
			body = body.with(member("reasoning_effort", jsonString(effort)))
		}
	}
	if len(ctx.Tools) > 0 {
		body = body.with(member("tools", toolsValue(ctx.Tools, merged)))
		if options.HasToolChoice {
			if value, ok := toolChoiceValue(options.ToolChoice); ok {
				body = body.with(member("tool_choice", value))
			}
		}
	}
	if merged.SupportsStore {
		body = body.with(member("store", jsonBool(false)))
	}
	return []byte(body.encode())
}

func toolsValue(tools []Tool, merged MergedCompat) jsonArray {
	out := jsonArray{}
	for _, tool := range tools {
		fn := jsonObject{
			member("name", jsonString(tool.Name)),
			member("description", jsonString(tool.Description)),
		}
		params := strings.TrimSpace(string(tool.Parameters))
		if params == "" {
			params = "{}"
		}
		fn = fn.with(member("parameters", jsonRaw(params)))
		if merged.SupportsStrictMode {
			fn = fn.with(member("strict", jsonBool(true)))
		}
		out = append(out, jsonObject{
			member("type", jsonString("function")),
			member("function", fn),
		})
	}
	return out
}

func toolChoiceValue(choice ToolChoice) (jsonValue, bool) {
	switch choice.Mode {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired:
		return jsonString(choice.Mode), true
	case "function":
		return jsonObject{
			member("type", jsonString("function")),
			member("function", jsonObject{member("name", jsonString(choice.Function))}),
		}, true
	}
	return nil, false
}

func deepSeekEffort(effort string) string {
	switch effort {
	case "minimal", "low":
		return "low"
	case "max", "ultra":
		return "max"
	}
	return "high"
}
