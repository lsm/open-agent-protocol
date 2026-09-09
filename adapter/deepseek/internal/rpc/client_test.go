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
	// A static reader lets the read loop parse the response before the call
	// registers its pending entry, so synchronize on the pipe: the fixture
	// writes the reply only after the request write completed.
	reader, remote := io.Pipe()
	defer remote.Close()
	client := NewClient(reader, io.Discard, ClientOptions{})
	in := client.Inbound()
	started := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		var result struct {
			MessageID string `json:"messageId"`
		}
		done <- client.CallStarted(context.Background(), "session/prompt", map[string]string{"sessionId": "s"}, &result, started)
	}()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"session.status\",\"params\":{\"sessionId\":\"s\",\"status\":\"running\"}}\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"messageId\":\"m\"}}\n")); err != nil {
		t.Fatal(err)
	}
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

func TestClientRejectsWriteAfterClose(t *testing.T) {
	reader, remote := io.Pipe()
	defer remote.Close()
	output := &lockedShortWriter{}
	client := NewClient(reader, output, ClientOptions{})
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), "session/prompt", map[string]string{"sessionId": "s"}, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
	deadline := time.Now().Add(100 * time.Millisecond)
	for output.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if output.Len() != 0 {
		t.Fatalf("bytes written after close: %q", output.String())
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
