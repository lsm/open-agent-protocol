package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	// Activated either by environment or by an explicit argument, so the
	// empty-allowlist fixture can be spawned with no environment at all.
	mode := os.Getenv("OAP_CODEX_RPC_HELPER")
	if strings.Contains(strings.Join(os.Args, "\x00"), "--codex-emptyenv") {
		if os.Getenv("CODEX_ENV_PROBE") != "" {
			os.Exit(14)
		}
		mode = "emptyenv"
	}
	if mode == "" {
		return
	}
	args := os.Args
	if len(args) < 3 || strings.Join(args[len(args)-3:], "\x00") != "app-server\x00--listen\x00stdio://" {
		os.Exit(10)
	}
	reader := NewDecoder(os.Stdin, DefaultFrameLimit)
	message, err := reader.Decode()
	if err != nil || message.Kind != MessageRequest || message.Method != "initialize" || message.ID != IntegerID(0) {
		os.Exit(11)
	}
	var initialize struct {
		ClientInfo ClientInfo `json:"clientInfo"`
	}
	if json.Unmarshal(message.Params, &initialize) != nil || initialize.ClientInfo.Name != "open-agent-protocol" || initialize.ClientInfo.Version != "0.1" {
		os.Exit(13)
	}
	if mode == "stderr-error" {
		_, _ = fmt.Fprintln(os.Stderr, "Authorization: secret-value")
		_ = NewEncoder(os.Stdout).Encode(ErrorResponse(message.ID, ErrorObject{Code: -32000, Message: "no"}))
		return
	}
	if mode == "wrong-id" {
		message.ID = IntegerID(99)
	}
	result := json.RawMessage(`{"userAgent":"codex-test","codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"linux"}`)
	_ = NewEncoder(os.Stdout).Encode(Response(message.ID, result))
	initialized, err := reader.Decode()
	if err != nil || initialized.Kind != MessageNotification || initialized.Method != "initialized" || len(initialized.Params) != 0 {
		os.Exit(12)
	}
	for {
		message, err = reader.Decode()
		if err != nil {
			if mode == "stay-alive" {
				select {}
			}
			os.Exit(0)
		}
		if message.Kind == MessageRequest {
			if mode == "large-response" {
				// A frame far larger than the pipe buffer keeps the reader busy
				// past the child's exit, which is the window this exercises.
				blob := strings.Repeat("x", 1<<20)
				_ = NewEncoder(os.Stdout).Encode(Response(message.ID, json.RawMessage(`{"ok":true,"blob":"`+blob+`"}`)))
				os.Exit(0)
			}
			_ = NewEncoder(os.Stdout).Encode(Response(message.ID, json.RawMessage(`{"ok":true}`)))
			if mode == "stay-alive" {
				continue
			}
			os.Exit(0)
		}
	}
}

func helperConfig(mode string) ProcessConfig {
	return ProcessConfig{
		Path:            os.Args[0],
		Args:            []string{"-test.run=TestHelperProcess", "--"},
		Env:             append(os.Environ(), "OAP_CODEX_RPC_HELPER="+mode),
		ClientInfo:      ClientInfo{Name: "open-agent-protocol", Version: "0.1"},
		ShutdownTimeout: time.Second,
	}
}

// envlessHelperConfig spawns the helper with an explicitly empty allowlist, so
// the child sees no parent variables at all.
func envlessHelperConfig() ProcessConfig {
	return ProcessConfig{
		Path:            os.Args[0],
		Args:            []string{"-test.run=TestHelperProcess", "--", "--codex-emptyenv"},
		Env:             []string{},
		ClientInfo:      ClientInfo{Name: "open-agent-protocol", Version: "0.1"},
		ShutdownTimeout: 5 * time.Second,
	}
}

// An explicitly empty allowlist must reach the child as an empty environment,
// not collapse to nil and inherit the parent's variables.
func TestProcessEmptyEnvAllowlistStaysEmpty(t *testing.T) {
	t.Setenv("CODEX_ENV_PROBE", "ambient-value")
	process, err := Start(context.Background(), envlessHelperConfig())
	if err != nil {
		t.Fatalf("empty environment did not stay empty: %v", err)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStartPerformsHandshakeAndCalls(t *testing.T) {
	process, err := Start(context.Background(), helperConfig("success"))
	if err != nil {
		t.Fatal(err)
	}
	if process.Initialize.UserAgent != "codex-test" || process.Initialize.PlatformOS != "linux" {
		t.Fatalf("initialize: %+v", process.Initialize)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := process.Client.Call(context.Background(), "thread/start", map[string]any{}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatal("call result was not decoded")
	}
	if err := process.Close(context.Background()); err != nil && !strings.Contains(err.Error(), "process exited") && !strings.Contains(err.Error(), "shutdown timed out") {
		t.Fatal(err)
	}
}

// The helper writes each response and then exits immediately. The reader must
// be allowed to drain the buffered frame before the process owner fails
// pending calls, otherwise this call races the child's exit and intermittently
// reports a process-exit error for a response that was already written.
func TestCallSurvivesChildExitImmediatelyAfterResponse(t *testing.T) {
	for iteration := range 10 {
		process, err := Start(context.Background(), helperConfig("large-response"))
		if err != nil {
			t.Fatalf("iteration %d: start: %v", iteration, err)
		}
		var result struct {
			OK   bool   `json:"ok"`
			Blob string `json:"blob"`
		}
		if err := process.Client.Call(context.Background(), "thread/start", map[string]any{}, &result); err != nil {
			t.Fatalf("iteration %d: call failed despite a written response: %v", iteration, err)
		}
		if !result.OK || len(result.Blob) != 1<<20 {
			t.Fatalf("iteration %d: response was not fully decoded", iteration)
		}
		_ = process.Close(context.Background())
	}
}

func TestProcessConcurrentCloseReturnsSameResult(t *testing.T) {
	config := helperConfig("stay-alive")
	config.ShutdownTimeout = time.Millisecond
	process, err := Start(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- process.Close(context.Background())
		}()
	}
	wait.Wait()
	close(results)
	var first string
	for err := range results {
		if err == nil {
			t.Fatal("concurrent close returned nil")
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("close errors differ: %q and %q", first, err)
		}
	}
	if err := process.Close(context.Background()); err == nil || err.Error() != first {
		t.Fatalf("repeated close=%v want=%q", err, first)
	}
}

func TestStartReportsRemoteHandshakeErrorWithRedactedStderr(t *testing.T) {
	_, err := Start(context.Background(), helperConfig("stderr-error"))
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("stderr was not redacted: %v", err)
	}
}

func TestStartRejectsWrongHandshakeID(t *testing.T) {
	_, err := Start(context.Background(), helperConfig("wrong-id"))
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
}

func TestLimitedBufferBoundsAndRedacts(t *testing.T) {
	buffer := &limitedBuffer{limit: 16}
	input := strings.Repeat("x", 32)
	written, err := buffer.Write([]byte(input))
	if err != nil || written != len(input) || !strings.Contains(buffer.String(), "truncated") {
		t.Fatalf("write=%d err=%v value=%q", written, err, buffer.String())
	}
	if got := redact("x-api-key=secret"); got != "x-api-key=[REDACTED]" {
		t.Fatalf("redact: %q", got)
	}
}

func TestInitializedNotificationHasNoParams(t *testing.T) {
	var output strings.Builder
	if err := NewEncoder(&output).Encode(Notification("initialized", nil)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(strings.NewReader(output.String())).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "{\"method\":\"initialized\"}\n" {
		t.Fatalf("wire: %q", line)
	}
}
