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

func ask(t *testing.T, client *Client, id string) protocol.EnvelopeID {
	t.Helper()
	request, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, protocol.EnvelopeID(id), protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	request.SessionID = "budget"
	if err = client.Send(request); err != nil {
		t.Fatal(err)
	}
	return request.ID
}

func awaitLiveness(t *testing.T, client *Client) {
	t.Helper()
	deadline := time.Now().Add(budgetHardCap)
	for {
		if _, err := client.Event(); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the chatty peer never produced a line, so this cannot tell a renewed budget from a peer that was never chatty")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func expectBudgetSpent(t *testing.T, what string, start time.Time, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("a peer that never answered the %s was reported as having answered it", what)
	}
	elapsed := time.Since(start)
	if elapsed < budgetForTest {
		t.Fatalf("the %s wait gave up after %s without spending its %s budget, so it stopped on something other than the deadline", what, elapsed, budgetForTest)
	}
	if elapsed > budgetForTest+budgetHardCap {
		t.Fatalf("the %s wait took %s against a %s budget, so frames renewed it", what, elapsed, budgetForTest)
	}
}

func TestAPeerThatNeverAnswersGetsOneBudgetNotOnePerLineItSends(t *testing.T) {
	for _, mode := range []struct {
		name   string
		chatty bool
	}{
		{"chatty", true},
		{"silent", false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			client := shortDeadlineClient(t, mode.name)
			ask(t, client, "budget-req")
			if mode.chatty {
				awaitLiveness(t, client)
			}
			start := time.Now()
			err := waitWithin(t, "response", func() error {
				_, err := client.Response("budget-req")
				return err
			})
			expectBudgetSpent(t, "response", start, err)
			if !strings.Contains(err.Error(), budgetForTest.String()) {
				t.Fatalf("the failure does not name the budget the peer was given: %v", err)
			}
		})
	}
}

func TestAControlWaitIsNotRenewedByRealUnrelatedControlFrames(t *testing.T) {
	client := shortDeadlineClient(t, "chatty-controls")
	after := uint64(0)
	if err := client.SendControl(ControlFrame{Control: "replay", ID: "budget-ctl-2", SessionID: "budget", RunID: "run-y", After: &after}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := waitWithin(t, "control", func() error {
		_, err := client.Control("budget-ctl-2")
		return err
	})
	if !errors.Is(err, ErrControlUnanswered) {
		t.Fatalf("an unanswered control is %v, want ErrControlUnanswered so the runner can still skip it", err)
	}
	expectBudgetSpent(t, "control", start, err)
}

func TestAMatchingAnswerAlreadyBufferedIsReturnedWithoutSpendingTheBudget(t *testing.T) {
	client, err := Spawn(context.Background(), helperCommand(t, "answers-each")[0], nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	firstID := ask(t, client, "budget-buf-1")
	secondID := ask(t, client, "budget-buf-2")
	if _, err := client.Response(secondID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	answer, err := client.Response("budget-buf-1")
	if err != nil {
		t.Fatalf("an answer the peer had already sent was not returned from the buffer: %v", err)
	}
	elapsed := time.Since(start)
	if answer.InReplyTo != firstID {
		t.Fatalf("the buffered answer is in reply to %q, not the request that drew it", answer.InReplyTo)
	}
	if elapsed > budgetForTest/2 {
		t.Fatalf("a buffered match took %s, so it was not served from the buffer", elapsed)
	}
}

func TestAnEventWaitIsNotRenewedByRepliesToNobody(t *testing.T) {
	client := shortDeadlineClient(t, "chatty-responses")
	ask(t, client, "budget-evt")
	before := len(client.Transcript())
	start := time.Now()
	err := waitWithin(t, "event", func() error {
		_, err := client.Event()
		return err
	})
	if err == nil {
		t.Fatal("an event appeared for a peer that only ever sent replies to nobody")
	}
	elapsed := time.Since(start)
	if elapsed < budgetForTest {
		t.Fatalf("the event wait gave up after %s without spending its %s budget", elapsed, budgetForTest)
	}
	if elapsed > budgetForTest+budgetHardCap {
		t.Fatalf("replies renewed the event budget for %s", elapsed)
	}
	if !strings.Contains(err.Error(), budgetForTest.String()) {
		t.Fatalf("the failure does not name the budget: %v", err)
	}
	if grew := len(client.Transcript()) - before; grew < 2 {
		t.Fatalf("the peer managed only %d frame(s) during the wait, so this case never saw a chatty peer and cannot tell a renewed budget from a silent one", grew)
	}
}
