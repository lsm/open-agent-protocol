package agent

import (
	"context"
	"time"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

type TurnRequest struct {
	Model    provider.Model
	Messages []provider.Message
	Tools    []provider.Tool
	Options  provider.StreamOptions
	Read     provider.ReadChunkFunc
}

type Turn struct {
	Events <-chan provider.Event
}

type Streamer interface {
	Stream(ctx context.Context, request TurnRequest) Turn
}

type ChunkStreamer struct{}

func (ChunkStreamer) Stream(ctx context.Context, request TurnRequest) Turn {
	out := make(chan provider.Event, 16)
	read := request.Read
	if read == nil {
		read = func() ([]byte, error) { return nil, nil }
	}
	options := request.Options
	if options.Now == nil {
		options.Now = func() int64 { return time.Now().UnixMilli() }
	}
	if ctx.Err() != nil {
		close(out)
		return Turn{Events: out}
	}
	cancelled := func() bool { return ctx.Err() != nil }
	turn := provider.Context{Messages: request.Messages, Tools: request.Tools}
	go func() {
		defer close(out)
		sink := &provider.EventSink{}
		stopped := make(chan struct{})
		defer close(stopped)
		sink.OnEvent = func(event provider.Event) {
			select {
			case out <- event:
			case <-stopped:
			case <-ctx.Done():
			}
		}
		streamTurn(sink, request.Model, turn, options, read, cancelled)
	}()
	return Turn{Events: out}
}

func streamTurn(sink *provider.EventSink, model provider.Model, turn provider.Context, options provider.StreamOptions, read provider.ReadChunkFunc, cancelled provider.CancelledFunc) {
	if model.API != "anthropic-messages" {
		provider.Stream(sink, model, turn, options, read, cancelled)
		return
	}
	provider.StreamAnthropic(sink, model, turn, anthropicOptions(options), read, cancelled, nil)
}

func anthropicOptions(options provider.StreamOptions) provider.AnthropicOptions {
	out := provider.AnthropicOptions{Now: options.Now, PingMillis: options.PingMillis}
	if options.HasMaxTokens {
		out.MaxTokens = options.MaxTokens
		out.HasMaxTokens = true
	}
	if options.HasTemperature {
		out.Temperature = options.Temperature
		out.HasTemperature = true
	}
	if options.HasToolChoice {
		out.ToolChoiceType = options.ToolChoice.Mode
		out.ToolChoiceName = options.ToolChoice.Function
		out.HasToolChoice = true
	}
	if options.ReasoningEffort != "" {
		out.ThinkingEnabled = true
		out.ThinkingEffort = options.ReasoningEffort
	}
	return out
}
