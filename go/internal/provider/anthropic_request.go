package provider

import (
	"encoding/json"
	"net/url"
	"strings"
)

const (
	AnthropicWire = "anthropic-messages"

	betaFlagsPlain = "fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14"
	betaFlagsOAuth = "claude-code-20250219,oauth-2025-04-20,fine-grained-tool-streaming-2025-05-14,interleaved-thinking-2025-05-14"

	oauthUserAgent  = "claude-cli/2.1.2 (external, cli)"
	oauthSystemText = "You are Claude Code, Anthropic's official CLI for Claude."
)

type Header struct {
	Name  string
	Value string
}

func IsOAuthToken(key string) bool { return strings.Contains(key, "sk-ant-oat") }

func IsAnthropicHost(baseURL string, hasBaseURL bool) bool {
	if !hasBaseURL {
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "api.anthropic.com")
}

func BuildAnthropicHeaders(apiKey string, modelHeaders []Header) []Header {
	out := []Header{}
	switch {
	case apiKey == "":
		out = append(out, Header{"anthropic-beta", betaFlagsPlain})
	case IsOAuthToken(apiKey):
		out = append(out,
			Header{"authorization", BearerValue(apiKey)},
			Header{"anthropic-beta", betaFlagsOAuth},
			Header{"anthropic-dangerous-direct-browser-access", "true"},
			Header{"user-agent", oauthUserAgent},
			Header{"x-app", "cli"},
		)
	default:
		out = append(out,
			Header{"x-api-key", apiKey},
			Header{"anthropic-beta", betaFlagsPlain},
		)
	}
	out = append(out,
		Header{"anthropic-version", "2023-06-01"},
		Header{"content-type", "application/json"},
	)
	for _, extra := range modelHeaders {
		if headerPresent(out, extra.Name) {
			continue
		}
		out = append(out, extra)
	}
	return out
}

func headerPresent(headers []Header, name string) bool {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return true
		}
	}
	return false
}

func supportsAdaptiveThinking(modelID string) bool {
	return strings.Contains(modelID, "opus-4-6") || strings.Contains(modelID, "opus-4.6")
}

func mapThinkingLevelToEffort(level string) string {
	switch level {
	case "off", "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh":
		return "max"
	}
	return "low"
}

func defaultThinkingBudget(level string, budgets map[string]int) int {
	fallbacks := map[string]int{"minimal": 256, "low": 512, "medium": 1024, "high": 2048, "xhigh": 4096}
	if level == "off" {
		return 0
	}
	if budgets != nil {
		if value, ok := budgets[level]; ok {
			return value
		}
	}
	return fallbacks[level]
}

func AnthropicThinkingForLevel(level string, budgets map[string]int) AnthropicOptions {
	if level == "" || level == "off" {
		return AnthropicOptions{}
	}
	out := AnthropicOptions{ThinkingEnabled: true, ThinkingEffort: mapThinkingLevelToEffort(level)}
	if budget := defaultThinkingBudget(level, budgets); budget > 0 {
		out.ThinkingBudgetTokens = budget
		out.HasThinkingBudget = true
	}
	return out
}

type CacheRetention string

const (
	CacheShort CacheRetention = "short"
	CacheLong  CacheRetention = "long"
	CacheNone  CacheRetention = "none"
)

type cacheControl struct {
	retention CacheRetention
	hasTTL    bool
}

func getCacheControl(baseURL string, hasBaseURL bool, retention CacheRetention, hasRetention bool, supportsLongTTL bool) *cacheControl {
	chosen := CacheShort
	if hasRetention {
		chosen = retention
	}
	if chosen == CacheNone {
		return nil
	}
	return &cacheControl{
		retention: chosen,
		hasTTL:    chosen == CacheLong && (IsAnthropicHost(baseURL, hasBaseURL) || supportsLongTTL),
	}
}

