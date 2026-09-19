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
	"sync/atomic"
	"testing"
	"time"
)

func TestClientOrdersNotificationBeforeResponseBarrier(t *testing.T) {

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

	select {
	case message := <-inbound:
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no barrier was delivered")
	}

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

func TestClientFailsPendingCallsWithoutCloserWhenAnotherCallCancels(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, clientWriterUnused := io.Pipe()
	defer clientWriterUnused.Close()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	_ = client.Inbound()
	go func() { _, _ = io.Copy(io.Discard, serverReader) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan error, 1)
	go func() { cancelled <- client.CallID(ctx, IntegerID(1), "a", nil, nil) }()
	other := make(chan error, 1)
	go func() { other <- client.CallID(context.Background(), IntegerID(2), "b", nil, nil) }()

	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled call err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled call never returned")
	}
	select {
	case err := <-other:
		if err == nil {
			t.Fatal("the other call reported success with no response")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the other pending call hung waiting for a reader nothing can interrupt")
	}
}

func TestClientCallSurvivesResponseFollowedByMalformedFrame(t *testing.T) {
	const runs = 500
	for i := 0; i < runs; i++ {
		serverReader, clientWriter := io.Pipe()
		clientReader, serverWriter := io.Pipe()
		client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
		inbound := client.Inbound()
		go func() {
			for {
				select {
				case message := <-inbound:
					if message.Barrier != nil {
						close(message.Barrier)
					}
				case <-client.ReadDone():
					return
				}
			}
		}()
		go func() {
			_, _ = bufio.NewReader(serverReader).ReadString('\n')
			_, _ = serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n{not json\n"))
			_ = serverWriter.Close()
		}()
		if err := client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil); err != nil {
			t.Fatalf("run %d: the response lost to the transport's death: %v", i, err)
		}

		<-client.ReadDone()
		if client.Err() == nil {
			t.Fatalf("run %d: the malformed frame did not retire the client", i)
		}
		_ = client.Close()
		_ = serverReader.Close()
		_ = clientReader.Close()
	}
}

func TestClientWriteBlockedWithoutCloserReturnsWhenReaderRetires(t *testing.T) {
	clientReader, serverWriter := io.Pipe()
	blocked := &blockedPumpWriter{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(blocked.release)
	client := NewClient(clientReader, blocked, ClientOptions{QueueCapacity: 8})
	result := make(chan error, 1)
	go func() { result <- client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil) }()

	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("pump never entered Encode")
	}

	if _, err := serverWriter.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("call reported success for a frame that never fully left")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call hung inside write on a pump nothing can unblock")
	}
	_ = serverWriter.Close()
}

func TestClientRefusesResponseWhenBarrierCannotBeEnqueued(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 1})
	_ = client.Inbound()
	result := make(chan error, 1)
	go func() { result <- client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil) }()
	if _, err := bufio.NewReader(serverReader).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, serverReader) }()

	if _, err := serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":"r1","method":"reverse","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrNotificationQueue) {
			t.Fatalf("call settled with %v, want the queue overflow", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call never settled")
	}
	_ = serverWriter.Close()
	_ = serverReader.Close()
}

type transportCloseCounter struct {
	inner io.Closer
	calls atomic.Int32
}

func (c *transportCloseCounter) Close() error { c.calls.Add(1); return c.inner.Close() }

func TestClientClosesTransportExactlyOnce(t *testing.T) {
	for _, order := range []string{"close-then-shutdown", "shutdown-then-close"} {
		reader, writer := io.Pipe()
		closer := &transportCloseCounter{inner: reader}
		client := NewClient(reader, io.Discard, ClientOptions{CloseReadWriter: closer})
		_ = client.Inbound()
		if order == "close-then-shutdown" {
			_ = client.Close()
			client.shutdown(errors.New("late"))
		} else {
			client.shutdown(errors.New("direct"))
			_ = client.Close()
		}

		<-client.ReadDone()
		if n := closer.calls.Load(); n != 1 {
			t.Fatalf("%s: transport closed %d times, want exactly once", order, n)
		}
		_ = writer.Close()
	}
}

var errEncodeAfterDelivery = errors.New("encode failed after the frame was delivered")

type releasedFailingWriter struct {
	inner   io.Writer
	release chan struct{}
}

func (w *releasedFailingWriter) Write(p []byte) (int, error) {
	n, err := w.inner.Write(p)
	if err != nil {
		return n, err
	}
	<-w.release
	return n, errEncodeAfterDelivery
}

func TestClientCallSettlesOnResponseWhenEncodeFailsAfterDelivery(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	writer := &releasedFailingWriter{inner: clientWriter, release: make(chan struct{})}
	client := NewClient(clientReader, writer, ClientOptions{QueueCapacity: 8})
	inbound := client.Inbound()
	result := make(chan error, 1)
	go func() { result <- client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil) }()
	if _, err := bufio.NewReader(serverReader).ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	if _, err := serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}

	select {
	case message := <-inbound:
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no barrier was delivered")
	}
	close(writer.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("the answered frame lost to its encode failure: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call never settled")
	}
	_ = serverWriter.Close()
	_ = serverReader.Close()
}

func TestCallStartedReportsAdmissionForRemoteErrorAfterEncodeFailure(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	writer := &releasedFailingWriter{inner: clientWriter, release: make(chan struct{})}
	client := NewClient(clientReader, writer, ClientOptions{QueueCapacity: 8})
	inbound := client.Inbound()
	started := make(chan error, 1)
	result := make(chan error, 1)
	go func() { result <- client.CallStarted(context.Background(), "prompt.submit", nil, nil, started) }()
	if _, err := bufio.NewReader(serverReader).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := serverWriter.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":"bad"}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-inbound:
		if message.Barrier == nil {
			t.Fatalf("expected the response barrier first, got %+v", message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no barrier was delivered")
	}
	close(writer.release)
	var remote *RemoteError
	select {
	case err := <-result:
		if !errors.As(err, &remote) {
			t.Fatalf("call settled with %v, want the remote error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call never settled")
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("admission reported %v for a request the peer answered", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admission never reported")
	}
	_ = serverWriter.Close()
	_ = serverReader.Close()
}

type blockedPumpWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockedPumpWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestPumpSettlesIsPerRequest(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	client := NewClient(reader, io.Discard, ClientOptions{})
	past := writeRequest{started: make(chan struct{}), encoded: make(chan struct{}), result: make(chan error, 1)}
	close(past.started)
	close(past.encoded)
	blocked := writeRequest{started: make(chan struct{}), encoded: make(chan struct{}), result: make(chan error, 1)}
	close(blocked.started)
	if !client.pumpSettles(past) {
		t.Fatal("a frame past Encode must settle on its own result")
	}
	if client.pumpSettles(blocked) {
		t.Fatal("a frame still inside Encode without a closer must not be waited on")
	}
}
