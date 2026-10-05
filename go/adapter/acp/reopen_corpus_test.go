package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func runACPReopenCorpus(t *testing.T, frames []acpCorpusFrame, messages []rpc.Message, definition acpCorpusCase, expectedPath string) {
	t.Helper()
	if len(frames) != 7 || messages[2].Method != native.MethodSessionLoad {
		t.Fatal("not the captured load exchange")
	}
	var params native.SessionReopenParams
	if err := json.Unmarshal(messages[2].Params, &params); err != nil {
		t.Fatal(err)
	}
	if definition.IdentityMap[params.SessionID] != "session" {
		t.Fatal("native binding is not declared")
	}
	host, agent := net.Pipe()
	defer agent.Close()
	client := rpc.NewClient(host, host, rpc.ClientOptions{CloseReadWriter: host, StrictResponseIDs: true})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(agent)
		for _, frame := range frames {
			if frame.Direction == "client_request" {
				if !scanner.Scan() {
					completed <- fmt.Errorf("native request missing: %v", scanner.Err())
					return
				}
				var actual, wanted any
				if err := json.Unmarshal(scanner.Bytes(), &actual); err != nil {
					completed <- err
					return
				}
				if err := json.Unmarshal(frame.Raw, &wanted); err != nil {
					completed <- err
					return
				}
				if !reflect.DeepEqual(actual, wanted) {
					completed <- fmt.Errorf("native request differs: %s", scanner.Bytes())
					return
				}
			} else if _, err := agent.Write(append(append([]byte(nil), frame.Raw...), '\n')); err != nil {
				completed <- err
				return
			}
		}
		completed <- nil
	}()
	implementation, err := New(Config{WorkingDirectory: params.Cwd, Factory: ClientFactoryFunc(func(ctx context.Context) (Client, rpc.InitializeResponse, error) {
		var initialized rpc.InitializeResponse
		err := client.Call(ctx, "initialize", rpc.InitializeRequest{ProtocolVersion: 1, ClientCapabilities: rpc.ClientCapabilities{}, ClientInfo: &rpc.Implementation{Name: "open-agent-protocol", Version: protocol.Version}}, &initialized)
		return client, initialized, err
	})})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for feature, level := range definition.Capabilities {
		if string(descriptor.Capabilities.Features[feature].Level) != level {
			t.Fatalf("capability %s differs", feature)
		}
	}
	opened, err := implementation.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: params.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close(context.Background())
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	state, err := opened.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := acpLoadJSON[protocol.SessionState](t, expectedPath)
	if state.SessionID != expected.SessionID || state.Status != expected.Status || state.CurrentModelID != expected.CurrentModelID || state.ReasoningLevel != expected.ReasoningLevel || !reflect.DeepEqual(state.Recovery, expected.Recovery) || state.ActiveRunID != "" || len(opened.(*session).journal) != 0 || opened.(base.NativeSession).NativeSessionID() != params.SessionID {
		t.Fatalf("recovered state=%+v", state)
	}
	if _, _, err := opened.(*session).Resume(ctx, base.ResumeRequest{RunID: "pre-close-run"}); !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("old run resume=%v", err)
	}
}
