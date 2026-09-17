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
)

const readyFrame = `{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{"name":"default"},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}}`

func TestHermesProcessHelper(t *testing.T) {
	if os.Getenv("OAP_HERMES_RPC_HELPER") == "" {
		return
	}
	os.Stdout.WriteString(readyFrame + "\n")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(11)
	}
	blob := strings.Repeat("x", 1<<20)
	os.Stdout.WriteString(`{"jsonrpc":"2.0","id":1,"result":{"ok":true,"blob":"` + blob + `"}}` + "\n")
	os.Exit(0)
}

func hermesHelperConfig() ProcessConfig {
	return ProcessConfig{
		Path:        os.Args[0],
		Args:        []string{"-test.run=TestHermesProcessHelper", "--"},
		Env:         append(os.Environ(), "OAP_HERMES_RPC_HELPER=1"),
		ExitTimeout: 2 * time.Second,
	}
}

func TestProcessReadyHandshakeAndEOFTeardown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, `printf '%s\n' '`+readyFrame+`'
# Drain requests until stdin reaches genuine EOF, then exit cleanly.
while IFS= read -r line; do :; done
exit 0
`)
	p, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, Env: []string{}, ExitTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if p.Ready.ReplayEpoch != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("epoch = %q", p.Ready.ReplayEpoch)
	}
	started := time.Now()
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("EOF teardown stalled for %v", elapsed)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("cached close: %v", err)
	}
}

func TestProcessEmptyEnvAllowlistStaysEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	script := writeScript(t, dir, `printf '%s' "${HERMES_ENV_PROBE-unset}" > "$1"
printf '%s\n' '`+readyFrame+`'
while IFS= read -r line; do :; done
`)
	t.Setenv("HERMES_ENV_PROBE", "ambient-value")
	p, err := Start(context.Background(), ProcessConfig{Path: script, Args: []string{envFile}, Dir: dir, Env: []string{}, ExitTimeout: 5 * time.Second})
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

func TestProcessRejectsNonReadyFirstObservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	cases := map[string]string{
		"event before ready":   `{"jsonrpc":"2.0","method":"event","params":{"type":"skin.changed","session_id":"","payload":{}}}`,
		"session event":        `{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"s","seq":1}}`,
		"unsolicited response": `{"jsonrpc":"2.0","id":1,"result":{}}`,
	}
	for name, first := range cases {
		script := writeScript(t, dir, `printf '%s\n' '`+first+`'
printf '%s\n' '`+readyFrame+`'
sleep 10
`)
		_, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, ExitTimeout: time.Second})
		if !errors.Is(err, ErrHandshake) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestProcessRejectsInvalidReady(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, `printf '%s\n' '{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"short"}}}'
sleep 10
`)
	_, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, ExitTimeout: time.Second})
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "ready") {
		t.Fatalf("rejection did not name the ready payload: %v", err)
	}
}

func TestProcessCloseBoundsGatewayThatIgnoresEOF(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, `printf '%s\n' '`+readyFrame+`'
# Ignore EOF: keep running until killed.
while true; do sleep 1; done
`)
	p, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, Env: []string{}, ExitTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = p.Close(context.Background())
	if err == nil || time.Since(started) > 5*time.Second {
		t.Fatalf("close err=%v elapsed=%v", err, time.Since(started))
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("child not reaped")
	}
}

func TestProcessRedactsBoundedStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, `printf '%s\n' 'authorization: Bearer secret' >&2
printf '%0500d' 0 >&2
printf '%s\n' 'garbage-not-json'
sleep 10
`)
	_, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, StderrLimit: 64, ExitTimeout: time.Second})
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("secret leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("missing markers: %v", err)
	}
}

func TestProcessCallSurvivesChildExitImmediatelyAfterResponse(t *testing.T) {
	for iteration := range 10 {
		p, err := Start(context.Background(), hermesHelperConfig())
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
		if err := p.Client.Call(context.Background(), "probe", nil, &result); err != nil {
			t.Fatalf("iteration %d: call failed despite a written response: %v", iteration, err)
		}
		if !result.OK || len(result.Blob) != 1<<20 {
			t.Fatalf("iteration %d: response was not fully decoded", iteration)
		}
		_ = p.Close(context.Background())
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
	path := filepath.Join(dir, "gateway.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
