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
)

func TestProcessSpawnObservesAndEOFTearsDown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, `printf '%s\n' '{"type":"keep_alive"}'
# Drain stdin until genuine EOF, then exit cleanly.
while IFS= read -r line; do :; done
exit 0
`)
	process, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, Env: []string{}, ExitTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-process.Client.Inbound():
		if message.Observation == nil || message.Observation.Type != TypeKeepAlive {
			t.Fatalf("first inbound = %+v", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no observation arrived")
	}
	started := time.Now()
	if err := process.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("EOF teardown stalled for %v", elapsed)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatalf("cached close: %v", err)
	}
}

func TestProcessExitCodeIsNotACloseError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	// The CLI exits non-zero on purpose after an error result; settlement is
	// owned by wire evidence, so Close must not convert the exit code.
	script := writeScript(t, dir, `while IFS= read -r line; do :; done
exit 1
`)
	process, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, Env: []string{}, ExitTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if process.WaitError() == nil {
		t.Fatal("expected the wait error to record the non-zero exit")
	}
}

func TestProcessEmptyEnvAllowlistStaysEmpty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	script := writeScript(t, dir, `printf '%s' "${CLAUDE_ENV_PROBE-unset}" > "$1"
while IFS= read -r line; do :; done
`)
	t.Setenv("CLAUDE_ENV_PROBE", "ambient-value")
	process, err := Start(context.Background(), ProcessConfig{Path: script, Args: []string{envFile}, Dir: dir, Env: []string{}, ExitTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	probe, _ := os.ReadFile(envFile)
	if string(probe) != "unset" {
		t.Fatalf("ambient env leaked into the child: %q", string(probe))
	}
}

func TestProcessCloseBoundsChildThatIgnoresEOF(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, `# Ignore EOF: keep running until killed.
while true; do sleep 1; done
`)
	process, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, Env: []string{}, ExitTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = process.Close(context.Background())
	if err == nil || time.Since(started) > 5*time.Second {
		t.Fatalf("close err=%v elapsed=%v", err, time.Since(started))
	}
	if !errors.Is(err, ErrProcessClosed) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("child not reaped")
	}
}

func TestProcessMalformedFrameSurfacesReaderError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	// The native reader tolerates non-JSON lines; the adapter codec is
	// deliberately narrower and fails closed (recorded mismatch).
	script := writeScript(t, dir, `printf '%s\n' '[SandboxDebug] not json'
while IFS= read -r line; do :; done
`)
	process, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, Env: []string{}, ExitTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close(context.Background()) //nolint:errcheck
	select {
	case <-process.Client.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("malformed frame did not retire the client")
	}
	if !strings.Contains(process.Client.Err().Error(), "exactly one JSON object") {
		t.Fatalf("err = %v", process.Client.Err())
	}
}

func TestProcessRedactsBoundedStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, `printf '%s\n' 'x-api-key: supersecret' >&2
printf '%0500d' 0 >&2
while IFS= read -r line; do :; done
`)
	process, err := Start(context.Background(), ProcessConfig{Path: script, Dir: dir, Env: []string{}, StderrLimit: 64, ExitTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close(context.Background()) //nolint:errcheck
	deadline := time.Now().Add(5 * time.Second)
	for process.Stderr() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	captured := process.Stderr()
	if strings.Contains(captured, "supersecret") {
		t.Fatalf("secret leaked: %v", captured)
	}
	if !strings.Contains(captured, "[REDACTED]") || !strings.Contains(captured, "[truncated]") {
		t.Fatalf("missing markers: %v", captured)
	}
}

func TestProcessRequiresExecutable(t *testing.T) {
	if _, err := Start(context.Background(), ProcessConfig{}); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func writeScript(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "cli.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}
