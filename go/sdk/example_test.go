package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	sdk "github.com/lsm/open-agent-protocol/go/sdk"
)

func ExampleNew() {
	ctx := context.Background()

	client, err := sdk.New(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	model, err := client.Models.Resolve(ctx, sdk.ResolveModelRequest{
		ProviderID: "anthropic",
		API:        "anthropic-messages",
		ModelID:    "claude-sonnet-4-5",
	})
	if err != nil {
		log.Fatal(err)
	}

	response, err := client.Provider.Complete(ctx, sdk.CompletionRequest{
		ModelRef: model.ModelRef,
		Messages: []sdk.Message{sdk.UserMessage("Write a haiku about streams.")},
		Options:  &sdk.RunOptions{MaxTokens: sdk.MaxTokens(128)},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(response.Message.Text)
}

func ExampleProviderService_Stream() {
	ctx := context.Background()
	client, err := sdk.New(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	stream, err := client.Provider.Stream(ctx, sdk.CompletionRequest{
		ModelRef: "anthropic/anthropic-messages@claude-sonnet-4-5",
		Messages: []sdk.Message{sdk.UserMessage("Explain lock-free queues in one paragraph.")},
		Options:  &sdk.RunOptions{MaxTokens: sdk.MaxTokens(256)},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()

	for stream.Next() {
		switch event := stream.Event().(type) {
		case *sdk.MessageStart:
			fmt.Fprintf(os.Stderr, "streaming %s/%s\n", event.ProviderID, event.ModelID)
		case *sdk.TextDelta:
			fmt.Print(event.Delta)
		case *sdk.ToolCallEvent:
			fmt.Fprintf(os.Stderr, "\ntool call: %s(%s)\n", event.Name, event.ArgumentsJSON)
		case *sdk.MessageEnd:
			fmt.Fprintf(os.Stderr, "\nstop reason: %s\n", event.StopReason)
		}
	}
	if err := stream.Err(); err != nil {
		log.Fatal(err)
	}
}

func ExampleAgentService_Run() {
	ctx := context.Background()
	client, err := sdk.New(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	weather := sdk.Tool{
		Name:        "get_weather",
		Description: "Get the current weather for a city.",
		ParametersSchemaJSON: `{
			"type": "object",
			"properties": {"city": {"type": "string"}},
			"required": ["city"],
			"additionalProperties": false
		}`,
		Execute: func(ctx context.Context, call sdk.ToolInvocation) (string, error) {
			var args struct {
				City string `json:"city"`
			}
			if err := json.Unmarshal([]byte(call.ArgumentsJSON), &args); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			return "It is raining in " + args.City + ".", nil
		},
	}

	response, err := client.Agent.Run(ctx, sdk.AgentRequest{
		ModelRef: "anthropic/anthropic-messages@claude-sonnet-4-5",
		Messages: []sdk.Message{sdk.UserMessage("Should I bring an umbrella in San Francisco?")},
		Tools:    []sdk.Tool{weather},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(response.Message.Text)
}

func ExampleAuthService_Login() {
	ctx := context.Background()
	client, err := sdk.New(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	providers, err := client.Auth.ListProviders(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, provider := range providers {
		if provider.ID != "anthropic" || provider.Status == sdk.AuthAuthenticated {
			continue
		}
		err := client.Auth.Login(ctx, provider.ID, sdk.LoginHandlers{
			OnEvent: func(event sdk.AuthEvent) {
				switch event.Type {
				case sdk.AuthEventURL:
					fmt.Println("open", event.URL)
				case sdk.AuthEventProgress:
					fmt.Println(event.Message)
				}
			},
		})
		if err != nil {
			log.Fatal(err)
		}
	}
}

func ExampleAuthRequiredError() {
	ctx := context.Background()
	client, err := sdk.New(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	request := sdk.CompletionRequest{
		ModelRef: "anthropic/anthropic-messages@claude-sonnet-4-5",
		Messages: []sdk.Message{sdk.UserMessage("Hello")},
	}

	response, err := client.Provider.Complete(ctx, request)

	var authRequired *sdk.AuthRequiredError
	if errors.As(err, &authRequired) {
		if err := client.Auth.Login(ctx, authRequired.ProviderID, sdk.LoginHandlers{}); err != nil {
			log.Fatal(err)
		}
		response, err = client.Provider.Complete(ctx, request)
	}
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(response.Message.Text)
}
