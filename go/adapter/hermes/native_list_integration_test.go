package hermes

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestHermesProcessListsTheSessionItStoredOnOneKeptGateway(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Hermes process integration in short mode")
	}
	if os.Getenv("OAP_HERMES_INTEGRATION") != "1" {
		t.Skip("set OAP_HERMES_INTEGRATION=1 with absolute OAP_HERMES_BIN (python interpreter) and OAP_HERMES_ROOT (pinned hermes-agent checkout) to run; optionally set OAP_HERMES_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	mock := providertest.New(t, providertest.Config{OpenAIKey: hermesMockSecret})
	mock.Enqueue(providertest.OpenAIChatCompletion, providertest.Success)
	root := verifiedHermesRoot(t)
	isolated := t.TempDir()
	environment := hermesEnvironment(t, isolated, mock.OpenAIBaseURL())
	writeHermesLoopbackConfig(t, isolated, mock.OpenAIBaseURL())
	var launches atomic.Int32
	implementation, err := New(Config{
		Executable: verifiedHermesPython(t), Args: []string{"-m", "tui_gateway.entry"},
		Environment: environment, WorkingDirectory: root, Model: hermesLoopbackModel,
		ExitTimeout: 15 * time.Second,
		ProcessFactory: ProcessFactoryFunc(func(ctx context.Context, config rpc.ProcessConfig) (ProcessBridge, error) {
			launches.Add(1)
			process, err := rpc.Start(ctx, config)
			if err != nil {
				return nil, err
			}
			return &rpcProcess{process}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer implementation.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{SessionID: "listed", Participant: protocol.Participant{ID: "integration-user"}})
	if err != nil {
		t.Fatalf("open pinned gateway: %v", err)
	}
	admission, stream, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "listed", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, 60*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	stored := session.(*Session).NativeSessionID()
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	opened := launches.Load()

	started := time.Now()
	for listing := range 2 {
		listed, err := implementation.NativeList(ctx, base.NativeListRequest{Limit: 10})
		if err != nil {
			t.Fatalf("listing %d: %v", listing+1, err)
		}
		var found *base.NativeListing
		for i := range listed {
			if listed[i].NativeID == stored {
				found = &listed[i]
			}
		}
		if found == nil || found.Title == "" || found.Directory != "" || found.UpdatedAtMS < started.Add(-5*time.Minute).UnixMilli() || found.UpdatedAtMS > time.Now().UnixMilli() {
			t.Fatalf("listing %d did not name the stored session %q as a titled row started just now: %+v", listing+1, stored, listed)
		}
	}
	if got := launches.Load() - opened; got != 1 {
		t.Fatalf("two listings started %d gateways, want one kept across both", got)
	}
}
