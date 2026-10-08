package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type ProviderService struct {
	transport *transport
	timeout   time.Duration
}

func (s *ProviderService) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	stream, err := s.Stream(ctx, req)
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

func (s *ProviderService) Stream(ctx context.Context, req CompletionRequest) (*ProviderStream, error) {
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
	request := providerFrame("inference.create.request", payload)
	sub := s.transport.subscribeStream(string(request.ID))
	sub.correlate(string(request.ID))
	if err := s.transport.sendProviderEnvelope(request); err != nil {
		sub.close()
		return nil, err
	}
	return &ProviderStream{ctx: ctx, transport: s.transport, sub: sub, streamID: string(request.ID),
		timeout: s.timeout, fallbackProvider: providerIDFromRef(req.ModelRef),
		oapModelRef: req.ModelRef, oapPartKinds: make(map[int]string)}, nil
}

type ProviderStream struct {
	oapModelRef      string
	oapInferenceID   protocol.InferenceID
	oapPartKinds     map[int]string
	oapResponse      *CompletionResponse
	ctx              context.Context
	transport        *transport
	sub              *subscription
	streamID         string
	timeout          time.Duration
	fallbackProvider string

	current  ProviderEvent
	err      error
	done     bool
	finished bool
}

func (s *ProviderStream) Next() bool {
	if s.done {
		return false
	}
	for {
		in, err := s.sub.next(s.ctx, s.timeout, "OAP inference")
		if err != nil {
			s.fail(err)
			return false
		}
		if in.broken != nil {
			s.fail(in.broken)
			return false
		}
		p := in.body()
		switch in.kind() {
		case "inference.create.response":
			if accepted, _ := p.boolean("accepted"); !accepted {
				s.fail(in.failure(s.fallbackProvider))
				return false
			}
			s.oapInferenceID = protocol.InferenceID(in.inference())
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
			response, err := oapResponse(p, s.oapModelRef, "message")
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
			s.fail(in.failure(s.fallbackProvider))
			return false
		default:
			s.fail(&StreamError{Kind: KindTransportError, Message: fmt.Sprintf("unexpected OAP inference event %q", in.kind())})
			return false
		}
	}
}

func (s *ProviderStream) Event() ProviderEvent { return s.current }

func (s *ProviderStream) Err() error { return s.err }

func (s *ProviderStream) Close() error {
	if s.sub == nil {
		return s.err
	}
	if !s.finished && s.oapInferenceID != "" {
		cancel := providerFrame("inference.cancel.request", map[string]any{"reason": "caller_closed"})
		cancel.InferenceID = s.oapInferenceID
		s.transport.sendProviderEnvelopeBestEffort(cancel)
	}
	s.sub.close()
	s.sub = nil
	s.done = true
	return s.err
}

func (s *ProviderStream) fail(err error) {
	s.err = withStreamID(err, s.streamID)
	s.done = true
	s.current = nil
}

func withStreamID(err error, streamID string) error {
	var streamErr *StreamError
	if asStreamError(err, &streamErr) && streamErr.StreamID == "" {
		streamErr.StreamID = streamID
	}
	return err
}
