package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("OAP_CODEX_RPC_HELPER") == "" {
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
	mode := os.Getenv("OAP_CODEX_RPC_HELPER")
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
			os.Exit(0)
		}
		if message.Kind == MessageRequest {
			_ = NewEncoder(os.Stdout).Encode(Response(message.ID, json.RawMessage(`{"ok":true}`)))
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
	if err := process.Close(context.Background()); err != nil && process.WaitError() != nil {
		t.Fatal(err)
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
