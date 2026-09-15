package rpc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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

// A teardown drain reacquires the inbound stream. It must not need the route
// lock, which the reader holds while it waits for that same consumer to
// acknowledge a response barrier.
func TestClientInboundDoesNotBlockWhileReaderWaitsOnBarrier(t *testing.T) {
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
	if _, err := remote.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"messageId":"m"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	// Consume the barrier without acknowledging it so the reader stays parked in
	// route(), holding the route lock.
	select {
	case message := <-in:
		if message.Barrier == nil {
			t.Fatalf("expected response barrier, got %#v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no barrier delivered")
	}
	acquired := make(chan struct{})
	go func() { _ = client.Inbound(); close(acquired) }()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("Inbound blocked while the reader held the route lock")
	}
	client.Close()
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

// Regression: a response parsed off the ordered stream before the transport
// died must win over the death. shutdown used to retire the pending map while
// the reader was still parked in the response barrier, so the genuine reply —
// already decoded from the wire — was discarded, the call returned the
// shutdown reason, and the unmatched-id path raised ErrResponseNotFound over
// the real cause.
func TestClientDeliversResponseParkedInBarrierWhenTransportRetires(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	inbound := client.Inbound()
	results := make(chan error, 1)
	go func() {
		var result struct {
			Status string `json:"status"`
		}
		err := client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, &result)
		if err == nil && result.Status != "streaming" {
			err = fmt.Errorf("status = %q", result.Status)
		}
		results <- err
	}()
	// Drain the whole request line, then push a notification through the same
	// serialized writer and drain that too. The writer hands back each frame's
	// result before it accepts the next, so once the notification is on the
	// wire the call is certainly parked on its response rather than still
	// inside a write that the close would fail.
	serverLines := bufio.NewReader(serverReader)
	if _, err := serverLines.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	synced := make(chan error, 1)
	go func() { synced <- client.Notify(context.Background(), "sync", nil) }()
	if _, err := serverLines.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, serverLines) }()
	if _, err := serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"streaming"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	// Take the barrier without acknowledging it: the reader is now parked
	// holding a fully decoded response for request 1.
	select {
	case message := <-inbound:
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no barrier was delivered")
	}
	// The transport dies underneath the parked reader.
	client.Close()
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("the parked response lost to the transport's death: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call never settled")
	}
	if err := client.Err(); errors.Is(err, ErrResponseNotFound) {
		t.Fatalf("a concurrently retired id was reported as unmatched: %v", err)
	}
	_ = serverWriter.Close()
}
