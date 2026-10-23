package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type input struct {
	Admission   protocol.MessageSubmitResponse `json:"admission"`
	Events      []protocol.Envelope            `json:"events"`
	Hold        bool                           `json:"hold"`
	StreamError bool                           `json:"stream_error"`
	SubmitError bool                           `json:"submit_error"`
	Message     *protocol.Message              `json:"message"`
}

type scripted struct{ data input }

func (s scripted) Submit(context.Context, adapter.SubmitRequest) (protocol.MessageSubmitResponse, adapter.EventStream, error) {
	if s.data.SubmitError {
		return protocol.MessageSubmitResponse{}, nil, adapter.ErrSessionClosed
	}
	stream := make(chan adapter.Result, len(s.data.Events)+1)
	for _, e := range s.data.Events {
		stream <- adapter.Result{Envelope: e}
	}
	if s.data.StreamError {
		stream <- adapter.Result{Error: adapter.ErrSessionClosed}
	}
	if !s.data.Hold {
		close(stream)
	}
	return s.data.Admission, stream, nil
}
func (s scripted) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{}, adapter.ErrUnsupportedInput
}
func (s scripted) Resolve(context.Context, adapter.InteractionResolution) error {
	return adapter.ErrUnsupportedInput
}
func (s scripted) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{}, adapter.ErrUnsupportedInput
}
func (s scripted) Resume(context.Context, adapter.ResumeRequest) (adapter.Recovery, adapter.EventStream, error) {
	return adapter.Recovery{}, nil, adapter.ErrUnsupportedInput
}
func (s scripted) Close(context.Context) error { return nil }

func main() {
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	var data input
	if err = json.Unmarshal(raw, &data); err != nil {
		panic(err)
	}
	if data.Message != nil {
		text, err := adapter.MessageText(*data.Message)
		kind := "completed"
		if err != nil {
			kind = "content"
		}
		if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": kind, "text": text}); err != nil {
			panic(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	outcome, err := adapter.RunToTerminal(ctx, scripted{data}, protocol.MessageSubmitRequest{}, adapter.RunOptions{StallWindow: time.Second})
	kind := "completed"
	var failure any
	var failed *adapter.RunFailedError
	switch {
	case errors.As(err, &failed):
		kind = "failed"
		failure = failed.Failure
	case errors.Is(err, adapter.ErrRunCancelled):
		kind = "cancelled"
	case errors.Is(err, adapter.ErrNoTerminal):
		kind = "no_terminal"
	case errors.Is(err, adapter.ErrGateUnanswered):
		kind = "gate_unanswered"
	case errors.Is(err, adapter.ErrSessionClosed):
		kind = "adapter"
	case err != nil:
		kind = "decode"
	}
	var run any
	if outcome.RunID != "" {
		run = outcome.RunID
	}
	var response any
	if outcome.Response.Role != "" {
		response = outcome.Response
	}
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"kind": kind, "run_id": run, "text": outcome.Text, "response": response, "usage": outcome.Usage, "tool_calls": outcome.ToolCalls, "failure": failure}); err != nil {
		panic(err)
	}
}