func cacheBlock(cc *cacheControl) jsonValue {
	if cc == nil {
		return nil
	}
	out := jsonObject{member("type", jsonString("ephemeral"))}
	if cc.hasTTL {
		out = out.with(member("ttl", jsonString("1h")))
	}
	return out
}

type AnthropicOptions struct {
	MaxTokens            int
	HasMaxTokens         bool
	Temperature          float64
	HasTemperature       bool
	CacheRetention       CacheRetention
	HasCacheRetention    bool
	ThinkingEnabled      bool
	ThinkingBudgetTokens int
	HasThinkingBudget    bool
	ThinkingEffort       string
	UserID               string
	HasUserID            bool
	ToolChoiceType       string
	ToolChoiceName       string
	HasToolChoice        bool
	Now                  func() int64
	PingMillis           int64
	ModelHeaders         []Header
}

type AnthropicTool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

func BuildAnthropicRequestBody(model Model, ctx Context, options AnthropicOptions, apiKey string) ([]byte, bool) {
	isOAuth := IsOAuthToken(apiKey)
	merged := MergeCompat(model)
	supportsLongTTL := model.HasCompat && model.Compat.SupportsAnthropicCacheTTL != nil && *model.Compat.SupportsAnthropicCacheTTL
	cc := getCacheControl(model.BaseURL, model.HasBaseURL, options.CacheRetention, options.HasCacheRetention, supportsLongTTL)
	_ = merged

	transformed := PreTransform(ctx.Messages, TransformConfig{
		TargetAPI:             model.API,
		TargetProvider:        model.Provider,
		TargetModelID:         model.ID,
		MaxToolIDLen:          64,
		InsertSyntheticResult: true,
		IsOAuth:               isOAuth,
		Tools:                 anthropicTools(ctx.Tools),
	})
	prepared := ctx
	prepared.Messages = transformed

	requestedMax := model.MaxTokens / 3
	if requestedMax > 32000 {
		requestedMax = 32000
	}
	if options.HasMaxTokens {
		requestedMax = options.MaxTokens
	}
	emitsThinking := options.ThinkingEnabled && model.Reasoning && (supportsAdaptiveThinking(model.ID) || requestedMax > 1024)

	body := jsonObject{
		member("model", jsonString(model.ID)),
		member("max_tokens", jsonInt(requestedMax)),
		member("stream", jsonBool(true)),
	}
	if options.HasTemperature {
		if !(emitsThinking && options.Temperature != 1) {
			body = body.with(member("temperature", jsonFloat(options.Temperature)))
		}
	}

	hasSystem := ctx.HasSystem && ctx.SystemPrompt != ""
	systemText := ctx.SystemPrompt
	if isOAuth && hasSystem {
		systemText = oauthSystemText + "\n\n" + ctx.SystemPrompt
	} else if isOAuth {
		systemText = oauthSystemText
	}
	if hasSystem || isOAuth {
		block := jsonObject{member("type", jsonString("text")), member("text", jsonString(systemText))}
		if cache := cacheBlock(cc); cache != nil {
			block = block.with(member("cache_control", cache))
		}
		body = body.with(member("system", jsonArray{block}))
	}

	body = body.with(member("messages", anthropicMessages(prepared, cc)))

	if len(ctx.Tools) > 0 {
		defs := jsonArray{}
		for _, tool := range ctx.Tools {
			schema := strings.TrimSpace(string(tool.Parameters))
			if schema == "" {
				schema = "{}"
			}
			defs = append(defs, jsonObject{
				member("name", jsonString(tool.Name)),
				member("description", jsonString(tool.Description)),
				member("input_schema", jsonRaw(schema)),
			})
		}
		body = body.with(member("tools", defs))
	}
	if options.HasToolChoice {
		if value, ok := anthropicToolChoice(options); ok {
			body = body.with(member("tool_choice", value))
		}
	}
	if options.HasUserID {
		body = body.with(member("metadata", jsonObject{member("user_id", jsonString(options.UserID))}))
	}
	if options.ThinkingEnabled && model.Reasoning {
		if supportsAdaptiveThinking(model.ID) {
			body = body.with(member("thinking", jsonObject{member("type", jsonString("adaptive"))}))
			if options.ThinkingEffort != "" {
				body = body.with(member("output_config", jsonObject{member("effort", jsonString(options.ThinkingEffort))}))
			}
		} else if requestedMax > 1024 {
			budget := 1024
			if options.HasThinkingBudget {
				budget = options.ThinkingBudgetTokens
			}
			ceiling := 0
			if requestedMax > 0 {
				ceiling = requestedMax - 1
			}
			if budget > ceiling {
				budget = ceiling
			}
			if budget < 1024 {
				budget = 1024
			}
			body = body.with(member("thinking", jsonObject{
				member("type", jsonString("enabled")),
				member("budget_tokens", jsonInt(budget)),
			}))
		}
	}
	return []byte(body.encode()), isOAuth
}

