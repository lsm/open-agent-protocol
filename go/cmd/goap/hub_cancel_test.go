package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
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

	var written int64
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		block := make([]byte, 32*1024)
		for i := range block {
			block[i] = 'c'
		}
		for written < declared {
			take := len(block)
			if left := int64(declared) - written; left < int64(take) {
				take = int(left)
			}
			n, werr := conn.Write(block[0:take])
			written += int64(n)
			if werr != nil {
				return
			}
		}
	}()

	answer, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		stop()
		_ = conn.Close()
		t.Fatalf("read the answer after %d bytes: %v", written, err)
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
	beforeSignal := written

	stop()
	select {
	case <-writeDone:
	case <-time.After(30 * time.Second):
		_ = conn.Close()
		t.Fatalf("the writer was still going 30s after the daemon exited")
	}
	t.Logf("refusal read whole after %d bytes; %d in total after the daemon stopped; %d status, %d body bytes",
		beforeSignal, written, answer.StatusCode, len(body))
}
