package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func answeredIncomingRequest(t *testing.T, message rpc.Message) (*rpc.IncomingRequest, <-chan string) {
	t.Helper()
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	client := rpc.NewClient(serverReader, serverWriter, rpc.ClientOptions{CloseReadWriter: pipePair{serverReader, serverWriter}, StrictResponseIDs: true})
	answers := make(chan string, 4)
	go func() {
		lines := bufio.NewScanner(clientReader)
		for lines.Scan() {
			answers <- lines.Text()
		}
	}()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = clientWriter.Write(append(raw, '\n')) }()
	select {
	case request := <-client.Requests():
		t.Cleanup(func() { _ = client.Close(); _ = clientWriter.Close(); _ = clientReader.Close() })
		return request, answers
	case <-time.After(time.Second):
		t.Fatal("timed out decoding reverse request")
	}
	return nil, nil
}

func TestAPermissionRequestArrivingAfterACancelIsAnsweredCancelledAtOnce(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	if _, err := s.Cancel(context.Background(), admission.RunID); err != nil {
		t.Fatal(err)
	}
	params := json.RawMessage(`{"sessionId":"native-session","toolCall":{"toolCallId":"call-late","title":"Act"},"options":[{"optionId":"allow_once","name":"Allow","kind":"allow_once"}]}`)
	request, answers := answeredIncomingRequest(t, rpc.Request(rpc.StringID("perm-late"), native.MethodSessionRequestPermission, params))
	f.inbound <- rpc.InboundMessage{Request: request}
	select {
	case answer := <-answers:
		if !strings.Contains(answer, `"perm-late"`) || !strings.Contains(answer, `"outcome":"cancelled"`) {
			t.Fatalf("answer = %s", answer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the late permission request was not answered while the prompt was still open")
	}
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "cancelled"}}
	events := adaptertest.Drain(t, stream, 2*time.Second)
	for _, event := range events {
		if event.Type == protocol.TypeActionPermissionRequested {
			t.Fatalf("a permission asked after the cancel was surfaced: %+v", event)
		}
	}
	if events[len(events)-1].Type != protocol.TypeRunCancelled {
		t.Fatalf("last = %s", events[len(events)-1].Type)
	}
}
