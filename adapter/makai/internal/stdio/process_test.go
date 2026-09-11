package stdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter/makai/internal/native"
)

func TestProcessHelper(t *testing.T) {
	// Activated either by environment or by an explicit argument, so the
	// empty-allowlist fixture can be spawned with no environment at all.
	envless := strings.Contains(strings.Join(os.Args, "\x00"), "--makai-emptyenv")
	mode := os.Getenv("MAKAI_HELPER_MODE")
	if mode == "" && !envless {
		return
	}
	if envless {
		mode = "emptyenv"
	}
	switch mode {
	case "ready":
		os.Stdout.WriteString(`{"type":"ready","protocol_version":"1"}` + "\n")
		_, _ = os.Stdin.Read(make([]byte, 1))
		os.Exit(0)
	case "large":
		os.Stdout.WriteString(`{"type":"ready","protocol_version":"1"}` + "\n")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			os.Exit(11)
		}
		var request struct {
			SessionID string `json:"session_id"`
			MessageID string `json:"message_id"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(12)
		}
		// A frame far larger than the pipe buffer keeps the reader busy past the
		// child's exit, which is the window this exercises.
		blob := strings.Repeat("x", 1<<20)
		os.Stdout.WriteString(`{"type":"pong","session_id":"` + request.SessionID + `","message_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW","sequence":1,"timestamp":1,"version":1,"in_reply_to":"` + request.MessageID + `","payload":{"ping_id":"` + blob + `"}}` + "\n")
		os.Exit(0)
	case "emptyenv":
		// A parent-inherited probe means the empty allowlist collapsed to nil.
		if os.Getenv("MAKAI_ENV_PROBE") != "" {
			os.Exit(13)
		}
		os.Stdout.WriteString(`{"type":"ready","protocol_version":"1"}` + "\n")
		_, _ = os.Stdin.Read(make([]byte, 1))
		os.Exit(0)
	case "bad":
		os.Stderr.WriteString("api_key=secret-value\n" + strings.Repeat("x", 256))
		os.Stdout.WriteString("noise\n")
		os.Exit(2)
	case "ready-descendant":
		// A descendant that inherits stdout and outlives this process keeps the
		// parent's read end from reaching EOF after the direct child exits.
		os.Stdout.WriteString(`{"type":"ready","protocol_version":"1"}` + "\n")
		descendant := exec.Command(os.Args[0], "-test.run=TestProcessHelper", "--")
		descendant.Env = append(os.Environ(), "MAKAI_HELPER_MODE=holder")
		descendant.Stdout = os.Stdout
		if err := descendant.Start(); err != nil {
			os.Exit(20)
		}
		os.Exit(0)
	case "holder":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "hang":
		select {}
	}
}
func helperConfig(mode string) ProcessConfig {
	return ProcessConfig{Path: os.Args[0], Args: []string{"-test.run=TestProcessHelper", "--"}, Env: append(os.Environ(), "GO_WANT_MAKAI_HELPER=1", "MAKAI_HELPER_MODE="+mode), ShutdownTimeout: 5 * time.Second, StderrLimit: 64}
}

// envlessHelperConfig spawns the helper with an explicitly empty allowlist, so
// the child sees no parent variables at all.
func envlessHelperConfig() ProcessConfig {
	return ProcessConfig{Path: os.Args[0], Args: []string{"-test.run=TestProcessHelper", "--", "--makai-emptyenv"}, Env: []string{}, ShutdownTimeout: 2 * time.Second, StderrLimit: 64}
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
		// A matched response is ordered behind an inbound barrier the consumer
		// must acknowledge, so model the adapter's own draining loop.
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
		request, err := native.NewEnvelope(native.TypePing, "01ARZ3NDEKTSV4RRFFQ69", "01ARZ3NDEKTSV4RRFFQ69G5FAV", 1, 1, native.Empty{})
		if err != nil {
			t.Fatalf("iteration %d: build request: %v", iteration, err)
		}
		reply, err := p.Client.Call(context.Background(), request, native.TypePong)
		if err != nil {
			t.Fatalf("iteration %d: call failed despite a written response: %v", iteration, err)
		}
		var pong native.Pong
		if err := json.Unmarshal(reply.Payload, &pong); err != nil {
			t.Fatalf("iteration %d: decode pong: %v", iteration, err)
		}
		if len(pong.PingID) != 1<<20 {
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
	t.Setenv("MAKAI_ENV_PROBE", "ambient-value")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, err := Start(ctx, envlessHelperConfig())
	if err != nil {
		t.Fatalf("empty environment did not stay empty: %v", err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeCancel()
	if err := p.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}
func TestProcessReadyAndClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p, err := Start(ctx, helperConfig("ready"))
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeCancel()
	if err := p.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Client.Done():
	default:
		t.Fatal("client open")
	}
}
func TestProcessHandshakeFailureRedactsAndBoundsStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := Start(ctx, helperConfig("bad"))
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("secret leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("missing safeguards: %v", err)
	}
}
func TestProcessHandshakeCancellationReaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Start(ctx, helperConfig("hang"))
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("child not reaped promptly")
	}
}

// A descendant holding stdout open keeps the reader from reaching EOF. A forced
// shutdown must release the pipe through the client's closer before waiting, or
// Close blocks forever despite the configured timeout.
func TestProcessShutdownReleasesDescendantHeldPipe(t *testing.T) {
	config := helperConfig("ready-descendant")
	config.ShutdownTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := Start(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.Close(context.Background()) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "shutdown timed out") {
			t.Fatalf("got %v, want shutdown timeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked while a descendant held stdout open")
	}
}

func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{limit: 3}
	n, err := b.Write([]byte("abcdef"))
	if err != nil || n != 6 || b.String() != "abc [truncated]" {
		t.Fatalf("n=%d err=%v value=%q", n, err, b.String())
	}
}
func TestRedactHidesQuotedAndBearerCredentials(t *testing.T) {
	for input, want := range map[string]string{
		"x-api-key=secret":                   "x-api-key=[REDACTED]",
		"Authorization: Bearer secret-value": "Authorization: [REDACTED]",
		"password=secret-value":              "password=[REDACTED]",
		"secret: value":                      "secret: [REDACTED]",
		"token=value":                        "token=[REDACTED]",
		`{"api_key":"secret-value"}`:         `{"api_key":"[REDACTED]"}`,
	} {
		if got := redact(input); got != want {
			t.Fatalf("redact %q = %q want %q", input, got, want)
		}
	}
}
