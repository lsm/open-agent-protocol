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

	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
)

type testCloser struct {
	in  *io.PipeReader
	out *io.PipeWriter
}

func (c testCloser) Close() error { _ = c.in.Close(); return c.out.Close() }
func clientPipes(t *testing.T, capacity int) (*Client, *bufio.Reader, *io.PipeWriter) {
	t.Helper()
	serverToClientR, serverToClientW := io.Pipe()
	clientToServerR, clientToServerW := io.Pipe()
	client := NewClient(serverToClientR, clientToServerW, ClientOptions{QueueCapacity: capacity, WriteQueueCapacity: capacity, CloseReadWriter: testCloser{serverToClientR, clientToServerW}})
	t.Cleanup(func() { _ = client.Close(); _ = clientToServerR.Close(); _ = serverToClientW.Close() })
	return client, bufio.NewReader(clientToServerR), serverToClientW
}
func readCommand(t *testing.T, reader *bufio.Reader) native.Command {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var command native.Command
	if err := json.Unmarshal(line, &command); err != nil {
		t.Fatal(err)
	}
	return command
}
func writeLine(t *testing.T, writer io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(writer, line+"\n"); err != nil {
		t.Fatal(err)
	}
}
func autoAcknowledge(client *Client) {
	go func() {
		for {
			select {
			case message := <-client.Inbound():
				if message.Barrier != nil {
					close(message.Barrier)
				}
			case <-client.Done():
				return
			}
		}
	}()
}

func TestClientOrderedInterleavingAndSemanticBarrier(t *testing.T) {
	client, reader, writer := clientPipes(t, 8)
	inbound := client.Inbound()
	result := make(chan error, 1)
	message := "hello"
	go func() {
		result <- client.Call(context.Background(), native.Command{Type: native.CommandPrompt, Message: &message}, nil)
	}()
	command := readCommand(t, reader)
	writeLine(t, writer, `{"type":"agent_start"}`)
	writeLine(t, writer, `{"type":"extension_ui_request","id":"ui","method":"confirm","title":"T","message":"M"}`)
	writeLine(t, writer, `{"id":"`+command.ID+`","type":"response","command":"prompt","success":true}`)
	first, second, barrier := <-inbound, <-inbound, <-inbound
	if first.Event == nil || first.Event.Type != native.EventAgentStart || second.ExtensionRequest == nil || barrier.Barrier == nil {
		t.Fatalf("order: %+v %+v %+v", first, second, barrier)
	}
	select {
	case err := <-result:
		t.Fatalf("response overtook barrier: %v", err)
	default:
	}
	close(barrier.Barrier)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestClientCorrelatesOutOfOrderResponses(t *testing.T) {
	client, reader, writer := clientPipes(t, 8)
	autoAcknowledge(client)
	type outcome struct {
		value string
		err   error
	}
	results := make(chan outcome, 2)
	go func() {
		var out struct {
			Text string `json:"text"`
		}
		err := client.Call(context.Background(), native.Command{ID: "a", Type: native.CommandGetLastAssistantText}, &out)
		results <- outcome{out.Text, err}
	}()
	go func() {
		var out struct {
			Text string `json:"text"`
		}
		err := client.Call(context.Background(), native.Command{ID: "b", Type: native.CommandGetLastAssistantText}, &out)
		results <- outcome{out.Text, err}
	}()
	one, two := readCommand(t, reader), readCommand(t, reader)
	writeLine(t, writer, `{"id":"`+two.ID+`","type":"response","command":"get_last_assistant_text","success":true,"data":{"text":"`+two.ID+`"}}`)
	writeLine(t, writer, `{"id":"`+one.ID+`","type":"response","command":"get_last_assistant_text","success":true,"data":{"text":"`+one.ID+`"}}`)
	seen := map[string]bool{}
	for range 2 {
		value := <-results
		if value.err != nil {
			t.Fatal(value.err)
		}
		seen[value.value] = true
	}
	if !seen["a"] || !seen["b"] {
		t.Fatal(seen)
	}
}

func TestClientResponseErrorsAreFatalWhenProtocolAmbiguous(t *testing.T) {
	for _, test := range []struct {
		name, response string
		want           error
	}{
		{"unmatched", `{"id":"other","type":"response","command":"get_state","success":true}`, ErrResponseNotFound},
		{"missing", `{"type":"response","command":"get_state","success":true}`, ErrResponseNotFound},
		{"command", `{"id":"x","type":"response","command":"abort","success":true}`, ErrResponseCommand},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, reader, writer := clientPipes(t, 4)
			result := make(chan error, 1)
			go func() {
				result <- client.Call(context.Background(), native.Command{ID: "x", Type: native.CommandGetState}, nil)
			}()
			_ = readCommand(t, reader)
			writeLine(t, writer, test.response)
			select {
			case <-client.Done():
				if !errors.Is(client.Err(), test.want) {
					t.Fatalf("%v", client.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("client open")
			}
			if err := <-result; err == nil {
				t.Fatal("call succeeded")
			}
		})
	}
}

func TestClientNeverReusesCompletedID(t *testing.T) {
	client, reader, writer := clientPipes(t, 4)
	autoAcknowledge(client)
	first := make(chan error, 1)
	go func() {
		first <- client.Call(context.Background(), native.Command{ID: "fixed", Type: native.CommandGetState}, nil)
	}()
	command := readCommand(t, reader)
	writeLine(t, writer, `{"id":"`+command.ID+`","type":"response","command":"get_state","success":true}`)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), native.Command{ID: "fixed", Type: native.CommandGetState}, nil); !errors.Is(err, ErrDuplicateRequestID) {
		t.Fatalf("got %v", err)
	}
}

