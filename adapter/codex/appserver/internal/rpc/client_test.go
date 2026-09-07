package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
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

	notification := <-client.Notifications()
	if notification.Method != "turn/started" {
		t.Fatalf("notification: %+v", notification)
	}
	request := <-client.Requests()
	if request.Method != "item/commandExecution/requestApproval" || !request.ID.IsString() || len(request.Trace) == 0 {
		t.Fatalf("request: %+v", request)
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

func TestClientQueueOverflowIsTerminal(t *testing.T) {
	client, _, writer := clientPipes(t, 1)
	writeWire(t, writer, Notification("one", nil))
	writeWire(t, writer, Notification("two", nil))
	select {
	case <-client.Done():
		if !errors.Is(client.Err(), ErrNotificationQueue) {
			t.Fatalf("got %v", client.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("client did not fail on overflow")
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
		if !errors.Is(client.Err(), ErrRequestQueue) {
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
