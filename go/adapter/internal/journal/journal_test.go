package journal

import (
	"encoding/json"
	"errors"
	"runtime"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func envelope(run protocol.RunID, sequence uint64) protocol.Envelope {
	return protocol.Envelope{Type: protocol.TypeContentDelta, RunID: run, Sequence: &sequence, Payload: json.RawMessage(`{"n":1}`)}
}

func appendRun(j *Journal, run protocol.RunID, count uint64, terminal bool) {
	for sequence := uint64(1); sequence <= count; sequence++ {
		j.Append(envelope(run, sequence), terminal && sequence == count)
	}
}

func collect(t *testing.T, stream base.EventStream) ([]protocol.Envelope, error) {
	t.Helper()
	var envelopes []protocol.Envelope
	var streamErr error
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case result, ok := <-stream:
			if !ok {
				return envelopes, streamErr
			}
			if result.Error != nil {
				streamErr = result.Error
				continue
			}
			envelopes = append(envelopes, result.Envelope)
		case <-timer.C:
			t.Fatal("stream did not close")
			return nil, nil
		}
	}
}

func assertContiguous(t *testing.T, envelopes []protocol.Envelope, count int) {
	t.Helper()
	if len(envelopes) != count {
		t.Fatalf("delivered %d envelopes, want %d", len(envelopes), count)
	}
	for index, envelope := range envelopes {
		if *envelope.Sequence != uint64(index+1) {
			t.Fatalf("envelope %d carries sequence %d", index, *envelope.Sequence)
		}
	}
}

func TestAStalledFollowerReceivesEveryRetainedEnvelopeWithoutOverflow(t *testing.T) {
	j := New(1000)
	stream := j.Follow("run", 0)
	appendRun(j, "run", 500, true)
	envelopes, err := collect(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	assertContiguous(t, envelopes, 500)
}

func TestAFollowerWhoseNextEnvelopeWasEvictedOverflowsAndResumeReportsAGap(t *testing.T) {
	j := New(16)
	stream := j.Follow("run", 0)
	appendRun(j, "run", 100, false)
	envelopes, err := collect(t, stream)
	if !errors.Is(err, base.ErrEventStreamOverflow) {
		t.Fatalf("stream closed with %v", err)
	}
	if len(envelopes) < followerBuffer || len(envelopes) >= 85 {
		t.Fatalf("delivered %d envelopes before overflowing", len(envelopes))
	}
	assertContiguous(t, envelopes, len(envelopes))
	cursor := uint64(len(envelopes))
	_, _, err = j.Resume(protocol.SessionState{}, "run", cursor, 100)
	var gap *base.ReplayGap
	if !errors.As(err, &gap) || *gap != (base.ReplayGap{RequestedAfter: cursor, OldestAvailable: 85, LatestAvailable: 100}) {
		t.Fatalf("resume = %v", err)
	}
}

func TestDeliveredEnvelopesAreCopiesOfTheJournal(t *testing.T) {
	j := New(8)
	appendRun(j, "run", 1, true)
	first, _ := collect(t, j.Follow("run", 0))
	first[0].Payload[1] = 'X'
	*first[0].Sequence = 99
	second, _ := collect(t, j.Follow("run", 0))
	if string(second[0].Payload) != `{"n":1}` || *second[0].Sequence != 1 {
		t.Fatalf("replay after vandalism = %s at %d", second[0].Payload, *second[0].Sequence)
	}
}

func TestCloseEndsWaitingBlockedAndUnboundFollowers(t *testing.T) {
	before := runtime.NumGoroutine()
	j := New(1000)
	waiting := j.Follow("idle", 0)
	blocked := j.Follow("busy", 0)
	appendRun(j, "busy", 200, false)
	unbound := j.Reserve().Stream()
	j.Close()
	for _, stream := range []base.EventStream{waiting, blocked, unbound} {
		if _, err := collect(t, stream); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines outlived close, from %d", runtime.NumGoroutine(), before)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAReservationDeliversTheRunItIsBoundTo(t *testing.T) {
	j := New(64)
	reservation := j.Reserve()
	appendRun(j, "other", 3, true)
	reservation.Bind("run")
	appendRun(j, "run", 5, true)
	envelopes, err := collect(t, reservation.Stream())
	if err != nil {
		t.Fatal(err)
	}
	assertContiguous(t, envelopes, 5)
	for _, envelope := range envelopes {
		if envelope.RunID != "run" {
			t.Fatalf("delivered an envelope of %s", envelope.RunID)
		}
	}
}
