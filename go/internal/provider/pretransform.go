package provider

import "strings"

type TransformConfig struct {
	TargetAPI             string
	TargetProvider        string
	TargetModelID         string
	MaxToolIDLen          int
	MistralToolIDs        bool
	InsertSyntheticResult bool
	Tools                 []Tool
}

func isSameModel(a *AssistantContent, config TransformConfig) bool {
	if config.TargetAPI == "" || config.TargetProvider == "" || config.TargetModelID == "" {
		return false
	}
	return a.API == config.TargetAPI && a.Provider == config.TargetProvider && a.Model == config.TargetModelID
}

func NormalizeToolID(id string, maxLen int) string {
	effective := id
	if at := strings.IndexByte(effective, '|'); at >= 0 {
		effective = effective[:at]
	}
	limit := len(effective)
	if maxLen > 0 {
		limit = maxLen
	}
	if len(effective) > limit {
		effective = effective[:limit]
	}
	var out strings.Builder
	out.Grow(len(effective))
	for i := 0; i < len(effective); i++ {
		c := effective[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			out.WriteByte(c)
		} else {
			out.WriteByte('_')
		}
	}
	return out.String()
}

const mistralIDChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func MistralToolID(sourceID string) string {
	var hash uint64
	for i := 0; i < len(sourceID); i++ {
		hash = hash*31 + uint64(sourceID[i])
	}
	out := make([]byte, 9)
	for i := 0; i < 9; i++ {
		idx := int((hash>>uint(i*6))&0x3F) % len(mistralIDChars)
		out[i] = mistralIDChars[idx]
	}
	return string(out)
}

func fromClaudeCodeName(name string, tools []Tool) string {
	for _, tool := range tools {
		if strings.EqualFold(name, tool.Name) {
			return tool.Name
		}
	}
	return name
}

type pendingCall struct {
	id   string
	name string
}

func syntheticResult(id, name string) *ToolResult {
	return &ToolResult{
		ToolCallID: id,
		ToolName:   name,
		Parts:      []ContentPart{{Text: &TextPart{Text: "No result provided"}}},
		IsError:    true,
	}
}

func PreTransform(messages []Message, config TransformConfig) []Message {
	toolIDMap := map[string]string{}
	var allCalls []pendingCall
	for _, msg := range messages {
		if msg.Assistant == nil {
			continue
		}
		for _, part := range msg.Assistant.Parts {
			if part.ToolCall == nil {
				continue
			}
			allCalls = append(allCalls, pendingCall{id: part.ToolCall.ID, name: part.ToolCall.Name})
			if config.MaxToolIDLen > 0 || config.MistralToolIDs || strings.Contains(part.ToolCall.ID, "|") {
				normalized := NormalizeToolID(part.ToolCall.ID, config.MaxToolIDLen)
				if config.MistralToolIDs {
					normalized = MistralToolID(part.ToolCall.ID)
				}
				toolIDMap[part.ToolCall.ID] = normalized
			}
		}
	}

	pending := []pendingCall{}
	answered := map[string]bool{}

	flush := func(out *[]Message) {
		if !config.InsertSyntheticResult {
			return
		}
		for _, call := range pending {
			if answered[call.id] {
				continue
			}
			id := call.id
			if mapped, ok := toolIDMap[id]; ok {
				id = mapped
			}
			*out = append(*out, Message{ToolResult: syntheticResult(id, call.name)})
		}
		pending = pending[:0]
		for k := range answered {
			delete(answered, k)
		}
	}

	out := []Message{}
	for _, msg := range messages {
		switch {
		case msg.Assistant != nil:
			flush(&out)
			if msg.Assistant.StopReason == StopAborted || msg.Assistant.StopReason == StopError {
				continue
			}
			sameModel := isSameModel(msg.Assistant, config)
			parts := []ContentPart{}
			for _, part := range msg.Assistant.Parts {
				switch {
				case part.Thinking != nil:
					if sameModel {
						if part.Thinking.Signature != "" {
							parts = append(parts, part)
							continue
						}
						if !hasVisibleText(part.Thinking.Thinking) {
							continue
						}
						parts = append(parts, part)
						continue
					}
					if !hasVisibleText(part.Thinking.Thinking) {
						continue
					}
					parts = append(parts, ContentPart{Text: &TextPart{Text: part.Thinking.Thinking}})
				case part.ToolCall != nil:
					id := part.ToolCall.ID
					if mapped, ok := toolIDMap[id]; ok {
						id = mapped
					}
					name := part.ToolCall.Name
					rewritten := &ToolCall{
						ID:        id,
						Name:      name,
						Arguments: part.ToolCall.Arguments,
					}
					if sameModel && part.ToolCall.HasThought {
						rewritten.ThoughtSig = part.ToolCall.ThoughtSig
						rewritten.HasThought = true
					}
					parts = append(parts, ContentPart{ToolCall: rewritten})
					pending = append(pending, pendingCall{id: id, name: name})
				default:
					parts = append(parts, part)
				}
			}
			rebuilt := *msg.Assistant
			rebuilt.Parts = parts
			out = append(out, Message{Assistant: &rebuilt})
		case msg.ToolResult != nil:
			answered[msg.ToolResult.ToolCallID] = true
			if mapped, ok := toolIDMap[msg.ToolResult.ToolCallID]; ok {
				remapped := *msg.ToolResult
				remapped.ToolCallID = mapped
				out = append(out, Message{ToolResult: &remapped})
			} else {
				out = append(out, msg)
			}
		default:
			flush(&out)
			out = append(out, msg)
		}
	}
	flush(&out)
	return out
}
