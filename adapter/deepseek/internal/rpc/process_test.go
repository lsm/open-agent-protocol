package rpc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter/deepseek/internal/native"
)

func TestProcessInitializeBacklogEnvAndShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	envFile := filepath.Join(dir, "env")
	script := writeScript(t, dir, `printf '%s' "$*" > "$ARGS_FILE"
printf '%s' "${ONLY_ENV-unset}" > "$ENV_FILE"
IFS= read -r init
printf '%s\n' '{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"pre","status":"running"}}'
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}'
IFS= read -r shutdown
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{}}'
`)
	p, err := Start(context.Background(), ProcessConfig{Path: script, Args: []string{"one", "two"}, Env: []string{"ARGS_FILE=" + argsFile, "ENV_FILE=" + envFile, "ONLY_ENV=exact"}, Initialize: native.InitializeParams{Cwd: dir, Provider: "p", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	in := p.Client.Inbound()
	select {
	case msg := <-in:
		if msg.Notification == nil || msg.Notification.Method != native.NotifySessionStatus {
			t.Fatalf("backlog %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("missing startup notification")
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsFile)
	if string(args) != "one two" {
		t.Fatalf("args %q", args)
	}
	env, _ := os.ReadFile(envFile)
	if string(env) != "exact" {
		t.Fatalf("env %q", env)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("cached close: %v", err)
	}
}

func TestProcessRejectsIdentityAndRedactsBoundedStderr(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, `printf '%s\n' 'authorization: Bearer secret' >&2
printf '%0500d' 0 >&2
IFS= read -r init
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"wrong","version":"0.0.1"}}}'
sleep 10
`)
	_, err := Start(context.Background(), ProcessConfig{Path: script, StderrLimit: 64, Initialize: native.InitializeParams{Cwd: dir, Provider: "p", Model: "m"}})
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("secret leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("missing markers: %v", err)
	}
}

func TestProcessCloseKillsHungShutdownWithinBound(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, `IFS= read -r init
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}'
IFS= read -r shutdown
sleep 10
`)
	p, err := Start(context.Background(), ProcessConfig{Path: script, ShutdownTimeout: 50 * time.Millisecond, Initialize: native.InitializeParams{Cwd: dir, Provider: "p", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = p.Close(context.Background())
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("close err=%v elapsed=%v", err, time.Since(started))
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("child not reaped")
	}
}

func writeScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "fixture.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
