package copilot

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func factoryConfig(t *testing.T, mode string) Config {
	t.Helper()
	p := syntheticProcessConfig(t, mode)
	return Config{Executable: p.Path, Args: p.Args, Environment: p.Env, WorkingDirectory: p.Dir, RequestTimeout: time.Second}
}

func openRequest() base.OpenRequest {
	return base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "user"}}
}

func TestPublicAdapterOpensThePinnedSyntheticServerAndRuns(t *testing.T) {
	a, err := New(factoryConfig(t, "early"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d, err := a.Probe(ctx)
	if err != nil || d.CapabilityRevision != CapabilityRevision || d.Capabilities.Endpoint.Version != PinnedVersion {
		t.Fatalf("probe %+v %v", d, err)
	}
	if d.Capabilities.Features["session.message.delivery.queue"].Level != protocol.SupportEmulated || d.Capabilities.Features["action.tools"].Level != protocol.SupportNative || d.InteractiveGates {
		t.Fatalf("descriptor %+v", d)
	}
	s, err := a.Open(ctx, openRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	request := helloRequest()
	ack, stream, err := s.Submit(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertProtocolValidWithSubmit(t, request.Request, ack, d, events)
	if len(events) != 3 {
		t.Fatalf("missing public events: %+v", events)
	}
	if s.(base.NativeSession).NativeSessionID() != "ses" {
		t.Fatal("native session reference missing")
	}
}

func TestPublicOpenRejectsMismatchedPins(t *testing.T) {
	for _, mode := range []string{"bad-protocol", "bad-version"} {
		t.Run(mode, func(t *testing.T) {
			a, err := New(factoryConfig(t, mode))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if s, err := a.Open(ctx, openRequest()); err == nil {
				_ = s.Close(ctx)
				t.Fatal("mismatched pin opened")
			}
		})
	}
}

func TestOpenRefusesUnavailableFeaturesBeforeStartingAProcess(t *testing.T) {
	a, err := New(Config{Executable: filepath.Join(t.TempDir(), "missing-executable"), WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	requests := []base.OpenRequest{openRequest(), openRequest(), openRequest(), openRequest(), openRequest()}
	requests[0].Reopen = true
	requests[1].NativeSessionID = "native"
	requests[2].ReasoningLevel = "high"
	requests[3].CompactionPolicy = &protocol.CompactionPolicy{}
	requests[4].Tools = []protocol.ToolDefinition{{Name: "synthetic"}}
	for _, request := range requests {
		_, err := a.Open(context.Background(), request)
		var refusal *base.UnsupportedControlError
		if !errors.As(err, &refusal) {
			t.Fatalf("feature reached process startup: %v", err)
		}
	}
}

func TestConfigurationRequiresAnExecutableAndAbsoluteDirectory(t *testing.T) {
	for _, c := range []Config{{}, {Executable: "copilot"}, {Executable: "copilot", WorkingDirectory: "relative"}, {Executable: "copilot", WorkingDirectory: t.TempDir(), Args: []string{"--"}}} {
		if _, err := New(c); err == nil {
			t.Fatalf("invalid configuration accepted: %+v", c)
		}
	}
}

func TestOneAdapterMintsSeparateOAPSessionIdentities(t *testing.T) {
	a, err := New(factoryConfig(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := openRequest()
	request.SessionID = ""
	first, err := a.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())
	second, err := a.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	x, err := first.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	y, err := second.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if x.SessionID == "" || x.SessionID == y.SessionID {
		t.Fatalf("session identities %q / %q", x.SessionID, y.SessionID)
	}
}
