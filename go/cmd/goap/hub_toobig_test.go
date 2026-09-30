package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestHubAddrAnswersAComplete413BeforeTheWholeDeclaredBodyIsSent(t *testing.T) {
	const maxTestBodyBytes = 16 << 20
	for _, declared := range []int{maxTestBodyBytes + 1, 1 << 34} {
		t.Run(strconv.Itoa(declared), func(t *testing.T) {
			address, stop, cancel := startHubAddr(t)
			defer cancel()
			defer stop()

			conn, err := net.DialTimeout("tcp", address, 10*time.Second)
			if err != nil {
				t.Fatalf("dial the hub: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

			head := fmt.Sprintf("POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", declared)
			if _, err := io.WriteString(conn, head); err != nil {
				t.Fatalf("write the head: %v", err)
			}

			var written atomic.Int64
			var wroteAll atomic.Bool
			done := make(chan struct{})
			go func() {
				defer close(done)
				block := make([]byte, 32*1024)
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
				wroteAll.Store(true)
			}()

			_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
			answer, readErr := http.ReadResponse(bufio.NewReader(conn), nil)
			if readErr != nil {
				_ = conn.Close()
				<-done
				t.Fatalf("read the refusal after %d bytes: %v", written.Load(), readErr)
			}
			body, readErr := io.ReadAll(answer.Body)
			_ = answer.Body.Close()
			if readErr != nil {
				_ = conn.Close()
				<-done
				t.Fatalf("read the whole refusal after %d bytes: %v", written.Load(), readErr)
			}
			if answer.StatusCode != http.StatusRequestEntityTooLarge {
				_ = conn.Close()
				<-done
				t.Fatalf("a %d byte declaration answered %d, want 413", declared, answer.StatusCode)
			}
			if answer.ContentLength <= 0 || int64(len(body)) != answer.ContentLength {
				_ = conn.Close()
				<-done
				t.Fatalf("the refusal body was %d bytes and Content-Length said %d", len(body), answer.ContentLength)
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
				_ = conn.Close()
				<-done
				t.Fatalf("the refusal is not parseable JSON (%v): %s", err, body)
			}
			if envelope.Type != "error.response" || envelope.Payload.Error.Code != "request_too_large" {
				_ = conn.Close()
				<-done
				t.Fatalf("the refusal was %s / %s, want error.response / request_too_large", envelope.Type, envelope.Payload.Error.Code)
			}

			select {
			case <-done:
			case <-time.After(20 * time.Second):
			}
			cutShort := !wroteAll.Load()
			stop()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				_ = conn.Close()
				t.Fatalf("the writer was still going 30s after the daemon was signalled")
			}
			if !cutShort {
				t.Fatalf("the client was still able to send all %d declared bytes, so this run observed no early stop and cannot show the declaration went unread", declared)
			}
			sent := written.Load()
			if sent <= 0 {
				t.Fatalf("the client transferred no body byte, so nothing was in flight and the early stop was not observed")
			}
			t.Logf("declared %d, refused 413 with a complete %d-byte envelope, and the client had sent %d of the declared bytes and was still writing when it stopped; every number here is a client write count, so this pins the refusal is complete and complete before the declared body was sent, and pins nothing about how many bytes the daemon read", declared, len(body), sent)
		})
	}
}
