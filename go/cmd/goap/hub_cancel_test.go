package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHubAddrFinishesTheRefusalAndStopsTheBodyWhenItsSignalArrives(t *testing.T) {
	daemon := startOwnedHub(t)
	defer daemon.cancelCtx()
	defer daemon.stop()
	address := daemon.address
	const declared = 8 << 20
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		t.Fatalf("dial the hub: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	if _, err := fmt.Fprintf(conn, "POST /adapters HTTP/1.1\r\nHost: evil.test\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n", declared); err != nil {
		t.Fatalf("write the head: %v", err)
	}

	var written atomic.Int64
	var blocks atomic.Int64
	writeDone := make(chan struct{})
	joined := false
	join := func() {
		if joined {
			return
		}
		joined = true
		_ = conn.Close()
		select {
		case <-writeDone:
		case <-time.After(30 * time.Second):
			t.Errorf("the writer was still going 30s after the connection closed, so the test did not clean up")
		}
	}
	defer join()
	go func() {
		defer close(writeDone)
		block := bytes.Repeat([]byte{'c'}, 32*1024)
		for written.Load() < int64(declared) {
			take := len(block)
			if left := int64(declared) - written.Load(); left < int64(take) {
				take = int(left)
			}
			n, werr := conn.Write(block[0:take])
			written.Add(int64(n))
			if werr != nil {
				return
			}
			blocks.Add(1)
			if blocks.Load()%8 == 0 {
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()

	answer, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read the answer after %d bytes: %v", written.Load(), err)
	}
	body, err := io.ReadAll(answer.Body)
	_ = answer.Body.Close()
	if err != nil {
		t.Fatalf("read the whole refusal: %v", err)
	}
	if answer.StatusCode != http.StatusForbidden {
		t.Fatalf("a refused head answered %d, want 403", answer.StatusCode)
	}
	if !strings.Contains(string(body), "unrecognized_host") {
		t.Fatalf("the refusal did not carry its code: %s", body)
	}
	beforeSignal := written.Load()
	if beforeSignal == 0 {
		t.Fatalf("the refusal was read before any body byte moved, so nothing was in flight")
	}
	if beforeSignal >= int64(declared) {
		t.Fatalf("the refusal was read only after all %d declared bytes had been written, so the daemon answered after the body rather than before it", declared)
	}
	select {
	case <-writeDone:
		t.Fatalf("the writer had already finished when the refusal was read, so the body arrived before the answer")
	default:
	}
	if answer.ContentLength <= 0 || int64(len(body)) != answer.ContentLength {
		t.Fatalf("the body was %d bytes and Content-Length said %d", len(body), answer.ContentLength)
	}
	var envelope struct {
		Type    string `json:"type"`
		Payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("the refusal body is not parseable JSON (%v): %s", err, body)
	}
	if envelope.Type != "error.response" || envelope.Payload.Error.Code != "unrecognized_host" {
		t.Fatalf("the refusal was %s / %s, want error.response / unrecognized_host", envelope.Type,
			envelope.Payload.Error.Code)
	}

	exited := daemon.signalAndAwaitExit(15 * time.Second)
	select {
	case <-writeDone:
	case <-time.After(30 * time.Second):
		_ = conn.Close()
		daemon.stop()
		t.Fatalf("the writer was still going 30s after the daemon exited")
	}
	final := written.Load()
	if final >= int64(declared) {
		t.Fatalf("all %d declared bytes were transferred, so nothing was left in flight to cancel", declared)
	}
	t.Logf("the refusal was read whole while %d of the %d declared body bytes were still unwritten, and the writer was still running at that moment; after the signal the writer reached %d and stopped, leaving %d never written; %d status, %d body bytes. These are client write counts: they bound when the answer arrived and when the writer stopped. They are not a count of bytes the daemon read, and a count above or below the drain cap would not be one either, because what the client pushes before the answer lands is kernel buffering and scheduling.",
		beforeSignal, declared, final, int64(declared)-final, answer.StatusCode, len(body))

	if !exited {
		daemon.stop()
		daemon.awaitExit(t, 15*time.Second)
		t.Fatalf("the daemon was still alive 15s after SIGINT and was reaped by the cleanup kill, so this run does not observe an exit caused by the signal")
	}
	awaitDaemonGone(t, address, 15*time.Second)
}

const aliveProofBound = 3 * time.Second

func TestHubAddrSignalProofReportsADaemonThatIgnoresTheSignalAsAlive(t *testing.T) {
	goTool := os.Getenv("OAP_GO")
	if goTool == "" {
		goTool = "go"
	}
	resolved, err := exec.LookPath(goTool)
	if err != nil {
		t.Fatalf("no go toolchain on PATH: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "fakehub")
	build := exec.Command(resolved, "build", "-o", binary, "./go/cmd/goap/testdata/fakehub")
	build.Dir = repositoryRoot()
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build the fake daemon: %v: %s", buildErr, output)
	}
	command := exec.Command(binary)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr lockedBuffer
	command.Stderr = &stderr
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			_ = command.Process.Kill()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Error("cleanup killed the fake daemon but never reaped it")
			}
		})
	}
	defer cleanup()
	identity := strings.TrimPrefix(readLineWithin(stdout, 60*time.Second), "ready ")
	fields := strings.Fields(identity)
	if len(fields) != 2 {
		t.Fatalf("the fake daemon announced %q, want an address and a pid", identity)
	}
	address := fields[0]
	accepting := func(when string) {
		t.Helper()
		conn, dialErr := net.DialTimeout("tcp", address, 5*time.Second)
		if dialErr != nil {
			t.Fatalf("the fake daemon %s on %s: %v; it said %s", when, address, dialErr, stderr.String())
		}
		_ = conn.Close()
	}
	accepting("is not accepting")
	proof := &ownedHub{command: command, exited: done, cancelCtx: func() {}}
	waited := time.Now()
	if proof.signalAndAwaitExit(aliveProofBound) {
		t.Fatal("the proof reported an exit for a daemon that ignores SIGINT, so it cannot tell the two apart")
	}
	observed := time.Since(waited)
	if observed < aliveProofBound-time.Second {
		t.Fatalf("the proof returned after %v, not the full %v bound, so it did not wait out the signal", observed, aliveProofBound)
	}
	accepting("no longer accepts though the proof reported it alive")
	t.Logf("the fake daemon ignored SIGINT, the proof reported it alive after waiting out the full %v bound, and it was still accepting on %s afterwards", observed, address)
	cleanup()
}

