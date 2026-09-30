package conformance

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func runEventEnvelopes(run protocol.RunID, events ...[3]any) []protocol.Envelope {
	envelopes := make([]protocol.Envelope, 0, len(events))
	for _, event := range events {
		sequence := event[1].(uint64)
		envelopes = append(envelopes, protocol.Envelope{
			ID:       event[2].(protocol.EnvelopeID),
			RunID:    run,
			Type:     event[0].(protocol.EnvelopeType),
			Sequence: &sequence,
		})
	}
	return envelopes
}

func acceptedByFirstSequenceAndAnyTerminal(original, replayed []protocol.Envelope) bool {
	first := uint64(0)
	for _, event := range replayed {
		if first == 0 && event.Sequence != nil {
			first = *event.Sequence
		}
		switch event.Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			return first == 1
		}
	}
	return false
}

func TestReplayMembershipRejectsWhatTheFirstSequenceAndAnyTerminalPredicateAccepted(t *testing.T) {
	run := protocol.RunID("run-1")
	original := runEventSignature(runEventEnvelopes(run,
		[3]any{protocol.TypeRunStarted, uint64(1), protocol.EnvelopeID("evt-1")},
		[3]any{protocol.TypeRunStatusUpdated, uint64(2), protocol.EnvelopeID("evt-2")},
		[3]any{protocol.TypeRunStatusUpdated, uint64(3), protocol.EnvelopeID("evt-3")},
		[3]any{protocol.TypeRunCompleted, uint64(4), protocol.EnvelopeID("evt-4")},
	), run)
	if len(original) != 4 {
		t.Fatalf("the original run recorded %d events, want 4", len(original))
	}

	cases := []struct {
		name     string
		replayed []protocol.Envelope
	}{
		{
			name: "the run's middle events are dropped",
			replayed: runEventEnvelopes(run,
				[3]any{protocol.TypeRunStarted, uint64(1), protocol.EnvelopeID("evt-1")},
				[3]any{protocol.TypeRunCompleted, uint64(4), protocol.EnvelopeID("evt-4")},
			),
		},
		{
			name: "the run's events arrive in a different order",
			replayed: runEventEnvelopes(run,
				[3]any{protocol.TypeRunStarted, uint64(1), protocol.EnvelopeID("evt-1")},
				[3]any{protocol.TypeRunStatusUpdated, uint64(3), protocol.EnvelopeID("evt-3")},
				[3]any{protocol.TypeRunStatusUpdated, uint64(2), protocol.EnvelopeID("evt-2")},
				[3]any{protocol.TypeRunCompleted, uint64(4), protocol.EnvelopeID("evt-4")},
			),
		},
		{
			name: "a status event is replayed as a different type",
			replayed: runEventEnvelopes(run,
				[3]any{protocol.TypeRunStarted, uint64(1), protocol.EnvelopeID("evt-1")},
				[3]any{protocol.TypeRunStatusUpdated, uint64(2), protocol.EnvelopeID("evt-2")},
				[3]any{protocol.TypeRunFailed, uint64(3), protocol.EnvelopeID("evt-3")},
				[3]any{protocol.TypeRunCompleted, uint64(4), protocol.EnvelopeID("evt-4")},
			),
		},
		{
			name: "every envelope is re-issued under a fresh id",
			replayed: runEventEnvelopes(run,
				[3]any{protocol.TypeRunStarted, uint64(1), protocol.EnvelopeID("fresh-1")},
				[3]any{protocol.TypeRunStatusUpdated, uint64(2), protocol.EnvelopeID("fresh-2")},
				[3]any{protocol.TypeRunStatusUpdated, uint64(3), protocol.EnvelopeID("fresh-3")},
				[3]any{protocol.TypeRunCompleted, uint64(4), protocol.EnvelopeID("fresh-4")},
			),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			replayed := runEventSignature(testCase.replayed, run)
			if !acceptedByFirstSequenceAndAnyTerminal(testCase.replayed, testCase.replayed) {
				t.Fatal("this case does not reproduce the weakness: the old predicate already rejected it")
			}
			if index, detail := firstDivergence(original, replayed); index < 0 {
				t.Fatalf("a replay that %s was accepted as the run's own history", testCase.name)
			} else if detail == "" {
				t.Fatal("a divergence was reported with no detail, so the operator is told nothing")
			}
		})
	}
}

