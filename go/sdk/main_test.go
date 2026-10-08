package sdk

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if scenario := os.Getenv(envFakeHost); scenario != "" {
		runFakeHost(scenario)
		return
	}
	os.Exit(m.Run())
}

func newTestClient(t *testing.T, scenario string, knobs ...string) *Client {
	t.Helper()
	client, err := newTestClientWithOptions(t, &Options{
		BinaryPath: os.Args[0],
		Args:       nil,
		Env:        fakeHostEnv(scenario, knobs...),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func newTestClientWithOptions(t *testing.T, opts *Options) (*Client, error) {
	t.Helper()

	if opts.Args == nil {
		opts.Args = []string{}
	}
	if opts.HandshakeTimeout == 0 {
		opts.HandshakeTimeout = 5 * time.Second
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 5 * time.Second
	}

	t.Setenv(EnvBinaryPath, "")
	t.Setenv(EnvBinaryURL, "")

	client, err := New(context.Background(), opts)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return client, nil
}

func osArgsZero() string { return os.Args[0] }

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitForGoroutines(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("goroutines did not settle: have %d, baseline %d\n%s", runtime.NumGoroutine(), baseline, buf)
}
