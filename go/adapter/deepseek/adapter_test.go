package deepseek

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestEnvironmentAllowlistNilness(t *testing.T) {
	run := func(env []string) rpc.ProcessConfig {
		var captured rpc.ProcessConfig
		factory := ProcessFactoryFunc(func(_ context.Context, c rpc.ProcessConfig) (ProcessBridge, error) {
			captured = c
			return nil, errors.New("capture complete")
		})
		implementation, err := New(Config{Executable: "/bin/true", WorkingDirectory: filepath.Join(t.TempDir()), Environment: env, Provider: "p", Model: "m", ProcessFactory: factory})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := implementation.Open(context.Background(), base.OpenRequest{SessionID: "session"}); err == nil {
			t.Fatal("expected the process factory to stop the open")
		}
		return captured
	}
	if captured := run([]string{"A=1"}); len(captured.Env) != 1 {
		t.Fatalf("explicit allowlist lost: %#v", captured.Env)
	}

	if captured := run([]string{}); captured.Env == nil || len(captured.Env) != 0 {
		t.Fatalf("explicit empty allowlist must stay empty, got %#v", captured.Env)
	}
	if captured := run(nil); captured.Env != nil {
		t.Fatalf("unset environment must inherit the parent, got %#v", captured.Env)
	}
}

func TestAnOpenStartsItsOwnRuntimeWithTheEffortAndACompactionPatch(t *testing.T) {
	var captured rpc.ProcessConfig
	var patch string
	factory := ProcessFactoryFunc(func(_ context.Context, c rpc.ProcessConfig) (ProcessBridge, error) {
		captured = c
		for index, arg := range c.Args {
			if arg == "--patch" && index+1 < len(c.Args) {
				data, err := os.ReadFile(c.Args[index+1])
				if err != nil {
					return nil, err
				}
				patch = string(data)
			}
		}
		return nil, errors.New("capture complete")
	})
	implementation, err := New(Config{Executable: "/bin/true", WorkingDirectory: t.TempDir(), Args: []string{"--profile", "sdk"}, Provider: "p", Model: "m", ProcessFactory: factory})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = implementation.Open(context.Background(), base.OpenRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningMax, CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 70}})
	if captured.Initialize.ReasoningEffort != "max" {
		t.Fatalf("initialize carried effort %q, want max", captured.Initialize.ReasoningEffort)
	}
	if patch != "- id: compaction-basic\n  config:\n    thresholdRatio: 0.7\n" {
		t.Fatalf("patch layer = %q, want compaction-basic's thresholdRatio at 0.7", patch)
	}
	if captured.Args[0] != "--profile" || captured.Args[2] != "--patch" {
		t.Fatalf("args = %v, want the configured args followed by the patch layer", captured.Args)
	}
	if _, err := os.Stat(captured.Args[3]); !os.IsNotExist(err) {
		t.Fatalf("the patch file outlived the start: %v", err)
	}

	for name, request := range map[string]base.OpenRequest{
		"medium": {SessionID: "s", ReasoningLevel: protocol.ReasoningMedium},
		"tokens": {SessionID: "s", CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 1000}},
	} {
		captured = rpc.ProcessConfig{}
		_, err := implementation.Open(context.Background(), request)
		var refusal *base.UnsupportedControlError
		if !errors.As(err, &refusal) || captured.Path != "" {
			t.Fatalf("%s: open answered %v (started %v), want a refusal before the runtime starts", name, err, captured.Path != "")
		}
	}
}

func TestASuppliedFactoryRefusesTheSettingTheOpenCarriedAndRunsAnAutoPolicy(t *testing.T) {
	started := 0
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) {
		started++
		return nil, "", errors.New("factory reached")
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = implementation.Open(context.Background(), base.OpenRequest{SessionID: "s", CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureCompactionPolicy || refusal.Field != "compaction_policy" || started != 0 {
		t.Fatalf("open answered %v (factory started %d times), want the compaction policy named", err, started)
	}
	_, err = implementation.Open(context.Background(), base.OpenRequest{SessionID: "s", CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionAuto}})
	if started != 1 || errors.As(err, &refusal) {
		t.Fatalf("an auto policy answered %v (factory started %d times), want it to need no configuration", err, started)
	}
}

func TestReopenIsDeclinedBeforeTheRuntimeStarts(t *testing.T) {
	started := 0
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) {
		started++
		return nil, "", errors.New("a declined reopen must not start a runtime")
	})})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if support := descriptor.Capabilities.Features[protocol.FeatureOpenReopen]; support.Level != protocol.SupportUnavailable {
		t.Fatalf("reopen advertised as %q, want unavailable", support.Level)
	}
	_, err = implementation.Open(context.Background(), base.OpenRequest{SessionID: "s", Reopen: true, NativeSessionID: "stored"})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnadvertised || started != 0 {
		t.Fatalf("refusal = %v, runtimes started = %d", err, started)
	}
}