func TestReplayMembershipAcceptsTheRunExactlyAsItHappened(t *testing.T) {
	run := protocol.RunID("run-2")
	history := runEventEnvelopes(run,
		[3]any{protocol.TypeRunStarted, uint64(1), protocol.EnvelopeID("evt-1")},
		[3]any{protocol.TypeRunStatusUpdated, uint64(2), protocol.EnvelopeID("evt-2")},
		[3]any{protocol.TypeRunCompleted, uint64(3), protocol.EnvelopeID("evt-3")},
	)
	original := runEventSignature(history, run)
	if index, detail := firstDivergence(original, runEventSignature(history, run)); index >= 0 {
		t.Fatalf("the run's own history was reported as a divergence at %d: %s", index, detail)
	}
}

func TestRunEventSignatureStopsAtTheRunTerminal(t *testing.T) {
	run := protocol.RunID("run-3")
	signature := runEventSignature(runEventEnvelopes(run,
		[3]any{protocol.TypeRunStarted, uint64(1), protocol.EnvelopeID("evt-1")},
		[3]any{protocol.TypeRunCompleted, uint64(2), protocol.EnvelopeID("evt-2")},
		[3]any{protocol.TypeRunStatusUpdated, uint64(3), protocol.EnvelopeID("evt-3")},
	), run)
	if len(signature) != 2 {
		t.Fatalf("the signature is %d events, want 2: a run that completed does not go on to report status", len(signature))
	}
}

func TestRunEventSignatureIgnoresOtherRunsAndUnsequencedEnvelopes(t *testing.T) {
	run := protocol.RunID("run-4")
	other := uint64(7)
	unsequenced := []protocol.Envelope{
		{RunID: protocol.RunID("run-5"), Type: protocol.TypeRunStarted, Sequence: &other},
		{RunID: run, Type: protocol.TypeSessionStateResponse},
		{RunID: run, Type: protocol.TypeRunStarted, Sequence: sequenceOf(1)},
		{RunID: run, Type: protocol.TypeRunCompleted, Sequence: sequenceOf(2)},
	}
	signature := runEventSignature(unsequenced, run)
	if len(signature) != 2 {
		t.Fatalf("the signature is %d events, want 2: another run's events and unsequenced envelopes are not this run's", len(signature))
	}
}

func sequenceOf(value uint64) *uint64 {
	return &value
}

const replayCheckName = "a cursor replay is accepted and re-delivers the run"

func replayCheck(t *testing.T, report *Report) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == replayCheckName {
			return check
		}
	}
	t.Fatalf("the runner reported no %q check at all, so the replay was never exercised", replayCheckName)
	return Check{}
}

func runHelper(t *testing.T, mode string) *Report {
	t.Helper()
	report, err := Run(context.Background(), Options{
		Command: helperCommand(t, mode),
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestRunnerRefusesAReplayThatDropsTheRunsEvents(t *testing.T) {
	check := replayCheck(t, runHelper(t, "replay-drops-middle"))
	if check.Passed {
		t.Fatal("the runner accepted a replay that dropped two of the run's four events")
	}
	for _, want := range []string{"event 1", string(protocol.TypeRunCompleted), string(protocol.TypeRunStatusUpdated)} {
		if !strings.Contains(check.Detail, want) {
			t.Fatalf("the rejection does not name %q, so the operator cannot tell what the replay got wrong: %q", want, check.Detail)
		}
	}
}

func TestRunnerRefusesAReplayThatRenamesTheRunsEnvelopes(t *testing.T) {
	check := replayCheck(t, runHelper(t, "replay-renames-ids"))
	if check.Passed {
		t.Fatal("the runner accepted a replay carrying fresh envelope ids, which the contract does not treat as the run it already has")
	}
	if !strings.Contains(check.Detail, "helper-replay-") {
		t.Fatalf("the rejection does not name the id the endpoint invented: %q", check.Detail)
	}
}

func TestRunnerAcceptsAReplayThatReDeliversTheSameEnvelopes(t *testing.T) {
	check := replayCheck(t, runHelper(t, "replay-faithful"))
	if !check.Passed {
		t.Fatalf("the runner refused a faithful replay of the run's own envelopes: %q", check.Detail)
	}
}

func TestRunnerSkipsAnEndpointThatDeclinesTheReplayControl(t *testing.T) {
	check := replayCheck(t, runHelper(t, "declines-controls"))
	if !check.Skipped {
		t.Fatalf("an endpoint that declines the control is not a failed replay, but the check was passed=%v skipped=%v: %q", check.Passed, check.Skipped, check.Detail)
	}
}
