package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (writer *blockingWriter) Write(data []byte) (int, error) {
	writer.once.Do(func() { close(writer.entered) })
	<-writer.release
	return len(data), nil
}

type pipeCloser struct {
	in  *io.PipeReader
	out *io.PipeWriter
}

func (closer pipeCloser) Close() error {
	_ = closer.in.Close()
	return closer.out.Close()
}

func clientPipes(t *testing.T, capacity int) (*Client, *bufio.Reader, *io.PipeWriter) {
	t.Helper()
	serverToClientReader, serverToClientWriter := io.Pipe()
	clientToServerReader, clientToServerWriter := io.Pipe()
	client := NewClient(serverToClientReader, clientToServerWriter, ClientOptions{
		QueueCapacity:   capacity,
		CloseReadWriter: pipeCloser{in: serverToClientReader, out: clientToServerWriter},
	})
	t.Cleanup(func() {
		_ = client.Close()
		_ = clientToServerReader.Close()
		_ = serverToClientWriter.Close()
	})
	return client, bufio.NewReader(clientToServerReader), serverToClientWriter
}

func readWire(t *testing.T, reader *bufio.Reader) Message {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	message, err := ParseMessage(line[:len(line)-1])
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func writeWire(t *testing.T, writer io.Writer, message Message) {
	t.Helper()
	if err := NewEncoder(writer).Encode(message); err != nil {
		t.Fatal(err)
	}
}

func TestClientCorrelatesOutOfOrderCalls(t *testing.T) {
	client, reader, writer := clientPipes(t, 8)
	type outcome struct {
		value string
		err   error
	}
	results := make(chan outcome, 2)
	for _, method := range []string{"first", "second"} {
		method := method
		go func() {
			var response struct {
				Value string `json:"value"`
			}
			err := client.Call(context.Background(), method, nil, &response)
			results <- outcome{value: response.Value, err: err}
		}()
	}
	one := readWire(t, reader)
	two := readWire(t, reader)
	writeWire(t, writer, Response(two.ID, json.RawMessage(`{"value":"`+two.Method+`"}`)))
	writeWire(t, writer, Response(one.ID, json.RawMessage(`{"value":"`+one.Method+`"}`)))
	seen := map[string]bool{}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		seen[result.value] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("wrong results: %v", seen)
	}
}

func TestClientRoutesNotificationAndReverseRequest(t *testing.T) {
	client, reader, writer := clientPipes(t, 8)
	writeWire(t, writer, Notification("turn/started", json.RawMessage(`{"turn":{"id":"t1"}}`)))
	writeWire(t, writer, Message{Kind: MessageRequest, ID: StringID("approval"), Method: "item/commandExecution/requestApproval", Params: json.RawMessage(`{"turnId":"t1"}`), Trace: json.RawMessage(`{"traceparent":"x"}`)})

	first := <-client.Inbound()
	if first.Request != nil || first.Notification == nil || first.Notification.Method != "turn/started" {
		t.Fatalf("first inbound: %+v", first)
	}
	second := <-client.Inbound()
	request := second.Request
	if request == nil || second.Notification != nil || request.Method != "item/commandExecution/requestApproval" || !request.ID.IsString() || len(request.Trace) == 0 {
		t.Fatalf("second inbound: %+v", second)
	}
	responded := make(chan error, 1)
	go func() {
		responded <- request.Respond(context.Background(), map[string]string{"decision": "accept"})
	}()
	response := readWire(t, reader)
	if err := <-responded; err != nil {
		t.Fatal(err)
	}
	if response.Kind != MessageResponse || response.ID != StringID("approval") {
		t.Fatalf("response: %+v", response)
	}
	if err := request.Respond(context.Background(), nil); err == nil {
		t.Fatal("accepted duplicate reverse response")
	}
}

