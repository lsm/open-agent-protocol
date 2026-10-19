package main

import (
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type parkedTaker struct{ release chan struct{} }

func (p parkedTaker) Signal(context.Context, int) (protocol.EndpointShutdownResponse, bool) {
	<-p.release
	return protocol.EndpointShutdownResponse{}, false
}

func TestAThirdSignalEndsServingEvenWhileAnEarlierOneIsParked(t *testing.T) {
	taker := parkedTaker{release: make(chan struct{})}
	defer close(taker.release)
	caught := make(chan os.Signal, 4)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go relaySignals(ctx, stop, caught, taker, io.Discard)
	for range 3 {
		caught <- syscall.SIGTERM
	}
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the third signal did not end serving while the first was parked")
	}
}

func TestTheSignalLineListsEachActiveRunAndSaysWhatTheNextSignalDoes(t *testing.T) {
	answer := protocol.EndpointShutdownResponse{
		ActiveRuns: []protocol.ShutdownRun{{SessionID: "s1", RunID: "r1"}, {SessionID: "s2", RunID: "r2"}},
		Lost:       []protocol.ShutdownQueuedRun{{SessionID: "s1", RunID: "r3"}},
	}
	graceful := signalLine(1, answer)
	if !strings.Contains(graceful, "still active: s1/r1, s2/r2") || !strings.Contains(graceful, "1 queued run(s) lost") || !strings.Contains(graceful, "signal again to force") {
		t.Fatalf("the graceful line is %q", graceful)
	}
	if forced := signalLine(2, answer); !strings.Contains(forced, "forced shutdown; cut off: s1/r1, s2/r2") {
		t.Fatalf("the forced line is %q", forced)
	}
	if idle := signalLine(1, protocol.EndpointShutdownResponse{}); !strings.Contains(idle, "still active: none") {
		t.Fatalf("the idle line is %q", idle)
	}
}
