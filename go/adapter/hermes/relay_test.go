package hermes

import (
	"context"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
)

type relaySource struct {
	in       chan rpc.InboundMessage
	done     chan struct{}
	readDone chan struct{}
}

func (s *relaySource) Call(context.Context, string, any, any) error { return nil }
func (s *relaySource) Inbound() <-chan rpc.InboundMessage           { return s.in }
func (s *relaySource) Done() <-chan struct{}                        { return s.done }
func (s *relaySource) ReadDone() <-chan struct{}                    { return s.readDone }
func (s *relaySource) Err() error                                   { return nil }
func (s *relaySource) Close() error                                 { return nil }

func TestInboundRelayDrainsFramesDecodedBeforeReaderStops(t *testing.T) {
	source := &relaySource{in: make(chan rpc.InboundMessage, 8), done: make(chan struct{}), readDone: make(chan struct{})}
	relay := make(chan rpc.InboundMessage, 8)
	go relayInbound(source, relay)
	close(source.done)

	time.Sleep(50 * time.Millisecond)

	source.in <- rpc.InboundMessage{Notification: &rpc.NotificationMessage{Method: "event"}}
	close(source.readDone)
	select {
	case message, ok := <-relay:
		if !ok || message.Notification == nil {
			t.Fatal("frame decoded before the reader stopped was dropped")
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not forward the decoded frame")
	}
}
