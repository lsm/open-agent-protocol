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
	"strconv"
	"strings"
	"testing"
	"time"
)

func startHubAddr(t *testing.T) (string, func(), context.CancelFunc) {
	t.Helper()
	oapx := os.Getenv("OAP_OAPX_BIN")
	if oapx == "" {
		t.Skip("set OAP_OAPX_BIN to an oapx binary to drive its HTTP daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	command := exec.CommandContext(ctx, oapx, "serve", "--addr=127.0.0.1:0")
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
	stopped := false
	cleanup := func() {
		if !stopped {
			stopped = true
			_ = command.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Log("oapx serve --addr did not report an exit after Kill")
				}
			}
		}
	}
	addressBound := make(chan string, 1)
	go func() {
		select {
		case address := <-bound:
			addressBound <- address
		case <-time.After(60 * time.Second):
			addressBound <- ""
		}
	}()
	address := <-addressBound
	if address == "" {
		cleanup()
		t.Fatalf("oapx serve --addr never reported a bound address:\n%s", stderr.String())
		return "", func() {}, cancel
	}
	return address, cleanup, cancel
}

func TestHubAddrRefusesALargeRefusedHeadOverARealSocket(t *testing.T) {
	address, cleanup, cancel := startHubAddr(t)
	defer cancel()
	defer cleanup()

	seedHubClock(t, address)
	boundAt := time.Now()
	for _, declared := range []int{1<<20 + 4096, 4 << 20, 16 << 20} {
		t.Run(strconv.Itoa(declared), func(t *testing.T) {
			if wait := 2500*time.Millisecond - time.Since(boundAt); wait > 0 {
				time.Sleep(wait)
			}
			if up := time.Since(boundAt); up <= 2500*time.Millisecond {
				t.Fatalf("the daemon was up %v, under its own drain budget", up)
			}
			var conn net.Conn
			conn, err := net.DialTimeout("tcp", address, 10*time.Second)
			if err != nil {
				t.Fatalf("dial the hub: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(90 * time.Second))

			head := fmt.Sprintf("POST /adapters HTTP/1.1\r\nHost: evil.test\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n", declared)
			if _, err := io.WriteString(conn, head); err != nil {
				t.Fatalf("write the head: %v", err)
			}

			_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			written, err := writeBody(conn, declared)
			if err != nil && written < minTransferred {
				t.Fatalf("wrote only %d of %d bytes before the write deadline: %v", written, declared, err)
			}
			_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

			answer, readErr := http.ReadResponse(bufio.NewReader(conn), nil)
			if readErr != nil {
				_ = conn.Close()
				t.Fatalf("read the answer after writing %d of %d bytes: %v", written, declared, readErr)
			}
			body, err := io.ReadAll(answer.Body)
			_ = answer.Body.Close()
			_ = conn.Close()
			if err != nil {
				t.Fatalf("read the whole refusal after writing %d of %d bytes: %v", written, declared, err)
			}
			if answer.StatusCode != http.StatusForbidden {
				t.Fatalf("a refused head after %d transferred bytes answered %d, want 403", written, answer.StatusCode)
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
			if envelope.Type != "error.response" {
				t.Fatalf("the refusal was %q, want an error.response envelope", envelope.Type)
			}
			if envelope.Payload.Error.Code != "unrecognized_host" {
				t.Fatalf("the refusal code was %q, want unrecognized_host", envelope.Payload.Error.Code)
			}
			if written < minTransferred {
				t.Fatalf("only %d bytes were transferred, under the %d drain budget, so this case does not exercise it", written, minTransferred)
			}
			t.Logf("declared %d, wrote %d, answered %d with a %d-byte body", declared, written, answer.StatusCode, len(body))
		})
	}
}

const minTransferred = 1 << 20

func seedHubClock(t *testing.T, address string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		t.Fatalf("dial the hub to seed its clock: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	head := "GET /adapters HTTP/1.1\r\nHost: evil.test\r\nContent-Length: 0\r\n\r\n"
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatalf("write the seeding request: %v", err)
	}
	answer, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("the seeding request was not answered: %v", err)
	}
	_, _ = io.Copy(io.Discard, answer.Body)
	_ = answer.Body.Close()
	if answer.StatusCode != http.StatusForbidden {
		t.Fatalf("the seeding request answered %d, want 403, so it never reached a drain", answer.StatusCode)
	}
}

func writeBody(conn net.Conn, declared int) (int, error) {
	block := bytes.Repeat([]byte("b"), 32*1024)
	written := 0
	for written < declared {
		take := len(block)
		if left := declared - written; left < take {
			take = left
		}
		n, err := conn.Write(block[0:take])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