func anthropicTools(tools []Tool) []Tool { return tools }

func anthropicToolChoice(options AnthropicOptions) (jsonValue, bool) {
	switch options.ToolChoiceType {
	case ToolChoiceAuto, ToolChoiceNone, ToolChoiceRequired, "any":
		wire := map[string]string{
			ToolChoiceAuto:     "auto",
			ToolChoiceNone:     "none",
			ToolChoiceRequired: "any",
			"any":              "any",
		}[options.ToolChoiceType]
		return jsonObject{member("type", jsonString(wire))}, true
	case ToolChoiceFunction, "tool":
		return jsonObject{
			member("type", jsonString("tool")),
			member("name", jsonString(options.ToolChoiceName)),
		}, true
	}
	return nil, false
}

func hasToolUse(msg Message) bool {
	if msg.Assistant == nil {
		return false
	}
	for _, part := range msg.Assistant.Parts {
		if part.ToolCall != nil {
			return true
		}
	}
	return false
}

func thinkingBlock(part ContentPart) jsonObject {
	if part.Thinking == nil {
		return jsonObject{}
	}
	if part.Thinking.Signature == "" {
		return jsonObject{
			member("type", jsonString("text")),
			member("text", jsonString(part.Thinking.Thinking)),
		}
	}
	return jsonObject{
		member("type", jsonString("thinking")),
		member("thinking", jsonString(part.Thinking.Thinking)),
		member("signature", jsonString(part.Thinking.Signature)),
	}
}

func imageBlock(img *ImagePart) jsonObject {
	return jsonObject{
		member("type", jsonString("image")),
		member("source", jsonObject{
			member("type", jsonString("base64")),
			member("media_type", jsonString(img.MediaType)),
			member("data", jsonString(img.Data)),
		}),
	}
}

func assistantBlocks(parts []ContentPart, withTools bool) jsonArray {
	blocks := jsonArray{}
	for _, part := range parts {
		switch {
		case part.Text != nil:
			blocks = append(blocks, jsonObject{
				member("type", jsonString("text")),
				member("text", jsonString(part.Text.Text)),
			})
		case part.Thinking != nil:
			blocks = append(blocks, thinkingBlock(part))
		case part.Image != nil:
			if withTools {
				blocks = append(blocks, imageBlock(part.Image))
			}
		case part.ToolCall != nil && withTools:
			arguments := strings.TrimSpace(part.ToolCall.Arguments)
			if arguments == "" {
				arguments = "{}"
			}
			blocks = append(blocks, jsonObject{
				member("type", jsonString("tool_use")),
				member("id", jsonString(part.ToolCall.ID)),
				member("name", jsonString(part.ToolCall.Name)),
				member("input", jsonRaw(arguments)),
			})
		}
	}
	return blocks
}