func TestClientRemoteError(t *testing.T) {
	client, reader, writer := clientPipes(t, 4)
	autoAcknowledge(client)
	result := make(chan error, 1)
	message := "x"
	go func() {
		result <- client.Call(context.Background(), native.Command{Type: native.CommandPrompt, Message: &message}, nil)
	}()
	command := readCommand(t, reader)
	writeLine(t, writer, `{"id":"`+command.ID+`","type":"response","command":"prompt","success":false,"error":"rejected"}`)
	var remote *RemoteError
	if err := <-result; !errors.As(err, &remote) || remote.Message != "rejected" {
		t.Fatalf("%v", err)
	}
}

func TestClientRespondSerializesWithCommands(t *testing.T) {
	client, reader, _ := clientPipes(t, 4)
	confirmed := false
	result := make(chan error, 1)
	go func() {
		result <- client.Respond(context.Background(), native.ExtensionUIResponse{Type: "extension_ui_response", ID: "ui", Confirmed: &confirmed})
	}()
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if line != `{"type":"extension_ui_response","id":"ui","confirmed":false}`+"\n" {
		t.Fatalf("%q", line)
	}
}

func TestClientPreActivationEventStillBlocksResponseAtBarrier(t *testing.T) {
	client, reader, writer := clientPipes(t, 2)
	result := make(chan error, 1)
	go func() { result <- client.Call(context.Background(), native.Command{Type: native.CommandGetState}, nil) }()
	command := readCommand(t, reader)
	writeLine(t, writer, `{"type":"agent_start"}`)
	writeLine(t, writer, `{"id":"`+command.ID+`","type":"response","command":"get_state","success":true}`)
	inbound := client.Inbound()
	if message := <-inbound; message.Event == nil {
		t.Fatalf("%+v", message)
	}
	barrier := <-inbound
	if barrier.Barrier == nil {
		t.Fatalf("%+v", barrier)
	}
	select {
	case err := <-result:
		t.Fatalf("response overtook barrier: %v", err)
	default:
	}
	close(barrier.Barrier)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

type blockingWriter struct {
	entered, release chan struct{}
	once             sync.Once
}

func (w *blockingWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(data), nil
}
func TestClientCancellationDuringWriteRetiresConnection(t *testing.T) {
	reader, writer := io.Pipe()
	bw := &blockingWriter{make(chan struct{}), make(chan struct{}), sync.Once{}}
	client := NewClient(reader, bw, ClientOptions{QueueCapacity: 1, WriteQueueCapacity: 1, CloseReadWriter: reader})
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, native.Command{Type: native.CommandGetState}, nil) }()
	<-bw.entered
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("wrong cancellation")
	}
	select {
	case <-client.Done():
	case <-time.After(time.Second):
		t.Fatal("still open")
	}
	close(bw.release)
}

