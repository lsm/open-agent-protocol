package stdio

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/makai/internal/native"
)

func TestProcessHelper(t *testing.T) {

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

		blob := strings.Repeat("x", 1<<20)
		os.Stdout.WriteString(`{"type":"pong","session_id":"` + request.SessionID + `","message_id":"01ARZ3NDEKTSV4RRFFQ69G5FAW","sequence":1,"timestamp":1,"version":1,"in_reply_to":"` + request.MessageID + `","payload":{"ping_id":"` + blob + `"}}` + "\n")
		os.Exit(0)
	case "emptyenv":

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

		os.Stdout.WriteString(`{"type":"ready","protocol_version":"1"}` + "\n")
		descendant := exec.Command(os.Args[0], "-test.run=TestProcessHelper", "--")
		descendant.Env = append(os.Environ(), "MAKAI_HELPER_MODE=holder")
		descendant.Stdout = os.Stdout
		if err := descendant.Start(); err != nil {
			os.Exit(20)
		}
		os.Exit(0)
	case "bad-descendant":

		os.Stderr.WriteString("api_key=secret-value\n" + strings.Repeat("x", 256))
		descendant := exec.Command(os.Args[0], "-test.run=TestProcessHelper", "--")
		descendant.Env = append(os.Environ(), "MAKAI_HELPER_MODE=holder")
		descendant.Stderr = os.Stderr
		if err := descendant.Start(); err != nil {
			os.Exit(22)
		}
		os.Stdout.WriteString("noise\n")
		os.Exit(2)
	case "ready-stderr-descendant":

		os.Stdout.WriteString(`{"type":"ready","protocol_version":"1"}` + "\n")
		descendant := exec.Command(os.Args[0], "-test.run=TestProcessHelper", "--")
		descendant.Env = append(os.Environ(), "MAKAI_HELPER_MODE=holder")
		descendant.Stdout = os.Stdout
		descendant.Stderr = os.Stderr
		if err := descendant.Start(); err != nil {
			os.Exit(21)
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

func envlessHelperConfig() ProcessConfig {
	return ProcessConfig{Path: os.Args[0], Args: []string{"-test.run=TestProcessHelper", "--", "--makai-emptyenv"}, Env: []string{}, ShutdownTimeout: 2 * time.Second, StderrLimit: 64}
}

func TestProcessCallSurvivesChildExitImmediatelyAfterResponse(t *testing.T) {
	for iteration := range 10 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		p, err := Start(ctx, helperConfig("large"))
		cancel()
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

type lateReader struct {
	io.ReadCloser
	gate chan struct{}
	once sync.Once
}

func (r *lateReader) Read(p []byte) (int, error) {
	r.once.Do(func() { <-r.gate })
	return r.ReadCloser.Read(p)
}

func TestProcessLaggingStderrCopierStillReachesBuffer(t *testing.T) {
	gate := make(chan struct{})
	config := helperConfig("bad")
	config.stderrTap = func(pipe io.ReadCloser) io.ReadCloser {
		return &lateReader{ReadCloser: pipe, gate: gate}
	}
	time.AfterFunc(150*time.Millisecond, func() { close(gate) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Start(ctx, config)
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("secret leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("stderr lost to a lagging copier: %v", err)
	}
}

func TestProcessShutdownReleasesDescendantHeldStderr(t *testing.T) {
	const timeout = time.Second
	config := helperConfig("ready-stderr-descendant")
	config.ShutdownTimeout = timeout
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p, err := Start(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() { _ = p.Close(context.Background()); done <- time.Since(start) }()
	select {
	case elapsed := <-done:

		if elapsed > timeout+500*time.Millisecond {
			t.Fatalf("Close took %v, want roughly one %v budget", elapsed, timeout)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Close blocked while a descendant held stderr open")
	}
}

func TestProcessHandshakeAbortBoundsDescendantHeldStderr(t *testing.T) {
	config := helperConfig("bad-descendant")
	config.ShutdownTimeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := Start(ctx, config)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Start took %v; the abort drain waited on a descendant-held stderr", elapsed)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("secret leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("stderr lost to the bounded abort drain: %v", err)
	}
}

func TestProcessHandshakeAbortDrainsBeforeReleasing(t *testing.T) {
	gate := make(chan struct{})
	config := helperConfig("bad")
	config.stderrTap = func(pipe io.ReadCloser) io.ReadCloser {
		return &lateReader{ReadCloser: pipe, gate: gate}
	}
	time.AfterFunc(abortStderrGrace/4, func() { close(gate) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Start(ctx, config)
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") || !strings.Contains(err.Error(), "[truncated]") {
		t.Fatalf("stderr released before it was drained: %v", err)
	}
}
