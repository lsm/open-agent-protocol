package rpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientOrdersNotificationBeforeResponseBarrier(t *testing.T) {
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"method\":\"session.status\",\"params\":{\"sessionId\":\"s\",\"status\":\"running\"}}\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"messageId\":\"m\"}}\n")
	client := NewClient(input, io.Discard, ClientOptions{})
	in := client.Inbound()
	done := make(chan error, 1)
	go func() {
		var result struct {
			MessageID string `json:"messageId"`
		}
		done <- client.CallID(context.Background(), IntegerID(1), "session/prompt", map[string]any{}, &result)
	}()
	first := <-in
	if first.Notification == nil || first.Notification.Method != "session.status" {
		t.Fatalf("first %#v", first)
	}
	barrier := <-in
	if barrier.Barrier == nil {
		t.Fatalf("second %#v", barrier)
	}
	select {
	case err := <-done:
		t.Fatalf("response crossed barrier: %v", err)
	default:
	}
	close(barrier.Barrier)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClientFatalUnmatchedResponse(t *testing.T) {
	client := NewClient(strings.NewReader(`{"jsonrpc":"2.0","id":"lost","result":{}}`+"\n"), io.Discard, ClientOptions{})
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("client did not close")
	}
	if !errors.Is(client.Err(), ErrResponseNotFound) {
		t.Fatalf("got %v", client.Err())
	}
}

func TestClientCancellationAfterWriteIsFatal(t *testing.T) {
	reader, remote := io.Pipe()
	defer remote.Close()
	output := &lockedShortWriter{}
	client := NewClient(reader, output, ClientOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, "session/prompt", map[string]string{"sessionId": "s"}, nil) }()
	deadline := time.Now().Add(time.Second)
	for output.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("expected cancellation")
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("client remained reusable")
	}
}

func TestClientSerializesPartialConcurrentWrites(t *testing.T) {
	reader, remote := io.Pipe()
	defer remote.Close()
	writer := &lockedShortWriter{}
	client := NewClient(reader, writer, ClientOptions{WriteQueueCapacity: 32})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = client.Notify(ctx, "session/status", map[string]int{"n": i}) }()
	}
	wg.Wait()
	for _, line := range strings.Split(strings.TrimSpace(writer.String()), "\n") {
		if _, err := ParseMessage([]byte(line)); err != nil {
			t.Fatalf("interleaved %q: %v", line, err)
		}
	}
}

type lockedShortWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *lockedShortWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(p) > 3 {
		p = p[:3]
	}
	return w.b.Write(p)
}
func (w *lockedShortWriter) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.b.String() }
func (w *lockedShortWriter) Len() int       { w.mu.Lock(); defer w.mu.Unlock(); return w.b.Len() }
