package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
)

func TestSyntheticProcessChild(t *testing.T) {
	if os.Getenv("OAP_COPILOT_SYNTHETIC_CHILD") != "1" {
		return
	}
	wire := NewCodec(os.Stdin, os.Stdout, 0)
	for {
		body, err := wire.Read()
		if err != nil {
			os.Exit(0)
		}
		m, err := native.Parse(body)
		if err != nil {
			os.Exit(2)
		}
		if m.Method == "stall" {
			for {
				time.Sleep(time.Hour)
			}
		}
		if m.Method == "malformed" {
			_ = wire.Write([]byte(`{"jsonrpc":"1.0"}`))
			continue
		}
		result := map[string]any{"protocolVersion": 3}
		if m.Method == "home" {
			result["home"] = os.Getenv("HOME")
			result["ambient"] = os.Getenv("OAP_COPILOT_AMBIENT_SENTINEL")
		}
		body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		if wire.Write(body) != nil {
			os.Exit(3)
		}
	}
}

func syntheticProcess(t *testing.T) (*Process, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	p, err := Start(ProcessConfig{Path: exe, Args: []string{"-test.run=^TestSyntheticProcessChild$"}, Dir: home, Env: []string{"HOME=" + home, "OAP_COPILOT_SYNTHETIC_CHILD=1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	})
	return p, home
}

func TestProcessUsesOnlyExplicitEnvironmentAndReapsChild(t *testing.T) {
	t.Setenv("OAP_COPILOT_AMBIENT_SENTINEL", "must-not-leak")
	p, home := syntheticProcess(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var got struct {
		Home    string `json:"home"`
		Ambient string `json:"ambient"`
	}
	if err := p.Client.Call(ctx, "home", struct{}{}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Home != home || got.Ambient != "" {
		t.Fatalf("environment isolation: %+v", got)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessCancellationAndBrokenWireStopChild(t *testing.T) {
	for _, method := range []string{"stall", "malformed"} {
		t.Run(method, func(t *testing.T) {
			p, _ := syntheticProcess(t)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			err := p.Client.Call(ctx, method, struct{}{}, nil)
			if err == nil {
				t.Fatal("expected failure")
			}
			if method == "stall" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("stalled call ended before cancellation: %v", err)
			}
			select {
			case <-p.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("failed process not reaped")
			}
		})
	}
}

func TestProcessStartFailure(t *testing.T) {
	if _, err := Start(ProcessConfig{Path: filepath.Join(t.TempDir(), "not-a-program")}); err == nil {
		t.Fatal("missing executable accepted")
	}
}
