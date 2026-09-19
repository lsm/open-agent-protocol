package rpc

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/native"
)

func TestDeepseekProcessHelper(t *testing.T) {
	if os.Getenv("OAP_DSH_RPC_HELPER") == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(11)
	}
	os.Stdout.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}` + "\n")
	if !scanner.Scan() {
		os.Exit(12)
	}
	blob := strings.Repeat("x", 1<<20)
	os.Stdout.WriteString(`{"jsonrpc":"2.0","id":2,"result":{"ok":true,"blob":"` + blob + `"}}` + "\n")
	os.Exit(0)
}

func deepseekHelperConfig(dir string) ProcessConfig {
	return ProcessConfig{
		Path:            os.Args[0],
		Args:            []string{"-test.run=TestDeepseekProcessHelper", "--"},
		Env:             append(os.Environ(), "OAP_DSH_RPC_HELPER=1"),
		ShutdownTimeout: 2 * time.Second,
		Initialize:      native.InitializeParams{Cwd: dir, Provider: "p", Model: "m"},
	}
}

func TestProcessInitializeEnvAndShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	envFile := filepath.Join(dir, "env")
	script := writeScript(t, dir, `printf '%s' "$*" > "$ARGS_FILE"
printf '%s' "${ONLY_ENV-unset}" > "$ENV_FILE"
IFS= read -r init
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}'
IFS= read -r shutdown
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{}}'
`)
	p, err := Start(context.Background(), ProcessConfig{Path: script, Args: []string{"one", "two"}, Env: []string{"ARGS_FILE=" + argsFile, "ENV_FILE=" + envFile, "ONLY_ENV=exact"}, Initialize: native.InitializeParams{Cwd: dir, Provider: "p", Model: "m"}})
	if err != nil {
		t.Fatal(err)
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

func TestProcessRejectsObservationBeforeInitializeResponse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, `IFS= read -r init
printf '%s\n' '{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"pre","status":"running"}}'
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}'
sleep 10
`)

	_, err := Start(context.Background(), ProcessConfig{Path: script, Initialize: native.InitializeParams{Cwd: dir, Provider: "p", Model: "m"}})
	if !errors.Is(err, ErrHandshake) || !strings.Contains(err.Error(), "preceded initialize response") {
		t.Fatalf("got %v", err)
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

func TestProcessCloseDeliversShutdownResponse(t *testing.T) {

	dir := t.TempDir()
	script := writeScript(t, dir, `IFS= read -r init
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}'
IFS= read -r shutdown
printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{}}'
IFS= read -r eof || exit 0
`)
	p, err := Start(context.Background(), ProcessConfig{Path: script, Initialize: native.InitializeParams{Cwd: dir, Provider: "p", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("close stalled for %v", elapsed)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("child not reaped")
	}
}

func TestProcessCallSurvivesChildExitImmediatelyAfterResponse(t *testing.T) {
	for iteration := range 10 {
		p, err := Start(context.Background(), deepseekHelperConfig(t.TempDir()))
		if err != nil {
			t.Fatalf("iteration %d: start: %v", iteration, err)
		}

		go func() {
			for {
				select {
				case message := <-p.Client.Inbound():
					if message.Barrier != nil {
						close(message.Barrier)
					}
				case <-p.Client.Done():
					return
				}
			}
		}()
		var result struct {
			OK   bool   `json:"ok"`
			Blob string `json:"blob"`
		}
		if err := p.Client.Call(context.Background(), "session/probe", nil, &result); err != nil {
			t.Fatalf("iteration %d: call failed despite a written response: %v", iteration, err)
		}
		if !result.OK || len(result.Blob) != 1<<20 {
			t.Fatalf("iteration %d: response was not fully decoded", iteration)
		}
		_ = p.Close(context.Background())
	}
}

func TestProcessEmptyEnvAllowlistStaysEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	script := writeScript(t, dir, `printf '%s' "${DSH_ENV_PROBE-unset}" > "$1"
IFS= read -r init
printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}}'
IFS= read -r eof || exit 0
`)
	t.Setenv("DSH_ENV_PROBE", "ambient-value")
	p, err := Start(context.Background(), ProcessConfig{Path: script, Args: []string{envFile}, Env: []string{}, Initialize: native.InitializeParams{Cwd: dir, Provider: "p", Model: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	probe, _ := os.ReadFile(envFile)
	if string(probe) != "unset" {
		t.Fatalf("ambient env leaked into the child: %q", string(probe))
	}
}

func TestRedactHidesUnquotedSecrets(t *testing.T) {
	for input, want := range map[string]string{
		"Authorization: Bearer secret-token": "Authorization: [REDACTED]",
		"password=secret-value":              "password=[REDACTED]",
		"secret: value":                      "secret: [REDACTED]",
		"token=value":                        "token=[REDACTED]",
	} {
		if got := redact(input); got != want {
			t.Fatalf("redact %q = %q want %q", input, got, want)
		}
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
