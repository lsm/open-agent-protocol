package adapter_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func ExampleRunToTerminal() {
	memory := adapter.NewMemory(adapter.Config{})
	session, err := memory.Open(context.Background(), adapter.OpenRequest{
		SessionID:   "session-1",
		Participant: protocol.Participant{ID: "user"},
	})
	if err != nil {
		fmt.Println("open:", err)
		return
	}

	outcome, err := adapter.RunToTerminal(context.Background(), session, protocol.MessageSubmitRequest{
		SessionID: "session-1",
		Delivery:  protocol.DeliveryAuto,
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("review the diff")}},
	}, adapter.RunOptions{
		Policy: func(_ context.Context, gate adapter.Gate) (adapter.GateAnswer, error) {
			if gate.Permission != nil {
				return adapter.GateAnswer{ChoiceID: gate.Permission.Choices[0].ID, Granted: true}, nil
			}
			if len(gate.UserInput.Questions) == 0 {
				return adapter.GateAnswer{}, errors.New("the run asked a question it cannot phrase")
			}
			return adapter.GateAnswer{Answers: []protocol.InputAnswer{{
				QuestionID:        gate.UserInput.Questions[0].ID,
				SelectedOptionIDs: []string{gate.UserInput.Questions[0].Options[0].ID},
			}}}, nil
		},
	})
	if err != nil {
		fmt.Println("run:", err)
		return
	}
	fmt.Println(outcome.Text)
	fmt.Println(outcome.ToolCalls, "tool call")
	// Output:
	// The golden script completed.
	// 1 tool call
}