func TestClientMalformedOutputAndOverflowClose(t *testing.T) {
	client, _, writer := clientPipes(t, 1)
	writeLine(t, writer, "garbage")
	<-client.Done()
	if !errors.Is(client.Err(), ErrInvalidFrame) {
		t.Fatalf("%v", client.Err())
	}
	client2, _, writer2 := clientPipes(t, 1)
	writeLine(t, writer2, `{"type":"agent_start"}`)
	writeLine(t, writer2, `{"type":"agent_settled"}`)
	<-client2.Done()
	if !errors.Is(client2.Err(), ErrInboundQueue) {
		t.Fatalf("%v", client2.Err())
	}
}

func TestClientEOFSettlesPending(t *testing.T) {
	client, reader, writer := clientPipes(t, 2)
	done := make(chan error, 1)
	go func() { done <- client.Call(context.Background(), native.Command{Type: native.CommandGetState}, nil) }()
	_ = readCommand(t, reader)
	_ = writer.Close()
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatalf("%v", err)
	}
}

func TestGeneratedIDsAreDistinct(t *testing.T) {
	client, reader, writer := clientPipes(t, 4)
	autoAcknowledge(client)
	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- client.Call(context.Background(), native.Command{Type: native.CommandGetState}, nil)
		}()
	}
	one, two := readCommand(t, reader), readCommand(t, reader)
	if one.ID == "" || one.ID == two.ID || !strings.HasPrefix(one.ID, "req_") {
		t.Fatalf("%q %q", one.ID, two.ID)
	}
	for _, c := range []native.Command{one, two} {
		writeLine(t, writer, `{"id":"`+c.ID+`","type":"response","command":"get_state","success":true}`)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// Regression: a response parsed off the ordered stream before the transport
// died must win over the death. The reader parked in the response barrier used
// to abandon the already-decoded frame when the client retired, and shutdown
// retired the pending map underneath it, so the call returned the shutdown
// reason instead of the reply the gateway had already written.
func TestClientDeliversResponseParkedInBarrierWhenTransportRetires(t *testing.T) {
	client, reader, writer := clientPipes(t, 8)
	inbound := client.Inbound()
	result := make(chan error, 1)
	go func() {
		result <- client.Call(context.Background(), native.Command{Type: native.CommandGetState}, nil)
	}()
	command := readCommand(t, reader)
	// Push a second frame through the same serialized writer and drain it. The
	// writer hands back each frame's result before it accepts the next, so once
	// this frame is on the wire the call is certainly parked on its response
	// rather than still inside a write that the close would fail.
	synced := make(chan error, 1)
	go func() {
		synced <- client.Respond(context.Background(), native.ExtensionUIResponse{Type: "extension_ui_response", ID: "sync", Cancelled: true})
	}()
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	if err := <-synced; err != nil {
		t.Fatal(err)
	}
	writeLine(t, writer, `{"id":"`+command.ID+`","type":"response","command":"get_state","success":true}`)
	// Take the barrier without acknowledging it: the reader is now parked
	// holding a fully decoded response for this command.
	barrier := <-inbound
	if barrier.Barrier == nil {
		t.Fatalf("expected the response barrier first, got %+v", barrier)
	}
	// The transport dies underneath the parked reader.
	_ = client.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("the parked response lost to the transport's death: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call never settled")
	}
	if err := client.Err(); errors.Is(err, ErrResponseNotFound) {
		t.Fatalf("a concurrently retired id was reported as unmatched: %v", err)
	}
}
