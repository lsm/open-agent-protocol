package deepseek

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/deepseek/internal/rpc"
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
	// An explicit empty allowlist must stay an empty environment; inheriting
	// the ambient one would forward credentials to the child.
	if captured := run([]string{}); captured.Env == nil || len(captured.Env) != 0 {
		t.Fatalf("explicit empty allowlist must stay empty, got %#v", captured.Env)
	}
	if captured := run(nil); captured.Env != nil {
		t.Fatalf("unset environment must inherit the parent, got %#v", captured.Env)
	}
}
