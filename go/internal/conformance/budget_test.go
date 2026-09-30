package conformance

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const budgetForTest = 4 * time.Second

const budgetHardCap = 15 * time.Second

func waitWithin(t *testing.T, what string, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(budgetHardCap):
		t.Fatalf("the %s wait was still running after %s: the endpoint produced lines, and each one was handed a fresh budget, so the deadline never arrived", what, budgetHardCap)
		return nil
	}
}

func shortDeadlineClient(t *testing.T, mode string) *Client {
	t.Helper()
	client, err := SpawnWithDeadline(context.Background(), helperCommand(t, mode)[0], nil, io.Discard, budgetForTest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func awaitLiveness(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.Now().Add(budgetHardCap)
	for {
		if _, err := client.Event(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the chatty peer never produced a line, so this case cannot tell a renewed budget from a peer that was never chatty")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAChattyPeerCannotRenewTheWaitForAnUnansweredResponse(t *testing.T) {
	client := shortDeadlineClient(t, "chatty")
	open, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "budget-req-1", protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	open.SessionID = "budget"
	if err := client.Send(open); err != nil {
		t.Fatal(err)
	}
	awaitLiveness(t, client)
	start := time.Now()
	err = waitWithin(t, "response", func() error {
		_, err := client.Response("budget-req-1")
		return err
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a peer that never answered was reported as having answered")
	}
	if !strings.Contains(err.Error(), budgetForTest.String()) {
		t.Fatalf("the failure does not name the budget the peer was given: %v", err)
	}
	if elapsed < budgetForTest {
		t.Fatalf("the wait gave up after %s without spending its %s budget, so it stopped on something other than the deadline", elapsed, budgetForTest)
	}
	if elapsed > budgetForTest+10*time.Second {
		t.Fatalf("the wait took %s against a %s budget, so frames renewed it", elapsed, budgetForTest)
	}
}

func TestASilentPeerEndsTheWaitOnItsBudget(t *testing.T) {
	client := shortDeadlineClient(t, "silent")
	open, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "budget-req-2", protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	open.SessionID = "budget"
	if err := client.Send(open); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = waitWithin(t, "response", func() error {
		_, err := client.Response("budget-req-2")
		return err
	})
	if err == nil {
		t.Fatal("a silent peer was reported as having answered")
	}
	if !strings.Contains(err.Error(), budgetForTest.String()) {
		t.Fatalf("the failure does not name the budget: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < budgetForTest {
		t.Fatalf("the wait gave up after %s without spending its %s budget", elapsed, budgetForTest)
	}
	if elapsed > budgetForTest+10*time.Second {
		t.Fatalf("a silent peer held the wait for %s against a %s budget", elapsed, budgetForTest)
	}
}

func TestAControlWaitIsNotRenewedByUnrelatedControlFrames(t *testing.T) {
	client := shortDeadlineClient(t, "chatty")
	open, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "budget-req-3", protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	open.SessionID = "budget"
	if err := client.Send(open); err != nil {
		t.Fatal(err)
	}
	after := uint64(0)
	if err := client.SendControl(ControlFrame{Control: "replay", ID: "budget-ctl-1", SessionID: "budget", RunID: "run-x", After: &after}); err != nil {
		t.Fatal(err)
	}
	awaitLiveness(t, client)
	start := time.Now()
	err = waitWithin(t, "control", func() error {
		_, err := client.Control("budget-ctl-1")
		return err
	})
	if !errors.Is(err, ErrControlUnanswered) {
		t.Fatalf("an unanswered control is %v, want ErrControlUnanswered so the runner can still skip it", err)
	}
	if elapsed := time.Since(start); elapsed > ControlAnswerBudget+10*time.Second {
		t.Fatalf("unrelated frames renewed the control budget for %s", elapsed)
	}
}

func TestAnAnswerThatIsAlreadyBufferedIsStillReturned(t *testing.T) {
	client, err := Spawn(context.Background(), helperCommand(t, "early-events")[0], nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	open, err := protocol.NewEnvelope(protocol.TypeSessionOpenRequest, "budget-req-4", protocol.SessionOpenRequest{SessionID: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	open.SessionID = "budget"
	if err := client.Send(open); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Response(open.ID); err != nil {
		t.Fatalf("an answer the helper had already sent was not returned: %v", err)
	}
}
