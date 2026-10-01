package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestHubAddrAnswersACompleteTransportFailureWhenTheBodyStopsShort(t *testing.T) {
	const declared = 8 << 20
	const partial = 4096
	address, stop, cancel := startHubAddr(t)
	defer cancel()
	defer stop()

	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		t.Fatalf("dial the hub: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	head := "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: " +
		strconv.Itoa(declared) + "\r\n\r\n"
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatalf("write the head: %v", err)
	}
	block := make([]byte, partial)
	for i := range block {
		block[i] = 'c'
	}
	sent, werr := conn.Write(block)
	if werr != nil {
		t.Fatalf("write the partial body: %v", werr)
	}
	if sent != partial {
		t.Fatalf("the client sent %d of the %d body bytes it meant to send", sent, partial)
	}
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatalf("half close so the daemon sees the peer stop: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	answer, readErr := http.ReadResponse(bufio.NewReader(conn), nil)
	if readErr != nil {
		t.Fatalf("no answer arrived after %d of %d declared body bytes, so the daemon did not give up on a peer that stopped sending: %v", sent, declared, readErr)
	}
	body, readErr := io.ReadAll(answer.Body)
	_ = answer.Body.Close()
	if readErr != nil {
		t.Fatalf("read the whole answer: %v", readErr)
	}
	if answer.StatusCode != http.StatusBadRequest {
		t.Fatalf("a body that stopped short answered %d, want 400", answer.StatusCode)
	}
	if answer.ContentLength <= 0 || int64(len(body)) != answer.ContentLength {
		t.Fatalf("the answer was %d bytes and Content-Length said %d", len(body), answer.ContentLength)
	}
	if sent >= declared {
		t.Fatalf("the client sent all %d declared bytes, so this run cannot show the daemon answered before the body arrived", declared)
	}
	var envelope struct {
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
		Version  string `json:"version"`
		Profile  string `json:"profile"`
		ID       string `json:"id"`
		InReply  string `json:"in_reply_to"`
		Payload  struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("the answer is not parseable JSON (%v): %s", err, body)
	}
	if envelope.Type != "error.response" || envelope.Payload.Error.Code != "request_read" {
		t.Fatalf("the answer was %s / %s, want error.response / request_read", envelope.Type, envelope.Payload.Error.Code)
	}
	if envelope.InReply != "oap-request-1" || envelope.ID != "oap-error-1" {
		t.Fatalf("the answer was correlated as %q replying to %q, want oap-error-1 replying to oap-request-1", envelope.ID, envelope.InReply)
	}
	if envelope.Protocol != "open-agent-protocol" || envelope.Version != "0.1" || envelope.Profile != "open-agent-protocol.agent-control-core" {
		t.Fatalf("the answer was %s %s %s, want open-agent-protocol 0.1 open-agent-protocol.agent-control-core", envelope.Protocol, envelope.Version, envelope.Profile)
	}
	t.Logf("declared %d, sent %d then stopped, and the daemon answered a complete %d-byte %s / %s from %s %s, correlated as %s replying to %s, without waiting for the rest", declared, sent, len(body), envelope.Type, envelope.Payload.Error.Code, envelope.Protocol, envelope.Version, envelope.ID, envelope.InReply)
}
