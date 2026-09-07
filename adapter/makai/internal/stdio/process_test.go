package stdio

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_MAKAI_HELPER") != "1" {
		return
	}
	mode := os.Getenv("MAKAI_HELPER_MODE")
	switch mode {
	case "ready":
		os.Stdout.WriteString(`{"type":"ready","protocol_version":"1"}` + "\n")
		_, _ = os.Stdin.Read(make([]byte, 1))
		os.Exit(0)
	case "bad":
		os.Stderr.WriteString("api_key=secret-value\n" + strings.Repeat("x", 256))
		os.Stdout.WriteString("noise\n")
		os.Exit(2)
	case "hang":
		select {}
	}
}
func helperConfig(mode string) ProcessConfig {
	return ProcessConfig{Path: os.Args[0], Args: []string{"-test.run=TestProcessHelper", "--"}, Env: append(os.Environ(), "GO_WANT_MAKAI_HELPER=1", "MAKAI_HELPER_MODE="+mode), ShutdownTimeout: 5 * time.Second, StderrLimit: 64}
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
func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{limit: 3}
	n, err := b.Write([]byte("abcdef"))
	if err != nil || n != 6 || b.String() != "abc [truncated]" {
		t.Fatalf("n=%d err=%v value=%q", n, err, b.String())
	}
}
