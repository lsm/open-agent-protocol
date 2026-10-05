package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func runPiReopenCorpus(t *testing.T, frames []piCorpusFrame, expectedFile string) {
	t.Helper()
	left, right := net.Pipe()
	client := rpc.NewClient(left, left, rpc.ClientOptions{CloseReadWriter: left})
	defer client.Close()
	defer right.Close()
	finished := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(right)
		for _, frame := range frames {
			if frame.Direction == "host-to-pi" {
				if !scanner.Scan() {
					finished <- fmt.Errorf("missing native command: %v", scanner.Err())
					return
				}
				var got, want any
				if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
					finished <- err
					return
				}
				if err := json.Unmarshal(frame.Raw, &want); err != nil {
					finished <- err
					return
				}
				if !reflect.DeepEqual(got, want) {
					finished <- fmt.Errorf("native command=%s, want=%s", scanner.Bytes(), frame.Raw)
					return
				}
			} else if _, err := right.Write(append(append([]byte(nil), frame.Raw...), '\n')); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var initial native.SessionState
	if err := openingCall(ctx, client, native.Command{Type: native.CommandGetState}, &initial); err != nil {
		t.Fatal(err)
	}
	var loaded native.Response
	if err := json.Unmarshal(frames[5].Raw, &loaded); err != nil {
		t.Fatal(err)
	}
	var boundState native.SessionState
	if err := json.Unmarshal(loaded.Data, &boundState); err != nil {
		t.Fatal(err)
	}
	binding := sessionBinding{SessionID: boundState.SessionID, SessionFile: boundState.SessionFile}
	restored, err := reopenSession(ctx, client, binding)
	if err != nil {
		t.Fatal(err)
	}
	if restored.SessionID == initial.SessionID || restored.MessageCount != 3 {
		t.Fatalf("did not switch conversation: %+v", restored)
	}
	var confirmed native.SessionState
	if err := openingCall(ctx, client, native.Command{Type: native.CommandGetState}, &confirmed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, confirmed) {
		t.Fatal("reloaded settings changed")
	}
	projected := &Session{state: protocol.SessionState{SessionID: "session", Status: protocol.SessionIdle, CurrentModelID: nativeModelID(restored.Model)}}
	projected.restoreState(restored)
	expected := piLoadJSON[protocol.SessionState](t, expectedFile)
	if !reflect.DeepEqual(projected.state, expected) {
		t.Fatalf("state=%+v want=%+v", projected.state, expected)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
