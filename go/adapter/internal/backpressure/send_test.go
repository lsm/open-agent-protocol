package backpressure

import (
	"testing"
	"time"
)

func TestSendWaitsForRoomRatherThanFailing(t *testing.T) {
	queue := make(chan int, 1)
	queue <- 1
	go func() {
		time.Sleep(20 * time.Millisecond)
		<-queue
	}()
	if outcome := Send(queue, 2, nil, 5*time.Second); outcome != Sent {
		t.Fatalf("outcome = %v, want Sent once the consumer made room", outcome)
	}
}

func TestSendStallsOnlyPastTheBound(t *testing.T) {
	queue := make(chan int, 1)
	queue <- 1
	started := time.Now()
	if outcome := Send(queue, 2, nil, 50*time.Millisecond); outcome != Stalled {
		t.Fatalf("outcome = %v, want Stalled", outcome)
	}
	if waited := time.Since(started); waited < 50*time.Millisecond {
		t.Fatalf("stalled after %v, before the 50ms bound", waited)
	}
}

func TestSendStopsWaitingWhenTheClientCloses(t *testing.T) {
	queue := make(chan int, 1)
	queue <- 1
	done := make(chan struct{})
	close(done)
	if outcome := Send(queue, 2, done, 5*time.Second); outcome != Closed {
		t.Fatalf("outcome = %v, want Closed", outcome)
	}
}