func awaitDaemonGone(t *testing.T, address string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if err != nil {
			return
		}
		_ = conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the daemon was still accepting connections on %s after %v, so its listener had not closed", address, within)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

type ownedHub struct {
	address   string
	command   *exec.Cmd
	cancelCtx context.CancelFunc
	exited    chan struct{}
	stopped   bool
	stderr    *lockedBuffer
}

func startOwnedHub(t *testing.T) *ownedHub {
	t.Helper()
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to drive its HTTP daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	command := exec.CommandContext(ctx, oapx, "hub", "--addr=127.0.0.1:0", "--session-history=")
	command.Stdin = strings.NewReader("")
	stderr := &lockedBuffer{}
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err == nil {
		err = command.Start()
	}
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	hub := &ownedHub{command: command, cancelCtx: cancel, exited: make(chan struct{}), stderr: stderr}
	go func() {
		defer close(hub.exited)
		_ = command.Wait()
	}()
	hub.address = awaitBoundAddress(t, stdout, hub, cancel)
	return hub
}

type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

func (hub *ownedHub) shutDownForDiagnostics(t *testing.T) string {
	t.Helper()
	hub.stop()
	hub.awaitExit(t, 15*time.Second)
	return hub.stderr.String()
}

func awaitBoundAddress(t *testing.T, stdout io.Reader, hub *ownedHub, cancel context.CancelFunc) string {
	t.Helper()
	identity := readLineWithin(stdout, 30*time.Second)
	fields := strings.Fields(identity)
	if len(fields) == 0 {
		cancel()
		t.Fatalf("oapx hub --addr reported no address:\n%s", hub.shutDownForDiagnostics(t))
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(fields[len(fields)-1], "http://"), "https://")
}

func readLineWithin(reader io.Reader, within time.Duration) string {
	lines := make(chan string, 1)
	go func() {
		line, err := bufio.NewReaderSize(reader, 64*1024).ReadString('\n')
		if err != nil {
			line = ""
		}
		lines <- strings.TrimSpace(line)
	}()
	select {
	case line := <-lines:
		return line
	case <-time.After(within):
		return ""
	}
}

func (hub *ownedHub) stop() {
	if hub.stopped {
		return
	}
	hub.stopped = true
	_ = hub.command.Process.Signal(os.Interrupt)
	if !hub.awaitExitWithin(10 * time.Second) {
		_ = hub.command.Process.Kill()
		hub.awaitExitWithin(10 * time.Second)
	}
	hub.cancelCtx()
}

func (hub *ownedHub) awaitExitWithin(within time.Duration) bool {
	select {
	case <-hub.exited:
		return true
	case <-time.After(within):
		return false
	}
}

func (hub *ownedHub) signalAndAwaitExit(within time.Duration) bool {
	_ = hub.command.Process.Signal(os.Interrupt)
	return hub.awaitExitWithin(within)
}

func (hub *ownedHub) awaitExit(t *testing.T, within time.Duration) {
	t.Helper()
	if !hub.awaitExitWithin(within) {
		t.Fatalf("the daemon child was not reaped within %v of the signal, so its exit was not observed", within)
	}
}