func toolResultContent(parts []ContentPart) (jsonValue, bool) {
	if len(parts) == 1 && parts[0].Text != nil {
		return jsonString(parts[0].Text.Text), true
	}
	if len(parts) > 1 {
		return jsonArray{}, false
	}
	if len(parts) == 1 && parts[0].Image != nil {
		return jsonArray{}, false
	}
	return jsonString(""), true
}

func toolResultBlocks(parts []ContentPart) jsonArray {
	blocks := jsonArray{}
	for _, part := range parts {
		switch {
		case part.Text != nil:
			blocks = append(blocks, jsonObject{
				member("type", jsonString("text")),
				member("text", jsonString(part.Text.Text)),
			})
		case part.Image != nil:
			blocks = append(blocks, imageBlock(part.Image))
		}
	}
	return blocks
}

func anthropicMessages(ctx Context, cc *cacheControl) jsonArray {
	lastUser := -1
	for i, msg := range ctx.Messages {
		if msg.User != nil {
			lastUser = i
		}
	}
	toolCallIDs := collectToolCallIDs(ctx.Messages)

	out := jsonArray{}
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

		if msg.ToolResult != nil {
			results := jsonArray{}
			for i < len(ctx.Messages) && ctx.Messages[i].ToolResult != nil {
				if isOrphanedToolResult(ctx.Messages[i], toolCallIDs) {
					i++
					continue
				}
				tr := ctx.Messages[i].ToolResult
				entry := jsonObject{
					member("type", jsonString("tool_result")),
					member("tool_use_id", jsonString(tr.ToolCallID)),
				}
				if content, isString := toolResultContent(tr.Parts); isString {
					entry = entry.with(member("content", content))
				} else {
					entry = entry.with(member("content", toolResultBlocks(tr.Parts)))
				}
				entry = entry.with(member("is_error", jsonBool(tr.IsError)))
				results = append(results, entry)
				i++
			}
			out = append(out, jsonObject{
				member("role", jsonString(string(RoleUser))),
				member("content", results),
			})
			continue
		}

		role := string(RoleUser)
		if msg.Assistant != nil {
			role = string(RoleAssistant)
		}

		if msg.Assistant != nil && hasToolUse(msg) {
			out = append(out, jsonObject{
				member("role", jsonString(role)),
				member("content", assistantBlocks(msg.Assistant.Parts, true)),
			})
			i++
			continue
		}

		isLastUser := i == lastUser
		if msg.Assistant == nil && msg.User == nil {
			i++
			continue
		}
		switch {
		case msg.Assistant != nil:
			out = append(out, jsonObject{
				member("role", jsonString(role)),
				member("content", assistantBlocks(msg.Assistant.Parts, false)),
			})
		case !msg.User.UseParts:
			if isLastUser && cc != nil {
				block := jsonObject{
					member("type", jsonString("text")),
					member("text", jsonString(msg.User.Text)),
					member("cache_control", cacheBlock(cc)),
				}
				out = append(out, jsonObject{
					member("role", jsonString(role)),
					member("content", jsonArray{block}),
				})
			} else {
				out = append(out, jsonObject{
					member("role", jsonString(role)),
					member("content", jsonString(msg.User.Text)),
				})
			}
		default:
			blocks := jsonArray{}
			for idx, part := range msg.User.Parts {
				if idx == len(msg.User.Parts)-1 && isLastUser && cc != nil {
					if part.Text != nil {
						blocks = append(blocks, jsonObject{
							member("type", jsonString("text")),
							member("text", jsonString(part.Text.Text)),
							member("cache_control", cacheBlock(cc)),
						})
						continue
					}
				}
				switch {
				case part.Text != nil:
					blocks = append(blocks, jsonObject{
						member("type", jsonString("text")),
						member("text", jsonString(part.Text.Text)),
					})
				case part.Image != nil:
					blocks = append(blocks, imageBlock(part.Image))
				}
			}
			out = append(out, jsonObject{
				member("role", jsonString(role)),
				member("content", blocks),
			})
		}
		i++
	}
	return out
}
