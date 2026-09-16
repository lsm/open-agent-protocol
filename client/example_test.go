package client_test

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/client"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/serve/servehttp"
)

// Example drives the canonical lifecycle against a local daemon: discover,
// open, subscribe, submit, resolve both interactive gates, and consume the
// run to its terminal event. Against a daemon started with `oap serve`, the
// client is simply client.New("127.0.0.1:6270"); this example serves the
// built-in memory adapter privately so it stays hermetic.
func Example() {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{})); err != nil {
		fmt.Println("register:", err)
		return
	}
	hub := serve.New(registry, serve.Options{})
	daemon, err := servehttp.New(hub, servehttp.Options{})
	if err != nil {
		fmt.Println("daemon:", err)
		return
	}
	server := httptest.NewServer(daemon.Handler())
	defer server.Close()

	ctx := context.Background()
	c := client.New(server.URL)

	// Discovery: the adapter listing and one capability snapshot.
	adapters, err := c.Adapters(ctx)
	if err != nil {
		fmt.Println("adapters:", err)
		return
	}
	caps, err := c.Capabilities(ctx, adapters[0].Name)
	if err != nil {
		fmt.Println("capabilities:", err)
		return
	}
	fmt.Println("adapter:", adapters[0].Name, "revision:", caps.Revision)

	// One session, subscribed before submitting so the run's first envelope
	// cannot be missed.
	session, err := c.Open(ctx, adapters[0].Name, "demo")
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	stream := session.Events(ctx)
	if _, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run the golden script")}},
		Delivery: protocol.DeliveryAuto,
	}); err != nil {
		fmt.Println("submit:", err)
		return
	}

	// Consume the run, resolving the scripted gates as they arrive.
	for {
		envelope, err := stream.Next()
		if err == io.EOF {
			break // the run reached its terminal event
		}
		if err != nil {
			fmt.Println("stream:", err)
			return
		}
		fmt.Println("event:", envelope.Type)
		switch envelope.Type {
		case protocol.TypeActionPermissionRequested:
			var requested protocol.PermissionRequestedPayload
			if err := envelope.DecodePayload(&requested); err != nil {
				fmt.Println("decode:", err)
				return
			}
			if err := session.ResolvePermission(ctx, protocol.PermissionResolveRequest{
				InteractionID: requested.InteractionID, RequestedBy: requested.RequestedBy,
				RespondedBy: requested.RespondedBy, SessionID: requested.SessionID,
				RunID: requested.RunID, ChoiceID: "approve", Granted: true,
			}); err != nil {
				fmt.Println("resolve permission:", err)
				return
			}
		case protocol.TypeUserInputRequested:
			var requested protocol.UserInputRequestedPayload
			if err := envelope.DecodePayload(&requested); err != nil {
				fmt.Println("decode:", err)
				return
			}
			if err := session.ResolveInput(ctx, protocol.UserInputResolveRequest{
				InteractionID: requested.InteractionID, RequestedBy: requested.RequestedBy,
				RespondedBy: requested.RespondedBy, SessionID: requested.SessionID,
				RunID:   requested.RunID,
				Answers: []protocol.InputAnswer{{QuestionID: requested.Questions[0].ID, SelectedOptionIDs: []string{"yes"}}},
			}); err != nil {
				fmt.Println("resolve input:", err)
				return
			}
		case protocol.TypeRunCompleted:
			if text, ok := client.FinalText(envelope); ok {
				fmt.Println("final:", text)
			}
		}
	}

	if err := session.Close(ctx); err != nil {
		fmt.Println("close:", err)
		return
	}
	fmt.Println("closed")
	// Output:
	// adapter: memory revision: reference-memory-v2
	// event: run.started
	// event: content.delta
	// event: action.call.requested
	// event: action.permission.requested
	// event: action.permission.resolved
	// event: action.call.started
	// event: action.call.completed
	// event: user.input.requested
	// event: run.status.updated
	// event: user.input.resolved
	// event: content.delta
	// event: run.completed
	// final: The golden script completed.
	// closed
}

// ExampleSession_EventsAfter resumes a completed run's stream from a cursor:
// the replayed suffix first, then the stream's documented clean end at the
// terminal event.
func ExampleSession_EventsAfter() {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{})); err != nil {
		fmt.Println("register:", err)
		return
	}
	hub := serve.New(registry, serve.Options{})
	daemon, err := servehttp.New(hub, servehttp.Options{})
	if err != nil {
		fmt.Println("daemon:", err)
		return
	}
	server := httptest.NewServer(daemon.Handler())
	defer server.Close()

	ctx := context.Background()
	c := client.New(server.URL)
	session, err := c.Open(ctx, "memory", "replay")
	if err != nil {
		fmt.Println("open:", err)
		return
	}
	// A helper consumes one full run so the journal holds a terminal run;
	// see Example for the gate-resolving loop this elides.
	runID, err := runGolden(ctx, session)
	if err != nil {
		fmt.Println("run:", err)
		return
	}

	stream := session.EventsAfter(ctx, runID, 10)
	for {
		envelope, err := stream.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Println("stream:", err)
			return
		}
		fmt.Println("replayed:", envelope.Type, *envelope.Sequence)
	}
	// Output:
	// replayed: content.delta 11
	// replayed: run.completed 12
}

// runGolden drives one scripted run to completion on a helper subscription.
func runGolden(ctx context.Context, session *client.Session) (protocol.RunID, error) {
	stream := session.Events(ctx)
	admission, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run the golden script")}},
		Delivery: protocol.DeliveryAuto,
	})
	if err != nil {
		return "", err
	}
	for {
		envelope, err := stream.Next()
		if err == io.EOF {
			return admission.RunID, nil
		}
		if err != nil {
			return "", err
		}
		switch envelope.Type {
		case protocol.TypeActionPermissionRequested:
			var requested protocol.PermissionRequestedPayload
			if err := envelope.DecodePayload(&requested); err != nil {
				return "", err
			}
			if err := session.ResolvePermission(ctx, protocol.PermissionResolveRequest{
				InteractionID: requested.InteractionID, RequestedBy: requested.RequestedBy,
				RespondedBy: requested.RespondedBy, SessionID: requested.SessionID,
				RunID: requested.RunID, ChoiceID: "approve", Granted: true,
			}); err != nil {
				return "", err
			}
		case protocol.TypeUserInputRequested:
			var requested protocol.UserInputRequestedPayload
			if err := envelope.DecodePayload(&requested); err != nil {
				return "", err
			}
			if err := session.ResolveInput(ctx, protocol.UserInputResolveRequest{
				InteractionID: requested.InteractionID, RequestedBy: requested.RequestedBy,
				RespondedBy: requested.RespondedBy, SessionID: requested.SessionID,
				RunID:   requested.RunID,
				Answers: []protocol.InputAnswer{{QuestionID: requested.Questions[0].ID, SelectedOptionIDs: []string{"yes"}}},
			}); err != nil {
				return "", err
			}
		}
	}
}
