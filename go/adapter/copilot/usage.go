package copilot

import (
	"encoding/json"
	"math"
	"strconv"

	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type usageState struct {
	input, output *uint64
	measured      bool
	cost          float64
	model         string
}

func (u *usageState) observe(data map[string]json.RawMessage) {
	u.input = accumulate(u.input, data["inputTokens"], !u.measured)
	u.output = accumulate(u.output, data["outputTokens"], !u.measured)
	u.measured = true
	var cost float64
	_ = json.Unmarshal(data["cost"], &cost)
	u.cost += cost
	if model := native.Text(data, "model"); model != "" {
		u.model = model
	}
}

func accumulate(previous *uint64, raw json.RawMessage, first bool) *uint64 {
	if len(raw) == 0 {
		return nil
	}
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return nil
		}
	}
	next, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return nil
	}
	if !first {
		if previous == nil || next > math.MaxUint64-*previous {
			return nil
		}
		next += *previous
	}
	return &next
}

func (u *usageState) portable() protocol.Usage {
	return protocol.Usage{InputTokens: u.input, OutputTokens: u.output}
}

func (u *usageState) extensions() map[string]json.RawMessage {
	if !u.measured {
		return nil
	}
	ext := map[string]json.RawMessage{}
	if !math.IsInf(u.cost, 0) && !math.IsNaN(u.cost) {
		ext["com.github.copilot.cost"], _ = json.Marshal(map[string]float64{"premium_requests": u.cost})
	}
	if u.model != "" {
		ext["com.github.copilot.routed_model"], _ = json.Marshal(map[string]string{"model": u.model})
	}
	return ext
}
