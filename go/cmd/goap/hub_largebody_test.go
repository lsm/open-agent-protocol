package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func startHubAddr(t *testing.T) (string, func()) {
	t.Helper()
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to drive its HTTP daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	command := exec.CommandContext(ctx, oapx, "hub", "--addr=127.0.0.1:0")
	command.Stdin = strings.NewReader("")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	bound := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "listening on http://") {
				bound <- strings.TrimPrefix(strings.TrimPrefix(scanner.Text(), "listening on "), "http://")
				return
			}
		}
		bound <- ""
	}()
	cleanup := func() {
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
		cancel()
	}
	select {
	case address := <-bound:
		if address == "" {
			cleanup()
			t.Fatalf("oapx hub --addr bound nothing:\n%s", stderr.String())
		}
		return address, cleanup
	case <-ctx.Done():
		cleanup()
		t.Fatalf("oapx hub --addr never reported a bound address:\n%s", stderr.String())
		return "", func() {}
	}
}

// A bounded concurrent writer transfers the whole declared body while the
// daemon reads and answers, so the 415 is produced against a body that was
// genuinely in flight rather than merely named in a header.
func TestHubAddrRefusesALargeMediaTypeOverARealSocket(t *testing.T) {
	address, cleanup := startHubAddr(t)
	defer cleanup()

	for _, declared := range []int{1<<20 + 4096, 4 << 20, 16 << 20} {
		t.Run(strconv.Itoa(declared), func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", address, 10*time.Second)
			if err != nil {
				t.Fatalf("dial the hub: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(90 * time.Second))

			head := fmt.Sprintf("POST /adapters HTTP/1.1\r\nHost: %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n", address, declared)
			if _, err := io.WriteString(conn, head); err != nil {
				t.Fatalf("write the head: %v", err)
			}

			var written int64
			var writeErr error
			var wg sync.WaitGroup
			stop := make(chan struct{})
			wg.Add(1)
			go func() {
				defer wg.Done()
				block := bytes.Repeat([]byte("b"), 32*1024)
				for written < int64(declared) {
					select {
					case <-stop:
						return
					default:
					}
					n, err := conn.Write(block)
					written += int64(n)
					if err != nil {
						writeErr = err
						return
					}
				}
			}()

			// the daemon refuses on the head, so the answer arrives while the
			// writer is still going; the writer is released only after the whole
			// response has been read, which is what leaves bytes pending
			answer, readErr := http.ReadResponse(bufio.NewReader(conn), nil)
			joined := make(chan struct{})
			go func() { wg.Wait(); close(joined) }()
			select {
			case <-joined:
			case <-time.After(30 * time.Second):
				_ = conn.Close()
				t.Fatalf("the bounded writer did not finish within 30s")
			}
			close(stop)
			if readErr != nil {
				t.Fatalf("read the answer after writing %d of %d bytes (write err %v): %v", written, declared, writeErr, readErr)
			}
			defer answer.Body.Close()
			if answer.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("a wrong Content-Type over %d bytes answered %d, want 415", declared, answer.StatusCode)
			}
			if answer.ContentLength <= 0 {
				t.Fatalf("the refusal carried no Content-Length: %v", answer.ContentLength)
			}
			body, err := io.ReadAll(answer.Body)
			if err != nil {
				t.Fatalf("read the framed body: %v", err)
			}
			if int64(len(body)) != answer.ContentLength {
				t.Fatalf("the body was %d bytes and Content-Length said %d", len(body), answer.ContentLength)
			}
			if !bytes.Contains(body, []byte(`"unsupported_media_type"`)) {
				t.Fatalf("the complete refusal did not carry its code:\n%s", body)
			}
			if !bytes.Contains(body, []byte(`"type":"error.response"`)) {
				t.Fatalf("the complete refusal was not an error.response envelope:\n%s", body)
			}
			t.Logf("declared %d, wrote %d, answered %d with a %d-byte body", declared, written, answer.StatusCode, len(body))
		})
	}
}
