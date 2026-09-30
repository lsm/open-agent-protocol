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
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	if os.Getenv("OAP_FAKE_HUB_AS_CHILD") == "1" {
		signal.Ignore(os.Interrupt)
	}
}

func TestHubAddrFakeHubIgnoresSigint(t *testing.T) {
	if os.Getenv("OAP_FAKE_HUB_AS_CHILD") != "1" {
		t.Skip("this is the child half of the fake daemon, and only the parent sets that variable")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, _ = fmt.Fprintln(os.Stdout, "listening on "+listener.Addr().String())
	select {}
}

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

	head := fmt.Sprintf("POST /adapters HTTP/1.1\r\nHost: evil.test\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n", declared)
	if _, err := io.WriteString(conn, head); err != nil {
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
		block := make([]byte, 32*1024)
		for i := range block {
			block[i] = 'c'
		}
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
		_ = conn.Close()
		t.Fatalf("read the answer after %d bytes: %v", written.Load(), err)
	}
	body, err := io.ReadAll(answer.Body)
	_ = answer.Body.Close()
	if err != nil {
		_ = conn.Close()
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
	writerRunning := func() bool {
		select {
		case <-writeDone:
			return false
		default:
			return true
		}
	}
	if !writerRunning() {
		t.Fatalf("the writer had already finished when the refusal was read, so the body arrived before the answer")
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

	daemon.stop()
	select {
	case <-writeDone:
	case <-time.After(30 * time.Second):
		_ = conn.Close()
		t.Fatalf("the writer was still going 30s after the daemon exited")
	}
	final := written.Load()
	if final >= int64(declared) {
		t.Fatalf("all %d declared bytes were transferred, so nothing was left in flight to cancel", declared)
	}
	t.Logf("the refusal was read whole while %d of the %d declared body bytes were still unwritten, and the writer was still running at that moment; after the signal the writer reached %d and stopped, leaving %d never written; %d status, %d body bytes. These are client write counts: they bound when the answer arrived and when the writer stopped. They are not a count of bytes the daemon read, and a count above or below the drain cap would not be one either, because what the client pushes before the answer lands is kernel buffering and scheduling.",
		beforeSignal, declared, final, int64(declared)-final, answer.StatusCode, len(body))

	awaitDaemonGone(t, address, 15*time.Second)
	if !daemon.signalAndAwaitExit(15 * time.Second) {
		t.Fatalf("the daemon was still alive 15s after SIGINT; cleanup will kill it, so this run does not observe an exit caused by the signal")
	}
}

// TestHubAddrSignalProofIsNotSatisfiedByTheTestsOwnKill starts a child that
// ignores SIGINT and keeps its listener open. The after-signal exit proof must
// miss its bound and the test must fail, even though the cleanup path still
// kills and reaps the child. Without this, a daemon that ignores the signal
// would pass, because stop() escalates to Kill and awaitExit would see the
// exit the *test* caused.
func TestHubAddrSignalProofIsNotSatisfiedByTheTestsOwnKill(t *testing.T) {
	if os.Getenv("OAP_FAKE_HUB_IGNORE_SIGINT") == "" {
		t.Skip("run with OAP_FAKE_HUB_IGNORE_SIGINT=1 to exercise the fake daemon that ignores SIGINT")
	}
	command := exec.Command(os.Args[0], "-test.run=TestHubAddrFakeHubIgnoresSigint")
	command.Env = append(os.Environ(), "OAP_FAKE_HUB_AS_CHILD=1")
	command.Env = append(command.Env, "OAP_FAKE_HUB_IGNORE_SIGINT=")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(exited)
	}()
	defer func() {
		_ = command.Process.Kill()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	}()

	address := ""
	got := make(chan string, 1)
	go func() {
		reader := bufio.NewReaderSize(stdout, 64*1024)
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			got <- ""
			return
		}
		got <- strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "listening on "))
	}()
	select {
	case address = <-got:
	case <-time.After(20 * time.Second):
		t.Fatal("the fake daemon never reported an address")
	}
	if address == "" {
		t.Fatal("the fake daemon reported a blank address")
	}

	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatalf("the fake daemon is not accepting: %v", err)
	}
	_ = conn.Close()

	survived := false
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		survived = true
	}
	if !survived {
		t.Skip("this platform terminates on SIGINT by default, so it cannot show the distinction here")
	}
	t.Logf("the fake daemon ignored SIGINT and was still alive 3s later, so the after-signal proof must miss its bound for it; the cleanup kill above still reaps it")
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
	command := exec.CommandContext(ctx, oapx, "hub", "--addr=127.0.0.1:0")
	command.Stdin = strings.NewReader("")
	stderr := &lockedBuffer{}
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
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
	lines := make(chan string, 1)
	go func() {
		reader := bufio.NewReaderSize(stdout, 64*1024)
		line, err := reader.ReadString('\n')
		if err == nil {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) == 0 {
				lines <- ""
				return
			}
			address := strings.TrimPrefix(fields[len(fields)-1], "http://")
			address = strings.TrimPrefix(address, "https://")
			lines <- address
			return
		}
		lines <- ""
	}()
	select {
	case address := <-lines:
		if address != "" {
			return address
		}
		cancel()
		t.Fatalf("oapx hub --addr reported no address:\n%s", hub.shutDownForDiagnostics(t))
		return ""
	case <-time.After(30 * time.Second):
		cancel()
		t.Fatalf("oapx hub --addr never reported a bound address within 30s:\n%s", hub.shutDownForDiagnostics(t))
		return ""
	}
}

func (hub *ownedHub) stop() {
	if hub.stopped {
		return
	}
	hub.stopped = true
	_ = hub.command.Process.Signal(os.Interrupt)
	select {
	case <-hub.exited:
	case <-time.After(10 * time.Second):
		_ = hub.command.Process.Kill()
		select {
		case <-hub.exited:
		case <-time.After(10 * time.Second):
		}
	}
	hub.cancelCtx()
}

// signalAndAwaitExit is the *proof*, and it is deliberately not stop(): it sends
// SIGINT and waits, with no fallback kill, so a daemon that ignores the signal
// misses the bound instead of being killed by the test and then counted as
// having exited on its own. Cleanup is the caller's business, and the deferred
// stop() still reaps whatever this returns false for.
func (hub *ownedHub) signalAndAwaitExit(within time.Duration) bool {
	_ = hub.command.Process.Signal(os.Interrupt)
	select {
	case <-hub.exited:
		return true
	case <-time.After(within):
		return false
	}
}

func (hub *ownedHub) awaitExit(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-hub.exited:
	case <-time.After(within):
		t.Fatalf("the daemon child was not reaped within %v of the signal, so its exit was not observed", within)
	}
}
