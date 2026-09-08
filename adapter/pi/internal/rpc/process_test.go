package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
)

func TestPiProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PI_HELPER") != "1" {
		return
	}
	args := strings.Join(os.Args, "\x00")
	if !strings.Contains(args, "--mode\x00rpc") {
		os.Stderr.WriteString("bad args: " + strings.Join(os.Args, " "))
		os.Exit(3)
	}
	mode := os.Getenv("PI_HELPER_MODE")
	switch mode {
	case "ready", "inherit":
		if mode == "inherit" && os.Getenv("PI_INHERITED") != "yes" {
			os.Stderr.WriteString("missing inherited env")
			os.Exit(4)
		}
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			os.Exit(5)
		}
		var command native.Command
		if json.Unmarshal(scanner.Bytes(), &command) != nil || command.Type != native.CommandGetState {
			os.Exit(6)
		}
		os.Stdout.WriteString(`{"id":"` + command.ID + `","type":"response","command":"get_state","success":true,"data":{"thinkingLevel":"high","isStreaming":false,"isCompacting":false,"steeringMode":"all","followUpMode":"one-at-a-time","sessionId":"session-1","autoCompactionEnabled":true,"messageCount":0,"pendingMessageCount":0}}` + "\n")
		for scanner.Scan() {
		}
	case "bad":
		os.Stderr.WriteString("authorization: secret-value\n" + strings.Repeat("x", 256))
		os.Stdout.WriteString("noise\n")
		os.Exit(2)
	case "hang":
		select {}
	case "badstate":
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		var command native.Command
		_ = json.Unmarshal(scanner.Bytes(), &command)
		os.Stdout.WriteString(`{"id":"` + command.ID + `","type":"response","command":"get_state","success":true,"data":{"steeringMode":"all","followUpMode":"all","sessionId":""}}` + "\n")
		select {}
	}
}
func helperConfig(mode string) ProcessConfig {
	return ProcessConfig{Path: os.Args[0], Args: []string{"-test.run=TestPiProcessHelper", "--"}, Env: append(os.Environ(), "GO_WANT_PI_HELPER=1", "PI_HELPER_MODE="+mode), ShutdownTimeout: 2 * time.Second, StderrLimit: 64}
}

func TestProcessGetStateHandshakeArgumentsAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := Start(ctx, helperConfig("ready"))
	if err != nil {
		t.Fatal(err)
	}
	if p.InitialState.SessionID != "session-1" || p.InitialState.ThinkingLevel != native.ThinkingHigh {
		t.Fatalf("%+v", p.InitialState)
	}
	closeCtx, cc := context.WithTimeout(context.Background(), 3*time.Second)
	defer cc()
	if err := p.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	default:
		t.Fatal("not reaped")
	}
}

func TestProcessHandshakeFailureRedactsAndBoundsStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Start(ctx, helperConfig("bad"))
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "secret-value") || !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("%v", err)
	}
}
func TestProcessInvalidInitialState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := Start(ctx, helperConfig("badstate"))
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("%v", err)
	}
}
func TestProcessHandshakeCancellationReaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Start(ctx, helperConfig("hang"))
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("not reaped")
	}
}

func TestProcessNilEnvironmentInheritsParent(t *testing.T) {
	t.Setenv("GO_WANT_PI_HELPER", "1")
	t.Setenv("PI_HELPER_MODE", "inherit")
	t.Setenv("PI_INHERITED", "yes")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	config := helperConfig("inherit")
	config.Env = nil
	p, err := Start(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, cc := context.WithTimeout(context.Background(), 3*time.Second)
	defer cc()
	if err := p.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

func TestRedactBearerCredential(t *testing.T) {
	value := redact("Authorization: Bearer secret-token")
	if strings.Contains(value, "secret-token") || !strings.Contains(value, "[REDACTED]") {
		t.Fatalf("%q", value)
	}
}

func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{limit: 3}
	n, err := b.Write([]byte("abcdef"))
	if err != nil || n != 6 || b.String() != "abc [truncated]" {
		t.Fatalf("%d %v %q", n, err, b.String())
	}
}
