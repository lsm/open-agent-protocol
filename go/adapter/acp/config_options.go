package acp

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
)

func configureSession(ctx context.Context, client Client, opened *native.SessionNewResult, wanted map[string]string) error {
	for _, id := range slices.Sorted(maps.Keys(wanted)) {
		value := wanted[id]
		option, ok := configuredOption(opened.ConfigOptions, id)
		if id == "" || value == "" || !ok || !slices.ContainsFunc(option.Values(), func(v native.ConfigValue) bool { return v.Value == value }) {
			return fmt.Errorf("ACP session config %q: requested value is not advertised", id)
		}
		var set native.SetConfigOptionResult
		if err := client.Call(ctx, native.MethodSessionSetConfigOption, native.SetConfigOptionParams{SessionID: opened.SessionID, ConfigID: id, Value: value}, &set); err != nil {
			return fmt.Errorf("ACP session config %q: set rejected: %w", id, err)
		}
		opened.ConfigOptions = set.ConfigOptions
		if option, ok := configuredOption(opened.ConfigOptions, id); !ok || option.CurrentValue != value {
			return fmt.Errorf("ACP session config %q: value not confirmed", id)
		}
	}
	return confirmSessionConfig(opened.ConfigOptions, wanted)
}

func confirmSessionConfig(options []native.ConfigOption, wanted map[string]string) error {
	for id, value := range wanted {
		if option, ok := configuredOption(options, id); !ok || option.CurrentValue != value {
			return fmt.Errorf("ACP session config %q: later setting changed the requested value", id)
		}
	}
	return nil
}

func configuredOption(options []native.ConfigOption, id string) (native.ConfigOption, bool) {
	var option native.ConfigOption
	count := 0
	for _, o := range options {
		if o.ID == id {
			option, count = o, count+1
		}
	}
	return option, count == 1 && (option.Type == "" || option.Type == "select")
}
