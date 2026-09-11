package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestACPHelperProcess(t *testing.T) {
	// The descendant that inherits the pipes and outlives the direct child only
	// needs to hold them open for longer than the test's guard.
	if os.Getenv("OAP_ACP_RPC_HOLDER") == "1" {
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	// Activated either by environment or by an explicit argument, so the
	// empty-allowlist fixture can be spawned with no environment at all.
	mode := os.Getenv("OAP_ACP_RPC_HELPER")
	if strings.Contains(strings.Join(os.Args, "\x00"), "--acp-emptyenv") {
		if os.Getenv("ACP_ENV_PROBE") != "" {
			os.Exit(13)
		}
		mode = "emptyenv"
	}
	if mode == "" {
		return
	}
	reader, writer := NewDecoder(os.Stdin, DefaultFrameLimit), NewEncoder(os.Stdout)
	message, err := reader.Decode()
	if err != nil || message.Kind != MessageRequest || message.Method != "initialize" || message.ID != IntegerID(1) {
		os.Exit(11)
	}
	var initialize InitializeRequest
	if json.Unmarshal(message.Params, &initialize) != nil || initialize.ProtocolVersion != 1 || initialize.ClientCapabilities == nil || initialize.ClientInfo == nil || initialize.ClientInfo.Name != "open-agent-protocol" {
		os.Exit(12)
	}
	if mode == "stderr-error" {
		_, _ = fmt.Fprintln(os.Stderr, "Authorization: secret-value")
		_ = writer.Encode(ErrorResponse(message.ID, ErrorObject{Code: -32000, Message: "authentication required"}))
		return
	}
	responseID := message.ID
	if mode == "wrong-id" {
		responseID = IntegerID(99)
	}
	version := 1
	if mode == "wrong-version" || mode == "wrong-version-hold" {
		version = 2
	}
	if mode == "wrong-version-hold" {
		// Spawn a descendant that inherits stdout/stderr and outlives this
		// process, so the pipes stay open after the direct child exits.
		descendant := exec.Command(os.Args[0], "-test.run=TestACPHelperProcess", "--")
		descendant.Env = append(os.Environ(), "OAP_ACP_RPC_HOLDER=1")
		descendant.Stdout = os.Stdout
		descendant.Stderr = os.Stderr
		if err := descendant.Start(); err != nil {
			os.Exit(20)
		}
	}
	result, _ := json.Marshal(InitializeResponse{
		ProtocolVersion: version, AgentCapabilities: AgentCapabilities{},
		AgentInfo: &Implementation{Name: "test-agent", Version: "1.0"},
	})
	_ = writer.Encode(Response(responseID, result))
	for {
		message, err = reader.Decode()
		if err != nil {
			if mode == "stay-alive" {
				select {}
			}
			return
		}
		if message.Kind == MessageRequest {
			if mode == "large-response" {
				// A frame far larger than the pipe buffer keeps the reader busy
				// past the child's exit, which is the window this exercises.
				blob := strings.Repeat("x", 1<<20)
				_ = writer.Encode(Response(message.ID, json.RawMessage(`{"ok":true,"blob":"`+blob+`"}`)))
				return
			}
			_ = writer.Encode(Response(message.ID, json.RawMessage(`{"ok":true}`)))
			if mode != "stay-alive" {
				return
			}
		}
	}
}

func helperConfig(mode string) ProcessConfig {
	return ProcessConfig{
		Path: os.Args[0], Args: []string{"-test.run=TestACPHelperProcess", "--"},
		Env:                append(os.Environ(), "OAP_ACP_RPC_HELPER="+mode),
		ClientInfo:         &Implementation{Name: "open-agent-protocol", Version: "0.1"},
		ClientCapabilities: ClientCapabilities{}, ShutdownTimeout: time.Second,
	}
}

// envlessHelperConfig spawns the helper with an explicitly empty allowlist, so
// the child sees no parent variables at all.
func envlessHelperConfig() ProcessConfig {
	return ProcessConfig{
		Path: os.Args[0], Args: []string{"-test.run=TestACPHelperProcess", "--", "--acp-emptyenv"},
		Env:                []string{},
		ClientInfo:         &Implementation{Name: "open-agent-protocol", Version: "0.1"},
		ClientCapabilities: ClientCapabilities{}, ShutdownTimeout: 5 * time.Second,
	}
}

// An explicitly empty allowlist must reach the child as an empty environment,
// not collapse to nil and inherit the parent's variables.
func TestProcessEmptyEnvAllowlistStaysEmpty(t *testing.T) {
	t.Setenv("ACP_ENV_PROBE", "ambient-value")
	process, err := Start(context.Background(), envlessHelperConfig())
	if err != nil {
		t.Fatalf("empty environment did not stay empty: %v", err)
	}
	if err := process.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A handshake validation failure aborts the process. If a descendant inherited
// stdout, the reader's drain never observes EOF, so abort must release the
// pipes before waiting rather than hang Start indefinitely.
func TestHandshakeAbortBoundsWhenDescendantHoldsPipes(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := Start(context.Background(), helperConfig("wrong-version-hold"))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrHandshake) {
			t.Fatalf("got %v, want ErrHandshake", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start hung in abort while a descendant held the pipes open")
	}
}

func TestStartPerformsACPInitializeHandshakeAndCalls(t *testing.T) {
	process, err := Start(context.Background(), helperConfig("success"))
	if err != nil {
		t.Fatal(err)
	}
	if process.Initialize.ProtocolVersion != 1 || process.Initialize.AgentInfo == nil || process.Initialize.AgentInfo.Name != "test-agent" {
		t.Fatalf("initialize: %+v", process.Initialize)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := process.Client.Call(context.Background(), "session/new", map[string]any{"cwd": "/tmp", "mcpServers": []any{}}, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatal("response not decoded")
	}
	_ = process.Close(context.Background())
}

func TestStartRejectsUnsupportedRequestedOrSelectedVersion(t *testing.T) {
	config := helperConfig("success")
	config.ProtocolVersion = 2
	if _, err := Start(context.Background(), config); !errors.Is(err, ErrHandshake) {
		t.Fatalf("requested: %v", err)
	}
	if _, err := Start(context.Background(), helperConfig("wrong-version")); !errors.Is(err, ErrHandshake) {
		t.Fatalf("selected: %v", err)
	}
}

func TestStartRejectsWrongHandshakeID(t *testing.T) {
	if _, err := Start(context.Background(), helperConfig("wrong-id")); !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
}

func TestStartReportsRedactedBoundedStderr(t *testing.T) {
	config := helperConfig("stderr-error")
	config.StderrLimit = 64
	_, err := Start(context.Background(), config)
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("not redacted: %v", err)
	}
}

// The helper writes its response and then exits immediately. The process owner
// must let the reader drain the buffered frame before closing the pipes and
// failing pending calls, or a response that was already written becomes a
// closed-pipe error on the pending call.
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
		if err := process.Client.Call(context.Background(), "session/prompt", map[string]any{}, &result); err != nil {
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
		go func() { defer wait.Done(); results <- process.Close(context.Background()) }()
	}
	wait.Wait()
	close(results)
	var first string
	for err := range results {
		if err == nil {
			t.Fatal("nil close result")
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("different results %q %q", first, err)
		}
	}
	if err := process.Close(context.Background()); err == nil || err.Error() != first {
		t.Fatalf("repeat=%v want=%q", err, first)
	}
}

func TestLimitedBufferBoundsAndRedacts(t *testing.T) {
	buffer := &limitedBuffer{limit: 16}
	input := strings.Repeat("x", 32)
	written, err := buffer.Write([]byte(input))
	if err != nil || written != len(input) || !strings.Contains(buffer.String(), "truncated") {
		t.Fatalf("write=%d err=%v value=%q", written, err, buffer.String())
	}
	for input, want := range map[string]string{
		"x-api-key=secret": "x-api-key=[REDACTED]", "Authorization: Bearer": "Authorization: [REDACTED]", "auth_token = abc": "auth_token = [REDACTED]",
	} {
		if got := redact(input); got != want {
			t.Errorf("redact %q = %q want %q", input, got, want)
		}
	}
}
