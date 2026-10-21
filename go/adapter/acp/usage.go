package acp

import (
	"bytes"
	"encoding/json"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	turnUsageExtension = "com.agentclientprotocol.turn_usage"
	contextExtension   = "com.agentclientprotocol.context"
)

type turnUsage struct {
	total, input, output             uint64
	thought, cachedRead, cachedWrite *uint64
}

type contextUsage struct {
	used, size uint64
	cost       *sessionCost
}

type sessionCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type nativeTurnUsage struct {
	TotalTokens       json.RawMessage `json:"totalTokens"`
	InputTokens       json.RawMessage `json:"inputTokens"`
	OutputTokens      json.RawMessage `json:"outputTokens"`
	ThoughtTokens     json.RawMessage `json:"thoughtTokens"`
	CachedReadTokens  json.RawMessage `json:"cachedReadTokens"`
	CachedWriteTokens json.RawMessage `json:"cachedWriteTokens"`
}

type nativeUsageUpdate struct {
	Used json.RawMessage `json:"used"`
	Size json.RawMessage `json:"size"`
	Cost json.RawMessage `json:"cost"`
}

type nativeCost struct {
	Amount   json.RawMessage `json:"amount"`
	Currency json.RawMessage `json:"currency"`
}

func decodeTurnUsage(raw json.RawMessage) *turnUsage {
	var native nativeTurnUsage
	if !decodeObject(raw, &native) {
		return nil
	}
	total, totalOK := decodeUint(native.TotalTokens)
	input, inputOK := decodeUint(native.InputTokens)
	output, outputOK := decodeUint(native.OutputTokens)
	if !totalOK || !inputOK || !outputOK {
		return nil
	}
	return &turnUsage{total: total, input: input, output: output, thought: optionalUint(native.ThoughtTokens), cachedRead: optionalUint(native.CachedReadTokens), cachedWrite: optionalUint(native.CachedWriteTokens)}
}

func decodeContextUsage(raw json.RawMessage) *contextUsage {
	var native nativeUsageUpdate
	if !decodeObject(raw, &native) {
		return nil
	}
	used, usedOK := decodeUint(native.Used)
	size, sizeOK := decodeUint(native.Size)
	if !usedOK || !sizeOK {
		return nil
	}
	return &contextUsage{used: used, size: size, cost: decodeCost(native.Cost)}
}

func decodeCost(raw json.RawMessage) *sessionCost {
	var native nativeCost
	if !decodeObject(raw, &native) || isNull(native.Amount) || isNull(native.Currency) {
		return nil
	}
	var cost sessionCost
	if json.Unmarshal(native.Amount, &cost.Amount) != nil || json.Unmarshal(native.Currency, &cost.Currency) != nil || cost.Currency == "" {
		return nil
	}
	return &cost
}

func decodeObject(raw json.RawMessage, into any) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Unmarshal(trimmed, into) == nil
}

func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func decodeUint(raw json.RawMessage) (uint64, bool) {
	if isNull(raw) {
		return 0, false
	}
	var value uint64
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	return value, true
}

func optionalUint(raw json.RawMessage) *uint64 {
	value, ok := decodeUint(raw)
	if !ok {
		return nil
	}
	return &value
}

func (u *turnUsage) core() *protocol.Usage {
	return &protocol.Usage{InputTokens: &u.input, OutputTokens: &u.output, TotalTokens: &u.total}
}

func (u *turnUsage) extension() json.RawMessage {
	fields := map[string]uint64{"total_tokens": u.total, "input_tokens": u.input, "output_tokens": u.output}
	for key, value := range map[string]*uint64{"thought_tokens": u.thought, "cached_read_tokens": u.cachedRead, "cached_write_tokens": u.cachedWrite} {
		if value != nil {
			fields[key] = *value
		}
	}
	encoded, _ := json.Marshal(fields)
	return encoded
}

func (c *contextUsage) extension() json.RawMessage {
	fields := map[string]any{"used_tokens": c.used, "size_tokens": c.size}
	if c.cost != nil {
		fields["cost"] = c.cost
	}
	encoded, _ := json.Marshal(fields)
	return encoded
}

func usageExtensions(turn *turnUsage, context *contextUsage) map[string]json.RawMessage {
	extensions := map[string]json.RawMessage{}
	if turn != nil {
		extensions[turnUsageExtension] = turn.extension()
	}
	if context != nil {
		extensions[contextExtension] = context.extension()
	}
	if len(extensions) == 0 {
		return nil
	}
	return extensions
}
