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
	// Activated either by environment (fixtures that inherit or set an env) or
	// by an explicit argument, so the empty-allowlist fixture can be spawned
	// with no environment at all.
	envless := strings.Contains(strings.Join(os.Args, "\x00"), "--pi-emptyenv")
	mode := os.Getenv("PI_HELPER_MODE")
	if mode == "" && !envless {
		return
	}
	if envless {
		mode = "emptyenv"
	}
	args := strings.Join(os.Args, "\x00")
	if !strings.Contains(args, "--mode\x00rpc") {
		os.Stderr.WriteString("bad args: " + strings.Join(os.Args, " "))
		os.Exit(3)
	}
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
	case "large":
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			os.Exit(7)
		}
		var command native.Command
		if json.Unmarshal(scanner.Bytes(), &command) != nil || command.Type != native.CommandGetState {
			os.Exit(8)
		}
		os.Stdout.WriteString(`{"id":"` + command.ID + `","type":"response","command":"get_state","success":true,"data":{"thinkingLevel":"high","isStreaming":false,"isCompacting":false,"steeringMode":"all","followUpMode":"one-at-a-time","sessionId":"session-1","autoCompactionEnabled":true,"messageCount":0,"pendingMessageCount":0}}` + "\n")
		if !scanner.Scan() {
			os.Exit(9)
		}
		var probe native.Command
		if json.Unmarshal(scanner.Bytes(), &probe) != nil {
			os.Exit(10)
		}
		// A frame far larger than the pipe buffer keeps the reader busy past the
		// child's exit, which is the window this exercises.
		blob := strings.Repeat("x", 1<<20)
		os.Stdout.WriteString(`{"id":"` + probe.ID + `","type":"response","command":"` + string(probe.Type) + `","success":true,"data":{"ok":true,"blob":"` + blob + `"}}` + "\n")
		os.Exit(0)
	case "emptyenv":
		// A parent-inherited probe means the empty allowlist collapsed to nil.
		if os.Getenv("PI_ENV_PROBE") != "" {
			os.Exit(13)
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

// envlessHelperConfig spawns the helper with an explicitly empty allowlist, so
// the child sees no parent variables at all.
func envlessHelperConfig() ProcessConfig {
	return ProcessConfig{Path: os.Args[0], Args: []string{"-test.run=TestPiProcessHelper", "--", "--pi-emptyenv"}, Env: []string{}, ShutdownTimeout: 2 * time.Second, StderrLimit: 64}
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
	start := time.Now()
	_, err := Start(ctx, helperConfig("badstate"))
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "invalid get_state response") {
		t.Fatalf("%v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("invalid state rejection took %v", elapsed)
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

// The helper writes its response and then exits immediately. The reader must be
// allowed to drain the buffered frame before the process owner reaps the child,
// otherwise this call races the exit and reports a process-exit error for a
// response that was already written.
func TestProcessCallSurvivesChildExitImmediatelyAfterResponse(t *testing.T) {
	for iteration := range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		p, err := Start(ctx, helperConfig("large"))
		cancel()
		if err != nil {
			t.Fatalf("iteration %d: start: %v", iteration, err)
		}
		// A response is ordered behind an inbound barrier the consumer must
		// acknowledge, so model the adapter's own draining loop.
		go func() {
			for {
				select {
				case in := <-p.Client.Inbound():
					if in.Barrier != nil {
						close(in.Barrier)
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
		if err := p.Client.Call(context.Background(), native.Command{Type: native.CommandGetState}, &result); err != nil {
			t.Fatalf("iteration %d: call failed despite a written response: %v", iteration, err)
		}
		if !result.OK || len(result.Blob) != 1<<20 {
			t.Fatalf("iteration %d: response was not fully decoded", iteration)
		}
		closeCtx, cc := context.WithTimeout(context.Background(), 3*time.Second)
		_ = p.Close(closeCtx)
		cc()
	}
}

// An explicitly empty allowlist must reach the child as an empty environment,
// not collapse to nil and inherit the parent's variables.
func TestProcessEmptyEnvironmentStaysEmpty(t *testing.T) {
	t.Setenv("PI_ENV_PROBE", "ambient-value")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p, err := Start(ctx, envlessHelperConfig())
	if err != nil {
		t.Fatalf("empty environment did not stay empty: %v", err)
	}
	closeCtx, cc := context.WithTimeout(context.Background(), 3*time.Second)
	defer cc()
	if err := p.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

func TestRedactBearerCredential(t *testing.T) {
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

func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{limit: 3}
	n, err := b.Write([]byte("abcdef"))
	if err != nil || n != 6 || b.String() != "abc [truncated]" {
		t.Fatalf("%d %v %q", n, err, b.String())
	}
}