func TestClientReturnsRemoteError(t *testing.T) {
	client, reader, writer := clientPipes(t, 8)
	result := make(chan error, 1)
	go func() { result <- client.Call(context.Background(), "fail", nil, nil) }()
	request := readWire(t, reader)
	writeWire(t, writer, ErrorResponse(request.ID, ErrorObject{Code: -32000, Message: "failed"}))
	var remote *RemoteError
	if err := <-result; !errors.As(err, &remote) || remote.Object.Code != -32000 {
		t.Fatalf("remote error: %v", err)
	}
}

func TestClientEOFUnblocksPendingCall(t *testing.T) {
	client, reader, writer := clientPipes(t, 8)
	result := make(chan error, 1)
	go func() { result <- client.Call(context.Background(), "wait", nil, nil) }()
	_ = readWire(t, reader)
	_ = writer.Close()
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call remained blocked")
	}
}

func TestClientCancellationAfterWriteClosesTransport(t *testing.T) {
	client, reader, _ := clientPipes(t, 8)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.Call(ctx, "cancelled", nil, nil) }()
	_ = readWire(t, reader)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("call: %v", err)
	}
	select {
	case <-client.Done():
		if !errors.Is(client.Err(), context.Canceled) {
			t.Fatalf("client error: %v", client.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled in-flight call did not close transport")
	}
}

// Close reports ErrClosed, not the incidental "read/write on closed pipe" the
// read loop observes once the transport is torn down. shutdown is first-wins, so
// recording the close reason after closing the closer lets the pipe error win.
func TestClientCloseReportsErrClosedNotPipeError(t *testing.T) {
	for iteration := range 100 {
		client, _, _ := clientPipes(t, 8)
		if err := client.Close(); err != nil {
			t.Fatalf("iteration %d: %v", iteration, err)
		}
		select {
		case <-client.Done():
			if !errors.Is(client.Err(), ErrClosed) {
				t.Fatalf("iteration %d: client error: %v", iteration, client.Err())
			}
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: close did not finish", iteration)
		}
	}
}

func TestClientQueueOverflowIsTerminal(t *testing.T) {
	client, _, writer := clientPipes(t, 1)
	writeWire(t, writer, Notification("one", nil))
	writeWire(t, writer, Notification("two", nil))
	select {
	case <-client.Done():
		if !errors.Is(client.Err(), ErrInboundQueue) {
			t.Fatalf("got %v", client.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("client did not fail on overflow")
	}
}

// Reverse requests and notifications share one queue and one order. A reader
// that split them across two channels would let a consumer selecting over
// both observe a request ahead of the notification that preceded it on the
// wire; the adapter relies on wire order to reduce turn/started before the
// turn's first reverse request.
func TestClientDeliversRequestsAndNotificationsInWireOrder(t *testing.T) {
	const frames = 64
	client, _, writer := clientPipes(t, frames)
	var want []string
	for index := range frames {
		// Cluster requests unevenly so every adjacency (n→r, r→n, r→r, n→n) occurs.
		if index%5 == 1 || index%5 == 2 || index%7 == 0 {
			method := "request-" + strconv.Itoa(index)
			writeWire(t, writer, Request(IntegerID(int64(index)), method, nil))
			want = append(want, method)
			continue
		}
		method := "notification-" + strconv.Itoa(index)
		writeWire(t, writer, Notification(method, nil))
		want = append(want, method)
	}
	for index, method := range want {
		select {
		case message := <-client.Inbound():
			switch {
			case message.Request != nil && message.Notification == nil:
				if message.Request.Method != method {
					t.Fatalf("frame %d: got request %s, want %s", index, message.Request.Method, method)
				}
			case message.Notification != nil && message.Request == nil:
				if message.Notification.Method != method {
					t.Fatalf("frame %d: got notification %s, want %s", index, message.Notification.Method, method)
				}
			default:
				t.Fatalf("frame %d: malformed inbound message %+v", index, message)
			}
		case <-time.After(time.Second):
			t.Fatalf("frame %d (%s) was not delivered", index, method)
		}
	}
}

func TestClientWriteReturnsWhenContextCancelledDuringBlockedWriter(t *testing.T) {
	serverToClientReader, serverToClientWriter := io.Pipe()
	writer := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	client := NewClient(serverToClientReader, writer, ClientOptions{})
	t.Cleanup(func() {
		close(writer.release)
		_ = serverToClientWriter.Close()
		_ = client.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- client.Notify(ctx, "blocked", nil) }()
	<-writer.entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write ignored context cancellation")
	}
}

func TestClientWriteReturnsWhenContextCancelledWhileQueued(t *testing.T) {
	serverToClientReader, serverToClientWriter := io.Pipe()
	writer := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	client := NewClient(serverToClientReader, writer, ClientOptions{})
	t.Cleanup(func() {
		select {
		case <-writer.release:
		default:
			close(writer.release)
		}
		_ = serverToClientWriter.Close()
		_ = client.Close()
	})
	first := make(chan error, 1)
	go func() { first <- client.Notify(context.Background(), "first", nil) }()
	<-writer.entered
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { second <- client.Notify(ctx, "second", nil) }()
	cancel()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued write ignored context cancellation")
	}
	close(writer.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestClientReverseRequestOverflowDoesNotBlockShutdown(t *testing.T) {
	serverToClientReader, serverToClientWriter := io.Pipe()
	client := NewClient(serverToClientReader, &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}, ClientOptions{QueueCapacity: 1, CloseReadWriter: serverToClientReader})
	t.Cleanup(func() {
		_ = serverToClientWriter.Close()
		_ = client.Close()
	})
	writeWire(t, serverToClientWriter, Message{Kind: MessageRequest, ID: IntegerID(1), Method: "one"})
	writeWire(t, serverToClientWriter, Message{Kind: MessageRequest, ID: IntegerID(2), Method: "two"})
	select {
	case <-client.Done():
		if !errors.Is(client.Err(), ErrInboundQueue) {
			t.Fatalf("got %v", client.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("reverse request overflow blocked shutdown")
	}
}

func TestClientSerializesConcurrentWrites(t *testing.T) {
	client, reader, writer := clientPipes(t, 64)
	defer writer.Close()
	var wait sync.WaitGroup
	for index := range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := client.Notify(context.Background(), "event", map[string]int{"index": index}); err != nil {
				t.Errorf("notify: %v", err)
			}
		}()
	}
	messages := make(chan []Message, 1)
	go func() {
		var decoded []Message
		for range 32 {
			line, err := reader.ReadString('\n')
			if err != nil {
				messages <- nil
				return
			}
			message, err := ParseMessage([]byte(strings.TrimSuffix(line, "\n")))
			if err != nil {
				messages <- nil
				return
			}
			decoded = append(decoded, message)
		}
		messages <- decoded
	}()
	wait.Wait()
	if decoded := <-messages; len(decoded) != 32 {
		t.Fatalf("decoded %d frames", len(decoded))
	}
}

// A client built without a CloseReadWriter — which is how the Codex process
// owner builds it — cannot interrupt a reader parked in Decode, so settling
// the pending calls must never wait for the reader to stop: when one call's
// cancellation retires the client, the other pending call has to fail
// promptly instead of hanging until the peer closes stdout.
func TestClientFailsPendingCallsWithoutCloserWhenAnotherCallCancels(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, clientWriterUnused := io.Pipe()
	defer clientWriterUnused.Close()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	go func() { _, _ = io.Copy(io.Discard, serverReader) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelled := make(chan error, 1)
	go func() { cancelled <- client.Call(ctx, "a", nil, nil) }()
	other := make(chan error, 1)
	go func() { other <- client.Call(context.Background(), "b", nil, nil) }()
	// Both requests are on the wire; nothing ever answers them.
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

// The malformed-frame corpus shape at the rpc layer: the peer answers the
// request, then writes a corrupt line and exits. Both the response and the
// transport's death sit on one ordered stream, back to back, and the response
// must win — including when the caller's write is still waiting for the pump's
// result as the reader retires the client.
func TestClientCallSurvivesResponseFollowedByMalformedFrame(t *testing.T) {
	const runs = 500
	for i := 0; i < runs; i++ {
		serverReader, clientWriter := io.Pipe()
		clientReader, serverWriter := io.Pipe()
		client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
		go func() {
			_, _ = bufio.NewReader(serverReader).ReadString('\n')
			_, _ = serverWriter.Write([]byte(`{"id":1,"result":{}}` + "\n{not json\n"))
			_ = serverWriter.Close()
		}()
		if err := client.Call(context.Background(), "prompt.submit", nil, nil); err != nil {
			t.Fatalf("run %d: the response lost to the transport's death: %v", i, err)
		}
		// The call may return before the reader reaches the corrupt line;
		// judge the retirement only once the reader has stopped.
		<-client.ReadDone()
		if client.Err() == nil {
			t.Fatalf("run %d: the malformed frame did not retire the client", i)
		}
		_ = client.Close()
		_ = serverReader.Close()
		_ = clientReader.Close()
	}
}

// A pump blocked inside Encode — the peer stopped reading stdin — cannot be
// woken without a closer. When the reader then retires the client, a caller
// waiting on that frame must be released with the death rather than held
// until the peer happens to exit.
func TestClientWriteBlockedWithoutCloserReturnsWhenReaderRetires(t *testing.T) {
	serverReader, clientWriter := io.Pipe() // never read: Encode blocks
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 8})
	result := make(chan error, 1)
	go func() { result <- client.Call(context.Background(), "prompt.submit", nil, nil) }()
	// Wait for the pump to be inside Encode, blocked on the unread pipe.
	deadline := time.Now().Add(2 * time.Second)
	for {
		client.mu.Lock()
		encoding := client.encoding
		client.mu.Unlock()
		if encoding {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pump never entered Encode")
		}
		time.Sleep(time.Millisecond)
	}
	// The peer emits garbage: the reader retires the client while the pump
	// is still blocked.
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
	_ = serverReader.Close()
	_ = serverWriter.Close()
}

// transportCloseCounter records how many times the client closed the
// supplied CloseReadWriter. io.Closer does not promise idempotence, so a
// retirement must close it exactly once whichever path reaches it first.
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

		if order == "close-then-shutdown" {
			_ = client.Close()
			client.shutdown(errors.New("late"))
		} else {
			client.shutdown(errors.New("direct"))
			_ = client.Close()
		}
		// The reader's own retirement on the closed pipe must not close it
		// again either.
		<-client.ReadDone()
		if n := closer.calls.Load(); n != 1 {
			t.Fatalf("%s: transport closed %d times, want exactly once", order, n)
		}
		_ = writer.Close()
	}
}

var errEncodeAfterDelivery = errors.New("encode failed after the frame was delivered")

// releasedFailingWriter forwards each frame to the peer, then holds the write
// open until released and reports a failure for it: a frame the peer received
// and answered whose Encode nevertheless returned an error.
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

// A frame the peer received and answered must settle on its response even when
// Encode reports a failure for it: the client retires before the failure is
// published, so the caller settles on its channel rather than removing a
// pending id whose reply is already in hand.
//
// Unlike the other regression tests here this one also passes against the
// client it fixes: there the pump published the failure before closing done,
// and losing that window needs the caller scheduled in the instant between the
// two, which 300 runs never hit. It guards the invariant rather than
// reproducing the defect.
func TestClientCallSettlesOnResponseWhenEncodeFailsAfterDelivery(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	writer := &releasedFailingWriter{inner: clientWriter, release: make(chan struct{})}
	client := NewClient(clientReader, writer, ClientOptions{QueueCapacity: 8})
	result := make(chan error, 1)
	go func() { result <- client.Call(context.Background(), "prompt.submit", nil, nil) }()
	if _, err := bufio.NewReader(serverReader).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	// The peer answers while the pump still holds the write open; wait until
	// the reply has been delivered to the pending call.
	if _, err := serverWriter.Write([]byte(`{"id":1,"result":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		client.mu.Lock()
		delivered := len(client.pending) == 0
		client.mu.Unlock()
		if delivered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reply never delivered")
		}
		time.Sleep(time.Millisecond)
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
