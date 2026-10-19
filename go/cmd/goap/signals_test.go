package main

import (
	"context"
	"io"
	"os"
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
