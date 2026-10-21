package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/lsm/open-agent-protocol/go/sdk"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := sdk.New(ctx, &sdk.Options{BinaryPath: os.Args[1], Args: []string{"serve", "agent", "--backend", "codex", "--config", os.Args[2]}})
	if err != nil {
		panic(err)
	}
	defer client.Close()
	stream, err := client.Agent.Stream(ctx, sdk.AgentRequest{ModelRef: "fixture/openai-responses@glm-test", Messages: []sdk.Message{sdk.UserMessage("read the code")}})
	if err != nil {
		panic(err)
	}
	defer stream.Close()
	var ends []*sdk.ToolExecutionEnd
	for stream.Next() {
		if event, ok := stream.Event().(*sdk.ToolExecutionEnd); ok {
			ends = append(ends, event)
		}
	}
	if stream.Err() != nil {
		panic(stream.Err())
	}
	raw, _ := json.Marshal(ends)
	fmt.Println(string(raw))
	if len(ends) != 8 {
		panic("expected eight command outcomes")
	}
	for i, event := range ends {
		if event.IsError != (i != 0 && i != 5) {
			panic(fmt.Sprintf("command outcome %d misclassified", i))
		}
		if i < 6 {
			var result map[string]any
			if err := json.Unmarshal(event.Result, &result); err != nil {
				panic(err)
			}
			expected := []any{float64(0), float64(1), float64(-7), nil, nil, float64(0)}
			if result["exitCode"] != expected[i] || result["command"] != "git diff HEAD~1" || result["cwd"] != "/workspace/repo" {
				panic(fmt.Sprintf("command result %d lost evidence: %s", i, event.Result))
			}
		}

	}
}
