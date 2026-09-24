package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve/servehttp"
)

func TestUsageMentionsHubAndServe(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), nil, nil, &stdout, &stderr); err == nil {
		t.Fatal("no command succeeded")
	}
	for _, verb := range []string{"hub", "serve"} {
		if !strings.Contains(stderr.String(), verb) {
			t.Fatalf("usage lacks %s: %s", verb, stderr.String())
		}
	}
}

func TestServeWithoutRoleNamesHub(t *testing.T) {
	for _, args := range [][]string{{"serve"}, {"serve", "--addr", "127.0.0.1:0"}, {"serve", "--stdio"}} {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err == nil {
			t.Fatalf("%v succeeded", args)
		}
		if !strings.Contains(stderr.String(), "goap hub") || !strings.Contains(stderr.String(), "serve agent") {
			t.Fatalf("%v usage does not name hub and serve agent: %s", args, stderr.String())
		}
	}
}

func TestServeProviderRolesAnswerUnavailable(t *testing.T) {
	for _, role := range []string{"provider", "agent,provider"} {
		var stdout, stderr bytes.Buffer
		err := run(context.Background(), []string{"serve", role, "--stdio"}, strings.NewReader(""), &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "unavailable") || !strings.Contains(err.Error(), "serve "+role) {
			t.Fatalf("serve %s: %v", role, err)
		}
	}
}

func TestServeAgentFlagErrors(t *testing.T) {
	cases := [][]string{
		{"serve", "agent", "--adapter", "memory"},
		{"serve", "agent", "extra-argument"},
		{"serve", "agent", "--backend", "absent"},
		{"serve", "agent", "--config", filepath.Join(t.TempDir(), "missing.json")},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err == nil {
			t.Fatalf("%v succeeded", args)
		}
	}
}

func TestHubFlagErrors(t *testing.T) {
	cases := [][]string{
		{"hub", "--nope"},
		{"hub", "extra-argument"},
		{"hub", "--config", filepath.Join(t.TempDir(), "missing.json")},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if err := run(context.Background(), args, nil, &stdout, &stderr); err == nil {
			t.Fatalf("hub %v succeeded", args)
		}
	}
}

func TestServeDefaultAddrIsLoopback(t *testing.T) {
	host, _, err := net.SplitHostPort(servehttp.DefaultAddr)
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("default address %q is not loopback", servehttp.DefaultAddr)
	}
}

func writeServeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oap.json")
	document := `{"adapters": {"memory": {"type": "memory"}}}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffer.String()
}

func startServe(t *testing.T, args []string) (string, func(), <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stdout := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runHub(ctx, args, nil, stdout, io.Discard) }()

	address := ""
	deadline := time.Now().Add(5 * time.Second)
	for address == "" {
		for _, line := range strings.Split(stdout.String(), "\n") {
			if after, ok := strings.CutPrefix(strings.TrimSpace(line), "listening on "); ok {
				address = after
			}
		}
		if address == "" {
			if time.Now().After(deadline) {
				t.Fatalf("serve did not start: %q", stdout.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if host, _, err := net.SplitHostPort(strings.TrimPrefix(address, "http://")); err != nil || host != "127.0.0.1" {
		t.Fatalf("serve bound a non-loopback address: %s", address)
	}
	return address, cancel, done
}

func expectServeExit(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve exited with error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}
}

func TestServeLifecycleOverRealListener(t *testing.T) {
	address, cancel, done := startServe(t, []string{"--config", writeServeConfig(t), "--addr", "127.0.0.1:0"})

	response, err := http.Get(address + "/adapters")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(data), "reference.memory") {
		t.Fatalf("adapters listing: %d %s", response.StatusCode, data)
	}

	open, err := protocol.NewEnvelope(protocol.TypeSessionOpenRequest, "serve-open", protocol.SessionOpenRequest{SessionID: "serve-session"})
	if err != nil {
		t.Fatal(err)
	}
	open.SessionID = "serve-session"
	body, err := json.Marshal(open)
	if err != nil {
		t.Fatal(err)
	}
	response, err = http.Post(address+"/adapters/memory/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("open: %d %s", response.StatusCode, data)
	}
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil || envelope.Type != protocol.TypeSessionOpenResponse {
		t.Fatalf("open envelope: %v %s", err, data)
	}

	stream, err := http.Get(address + "/sessions/serve-session/events")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK || stream.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("events: %d %s", stream.StatusCode, stream.Header.Get("Content-Type"))
	}
	ended := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, stream.Body); close(ended) }()

	cancel()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("sse stream survived shutdown")
	}
	expectServeExit(t, done)
}

func TestServeSessionsClosedOnShutdown(t *testing.T) {
	address, cancel, done := startServe(t, []string{"--config", writeServeConfig(t), "--addr", "127.0.0.1:0"})

	envelope, err := protocol.NewEnvelope(protocol.TypeSessionOpenRequest, "serve-close-open", protocol.SessionOpenRequest{SessionID: "serve-close"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(envelope)
	response, err := http.Post(address+"/adapters/memory/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("open: %d %s", response.StatusCode, data)
	}

	state, err := http.Get(address + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(state.Body)
	state.Body.Close()
	if !strings.Contains(string(data), "serve-close") {
		t.Fatalf("session listing lacks opened session: %s", data)
	}

	cancel()
	expectServeExit(t, done)
}

func TestLoopbackHosts(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:6270", "localhost:6270", "[::1]:6270"} {
		if got := loopbackHosts(addr); got == nil {
			t.Fatalf("loopback addr %q did not enable the host allowlist", addr)
		}
	}

	for _, addr := range []string{":6270", "0.0.0.0:6270", "example.org:80", "[::]:6270"} {
		if got := loopbackHosts(addr); got != nil {
			t.Fatalf("non-loopback addr %q kept an allowlist: %v", addr, got)
		}
	}
}
