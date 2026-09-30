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
	address, stop := startHubAddr(t)

	const declared = 8 << 20
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		stop()
		t.Fatalf("dial the hub: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	head := fmt.Sprintf("POST /adapters HTTP/1.1\r\nHost: %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n", address, declared)
	if _, err := io.WriteString(conn, head); err != nil {
		stop()
		t.Fatalf("write the head: %v", err)
	}

	var written atomic.Int64
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
			t.Log("the writer did not finish within 30s")
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
	if answer.StatusCode != http.StatusUnsupportedMediaType {
		stop()
		t.Fatalf("a wrong Content-Type answered %d, want 415", answer.StatusCode)
	}
	if !strings.Contains(string(body), "unsupported_media_type") {
		stop()
		t.Fatalf("the refusal did not carry its code: %s", body)
	}
	beforeSignal := written.Load()
	if beforeSignal == 0 {
		stop()
		t.Fatalf("the refusal was read before any body byte moved, so nothing was in flight")
	}
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
	if envelope.Type != "error.response" || envelope.Payload.Error.Code != "unsupported_media_type" {
		stop()
		t.Fatalf("the refusal was %s / %s, want error.response / unsupported_media_type", envelope.Type, envelope.Payload.Error.Code)
	}

	stop()
	select {
	case <-writeDone:
	case <-time.After(30 * time.Second):
		_ = conn.Close()
		t.Fatalf("the writer was still going 30s after the daemon exited")
	}
	if final := written.Load(); final >= int64(declared) {
		t.Logf("the whole declared body was transferred before the signal")
	}
	t.Logf("refusal read whole after %d bytes; %d in total after the daemon stopped; %d status, %d body bytes",
		beforeSignal, written.Load(), answer.StatusCode, len(body))
}
