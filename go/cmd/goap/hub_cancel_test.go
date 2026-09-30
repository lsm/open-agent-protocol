package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHubAddrFinishesTheRefusalAndStopsTheBodyWhenItsSignalArrives(t *testing.T) {
	address, stop, cancel := startHubAddr(t)
	defer cancel()

	const declared = 8 << 20
	const drainTotalCapBytes = 1 << 20
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		stop()
		t.Fatalf("dial the hub: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	head := fmt.Sprintf("POST /adapters HTTP/1.1\r\nHost: evil.test\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n", declared)
	if _, err := io.WriteString(conn, head); err != nil {
		stop()
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
		stop()
		_ = conn.Close()
		t.Fatalf("read the answer after %d bytes: %v", written.Load(), err)
	}
	body, err := io.ReadAll(answer.Body)
	_ = answer.Body.Close()
	if err != nil {
		stop()
		_ = conn.Close()
		t.Fatalf("read the whole refusal: %v", err)
	}
	if answer.StatusCode != http.StatusForbidden {
		stop()
		t.Fatalf("a refused head answered %d, want 403", answer.StatusCode)
	}
	if !strings.Contains(string(body), "unrecognized_host") {
		stop()
		t.Fatalf("the refusal did not carry its code: %s", body)
	}
	beforeSignal := written.Load()
	if beforeSignal == 0 {
		stop()
		t.Fatalf("the refusal was read before any body byte moved, so nothing was in flight")
	}
	if beforeSignal >= int64(declared) {
		stop()
		t.Fatalf("the refusal was read only after all %d declared bytes had been written, so the daemon answered after the body rather than before it", declared)
	}
	if beforeSignal > drainTotalCapBytes/2 {
		stop()
		t.Fatalf("the writer had pushed %d body bytes when the refusal was read, more than half the %d drain cap, so the daemon had already been reading the body it refused",
			beforeSignal, drainTotalCapBytes)
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
		stop()
		t.Fatalf("the writer had already finished when the refusal was read, so the body arrived before the answer")
	}
	stop()
	_ = writerRunning
	if answer.ContentLength <= 0 || int64(len(body)) != answer.ContentLength {
		stop()
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
		stop()
		t.Fatalf("the refusal body is not parseable JSON (%v): %s", err, body)
	}
	if envelope.Type != "error.response" || envelope.Payload.Error.Code != "unrecognized_host" {
		stop()
		t.Fatalf("the refusal was %s / %s, want error.response / unrecognized_host", envelope.Type,
			envelope.Payload.Error.Code)
	}

	stop()
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
	t.Logf("the refusal was read whole while %d of the %d declared body bytes were still unwritten; after the signal the writer reached %d and stopped, leaving %d never written; %d status, %d body bytes. These are client write counts: they bound when the answer arrived and when the writer stopped, not how many bytes the daemon read.",
		beforeSignal, declared, final, int64(declared)-final, answer.StatusCode, len(body))
}
