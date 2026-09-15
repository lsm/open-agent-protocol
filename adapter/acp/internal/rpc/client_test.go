package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testPipeCloser struct {
	in  *io.PipeReader
	out *io.PipeWriter
}

func (closer testPipeCloser) Close() error { _ = closer.in.Close(); return closer.out.Close() }

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

type closingBlockingWriter struct {
	entered chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (writer *closingBlockingWriter) Write([]byte) (int, error) {
	writer.once.Do(func() { close(writer.entered) })
	<-writer.closed
	return 0, io.ErrClosedPipe
}
func (writer *closingBlockingWriter) Close() error {
	writer.once.Do(func() { close(writer.entered) })
	select {
	case <-writer.closed:
	default:
		close(writer.closed)
	}
	return nil
}

type splitWriter struct {
	mu     sync.Mutex
	output strings.Builder
}

func (writer *splitWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(data) == 0 {
		return 0, nil
	}
	n := 1
	_, _ = writer.output.Write(data[:n])
	return n, nil
}
func (writer *splitWriter) String() string {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.output.String()
}

func clientPipes(t *testing.T, capacity int, strict bool) (*Client, *bufio.Reader, *io.PipeWriter) {
	t.Helper()
	serverToClientReader, serverToClientWriter := io.Pipe()
	clientToServerReader, clientToServerWriter := io.Pipe()
	client := NewClient(serverToClientReader, clientToServerWriter, ClientOptions{
		QueueCapacity: capacity, WriteQueueCapacity: capacity, StrictResponseIDs: strict,
		CloseReadWriter: testPipeCloser{in: serverToClientReader, out: clientToServerWriter},
	})
	t.Cleanup(func() { _ = client.Close(); _ = clientToServerReader.Close(); _ = serverToClientWriter.Close() })
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

func TestClientCorrelatesOutOfOrderCallsWithIDDomains(t *testing.T) {
	client, reader, writer := clientPipes(t, 8, true)
	type outcome struct {
		value string
		err   error
	}
	results := make(chan outcome, 2)
	go func() {
		var out string
		results <- outcome{out, client.CallID(context.Background(), StringID("same"), "string", nil, &out)}
	}()
	go func() {
		var out string
		results <- outcome{out, client.CallID(context.Background(), IntegerID(7), "integer", nil, &out)}
	}()
	one, two := readWire(t, reader), readWire(t, reader)
	writeWire(t, writer, Response(two.ID, json.RawMessage(`"`+two.Method+`"`)))
	writeWire(t, writer, Response(one.ID, json.RawMessage(`"`+one.Method+`"`)))
	seen := map[string]bool{}
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		seen[result.value] = true
	}
	if !seen["string"] || !seen["integer"] {
		t.Fatalf("results: %v", seen)
	}
}

func TestClientOrderedInboundBarrierPrecedesResponse(t *testing.T) {
	client, reader, writer := clientPipes(t, 8, true)
	inbound := client.Inbound()
	result := make(chan error, 1)
	go func() {
		var out string
		result <- client.Call(context.Background(), "prompt", nil, &out)
	}()
	request := readWire(t, reader)
	writeWire(t, writer, Notification("session/update", json.RawMessage(`{"n":1}`)))
	writeWire(t, writer, Response(request.ID, json.RawMessage(`"done"`)))
	message := <-inbound
	if message.Notification == nil || message.Notification.Method != "session/update" {
		t.Fatalf("first inbound=%+v", message)
	}
	select {
	case err := <-result:
		t.Fatalf("response overtook notification: %v", err)
	default:
	}
	barrier := <-inbound
	if barrier.Barrier == nil {
		t.Fatalf("second inbound=%+v", barrier)
	}
	close(barrier.Barrier)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestClientInboundMigratesFramesDecodedBeforeActivation(t *testing.T) {
	client, reader, writer := clientPipes(t, 8, true)
	result := make(chan error, 1)
	go func() {
		var out string
		result <- client.Call(context.Background(), "session/new", nil, &out)
	}()
	request := readWire(t, reader)
	writeWire(t, writer, Response(request.ID, json.RawMessage(`"done"`)))
	writeWire(t, writer, Notification("session/update", json.RawMessage(`{"n":1}`)))
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	inbound := client.Inbound()
	select {
	case message := <-inbound:
		if message.Notification == nil || message.Notification.Method != "session/update" {
			t.Fatalf("migrated inbound=%+v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("notification decoded before activation was stranded")
	}
}

func TestClientInboundPreservesMixedPreActivationOrder(t *testing.T) {
	client, _, writer := clientPipes(t, 2, true)
	writeWire(t, writer, Notification("first", json.RawMessage(`{}`)))
	writeWire(t, writer, Request(StringID("second"), "second", json.RawMessage(`{}`)))
	inbound := client.Inbound()
	first := <-inbound
	second := <-inbound
	if first.Notification == nil || first.Notification.Method != "first" || second.Request == nil || second.Request.Method != "second" {
		t.Fatalf("migration reordered observations: first=%+v second=%+v", first, second)
	}
}

func TestClientInboundSelectionDoesNotDuplicateIntoLegacyQueues(t *testing.T) {
	client, _, writer := clientPipes(t, 2, true)
	writeWire(t, writer, Notification("once", json.RawMessage(`{}`)))
	if message := <-client.Inbound(); message.Notification == nil || message.Notification.Method != "once" {
		t.Fatalf("inbound=%+v", message)
	}
	select {
	case duplicate := <-client.notifications:
		t.Fatalf("notification duplicated into legacy queue: %+v", duplicate)
	default:
	}
}

func TestClientInboundMigrationDoesNotBlockAtCapacity(t *testing.T) {
	client, _, writer := clientPipes(t, 2, true)
	writeWire(t, writer, Notification("one", json.RawMessage(`{}`)))
	writeWire(t, writer, Notification("two", json.RawMessage(`{}`)))
	activated := make(chan (<-chan InboundMessage), 1)
	go func() { activated <- client.Inbound() }()
	var inbound <-chan InboundMessage
	select {
	case inbound = <-activated:
	case <-time.After(time.Second):
		t.Fatal("Inbound blocked while migrating a full backlog")
	}
	if (<-inbound).Notification.Method != "one" || (<-inbound).Notification.Method != "two" {
		t.Fatal("full backlog was not migrated in order")
	}
}

func TestClientRejectsDuplicateOutboundID(t *testing.T) {
	client, reader, writer := clientPipes(t, 8, true)
	first := make(chan error, 1)
	go func() { first <- client.CallID(context.Background(), StringID("x"), "one", nil, nil) }()
	request := readWire(t, reader)
	if err := client.CallID(context.Background(), StringID("x"), "two", nil, nil); !errors.Is(err, ErrDuplicateRequestID) {
		t.Fatalf("got %v", err)
	}
	writeWire(t, writer, Response(request.ID, json.RawMessage(`null`)))
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestClientRoutesAndResolvesReverseRequest(t *testing.T) {
	client, reader, writer := clientPipes(t, 8, true)
	writeWire(t, writer, Notification("session/update", json.RawMessage(`{"sessionId":"s"}`)))
	writeWire(t, writer, Request(StringID("permission"), "session/request_permission", json.RawMessage(`{"sessionId":"s"}`)))
	if notification := <-client.Notifications(); notification.Method != "session/update" {
		t.Fatalf("notification: %+v", notification)
	}
	request := <-client.Requests()
	responded := make(chan error, 1)
	go func() {
		responded <- request.Respond(context.Background(), map[string]any{"outcome": map[string]string{"outcome": "cancelled"}})
	}()
	response := readWire(t, reader)
	if err := <-responded; err != nil {
		t.Fatal(err)
	}
	if response.Kind != MessageResponse || response.ID != StringID("permission") {
		t.Fatalf("response: %+v", response)
	}
	if err := request.Respond(context.Background(), nil); !errors.Is(err, ErrReverseRequestResolved) {
		t.Fatalf("duplicate response: %v", err)
	}
}

func TestReverseResponseWriteFailureRemainsUnresolved(t *testing.T) {
	serverToClientReader, serverToClientWriter := io.Pipe()
	writer := &closingBlockingWriter{entered: make(chan struct{}), closed: make(chan struct{})}
	client := NewClient(serverToClientReader, writer, ClientOptions{QueueCapacity: 2, WriteQueueCapacity: 2, CloseReadWriter: writer})
	t.Cleanup(func() { _ = client.Close(); _ = serverToClientWriter.Close() })
	writeWire(t, serverToClientWriter, Request(StringID("permission"), "session/request_permission", json.RawMessage(`{}`)))
	request := <-client.Requests()
	responded := make(chan error, 1)
	go func() { responded <- request.Respond(context.Background(), map[string]string{"outcome": "selected"}) }()
	<-writer.entered
	_ = writer.Close()
	if err := <-responded; err == nil {
		t.Fatal("expected response write failure")
	}
	if err := request.Respond(context.Background(), nil); errors.Is(err, ErrReverseRequestResolved) {
		t.Fatalf("failed write incorrectly settled request: %v", err)
	}
}

func TestClientRejectsDuplicateActiveReverseID(t *testing.T) {
	client, _, writer := clientPipes(t, 8, true)
	writeWire(t, writer, Request(IntegerID(1), "first", nil))
	writeWire(t, writer, Request(IntegerID(1), "second", nil))
	select {
	case <-client.Done():
		if !errors.Is(client.Err(), ErrDuplicateRequestID) {
			t.Fatalf("got %v", client.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("duplicate did not close")
	}
}

func TestClientReturnsRemoteError(t *testing.T) {
	client, reader, writer := clientPipes(t, 8, true)
	result := make(chan error, 1)
	go func() { result <- client.Call(context.Background(), "fail", nil, nil) }()
	request := readWire(t, reader)
	writeWire(t, writer, ErrorResponse(request.ID, ErrorObject{Code: -32800, Message: "cancelled"}))
	var remote *RemoteError
	if err := <-result; !errors.As(err, &remote) || remote.Object.Code != -32800 {
		t.Fatalf("remote: %v", err)
	}
}

func TestClientStrictUnmatchedResponseIsTerminal(t *testing.T) {
	client, _, writer := clientPipes(t, 8, true)
	writeWire(t, writer, Response(IntegerID(999), json.RawMessage(`null`)))
	select {
	case <-client.Done():
		if !errors.Is(client.Err(), ErrResponseNotFound) {
			t.Fatalf("got %v", client.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("unmatched response did not close")
	}
}

func TestClientNonStrictUnmatchedResponseIsDiagnostic(t *testing.T) {
	client, _, writer := clientPipes(t, 8, false)
	writeWire(t, writer, Response(IntegerID(999), json.RawMessage(`null`)))
	select {
	case err := <-client.Diagnostics():
		if !errors.Is(err, ErrResponseNotFound) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing diagnostic")
	}
	select {
	case <-client.Done():
		t.Fatal("non-strict response closed client")
	default:
	}
}

func TestClientEOFUnblocksPendingCall(t *testing.T) {
	client, reader, writer := clientPipes(t, 8, true)
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
		t.Fatal("pending call blocked")
	}
}

func TestClientCancellationAfterWriteClosesTransport(t *testing.T) {
	client, reader, _ := clientPipes(t, 8, true)
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
			t.Fatalf("client: %v", client.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("transport remained open")
	}
}

func TestClientQueueOverflowsAreTerminal(t *testing.T) {
	for name, test := range map[string]struct {
		first, second Message
		target        error
	}{
		"notification": {Notification("one", nil), Notification("two", nil), ErrNotificationQueue},
		"request":      {Request(IntegerID(1), "one", nil), Request(IntegerID(2), "two", nil), ErrRequestQueue},
	} {
		t.Run(name, func(t *testing.T) {
			client, _, writer := clientPipes(t, 1, true)
			writeWire(t, writer, test.first)
			writeWire(t, writer, test.second)
			select {
			case <-client.Done():
				if !errors.Is(client.Err(), test.target) {
					t.Fatalf("got %v", client.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("overflow did not close")
			}
		})
	}
}

func TestClientCancellationInterruptsBlockedWriteWithCloser(t *testing.T) {
	serverReader, serverWriter := io.Pipe()
	writer := &closingBlockingWriter{entered: make(chan struct{}), closed: make(chan struct{})}
	client := NewClient(serverReader, writer, ClientOptions{CloseReadWriter: writer})
	t.Cleanup(func() { _ = serverWriter.Close(); _ = client.Close() })
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
		t.Fatal("blocked write ignored cancellation")
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("client remained open")
	}
}

func TestClientCancelledQueuedWriteIsNotEncoded(t *testing.T) {
	serverReader, serverWriter := io.Pipe()
	writer := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	client := NewClient(serverReader, writer, ClientOptions{WriteQueueCapacity: 2})
	t.Cleanup(func() {
		select {
		case <-writer.release:
		default:
			close(writer.release)
		}
		_ = serverWriter.Close()
		_ = client.Close()
	})
	first := make(chan error, 1)
	go func() { first <- client.Notify(context.Background(), "first", nil) }()
	<-writer.entered
	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() { second <- client.Notify(ctx, "second", nil) }()
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("second: %v", err)
	}
	close(writer.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestClientSerializesConcurrentPartialWrites(t *testing.T) {
	serverReader, serverWriter := io.Pipe()
	writer := &splitWriter{}
	client := NewClient(serverReader, writer, ClientOptions{QueueCapacity: 64, WriteQueueCapacity: 64})
	t.Cleanup(func() { _ = serverWriter.Close(); _ = client.Close() })
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
	wait.Wait()
	lines := strings.Split(strings.TrimSpace(writer.String()), "\n")
	if len(lines) != 32 {
		t.Fatalf("got %d frames", len(lines))
	}
	for _, line := range lines {
		if _, err := ParseMessage([]byte(line)); err != nil {
			t.Fatalf("interleaved %q: %v", line, err)
		}
	}
}

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

// A client built without a CloseReadWriter cannot interrupt a reader parked
// in Decode, so settling the pending calls must never wait for the reader to
// stop: when one call's cancellation retires the client, the other pending
// call has to fail promptly instead of hanging until the peer closes stdout.
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

// The malformed-frame corpus shape at the rpc layer: the gateway answers the
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
	go func() { result <- client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil) }()
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

// A response whose ordering barrier cannot be enqueued must not be delivered:
// the client is retiring on queue overflow with wire-earlier observations
// still unreduced, so releasing the reply would let it overtake them. Only a
// barrier that was enqueued and then overtaken by the transport's death
// releases the response.
func TestClientRefusesResponseWhenBarrierCannotBeEnqueued(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := NewClient(clientReader, clientWriter, ClientOptions{QueueCapacity: 1})
	_ = client.Inbound() // ordered stream active, nobody consuming
	result := make(chan error, 1)
	go func() { result <- client.CallID(context.Background(), IntegerID(1), "prompt.submit", nil, nil) }()
	if _, err := bufio.NewReader(serverReader).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = io.Copy(io.Discard, serverReader) }()
	// One wire-earlier observation fills the queue; the response's barrier
	// then cannot be enqueued.
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
		_ = client.Inbound()
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
