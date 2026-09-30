package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var oapSyntheticID = regexp.MustCompile(`^oap-(error|request)-(\d+)$`)

func TestHubAddrRefusesAnOriginHeaderBeforeItLooksAtTheHostOrTheMediaType(t *testing.T) {
	address, stop, cancel := startHubAddr(t)
	defer cancel()
	defer stop()

	cases := []struct {
		name   string
		host   string
		origin string
		ctype  string
		body   string
	}{
		{name: "an Origin header and nothing else wrong", host: "127.0.0.1:1", origin: "http://elsewhere.test", ctype: "application/json", body: "{}"},
		{name: "an Origin header beside a Host the hub refuses", host: "evil.test", origin: "http://elsewhere.test", ctype: "application/json", body: "{}"},
		{name: "an Origin header beside a body with a refused media type", host: "127.0.0.1:1", origin: "http://elsewhere.test", ctype: "text/plain", body: "xx"},
		{name: "an Origin header equal to the Host", host: "127.0.0.1:1", origin: "http://127.0.0.1:1", ctype: "application/json", body: "{}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", address, 10*time.Second)
			if err != nil {
				t.Fatalf("dial the hub: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

			head := "POST /adapters/a/sessions HTTP/1.1\r\nHost: " + c.host + "\r\nOrigin: " + c.origin +
				"\r\nContent-Type: " + c.ctype + "\r\nContent-Length: " + strconv.Itoa(len(c.body)) + "\r\n\r\n" + c.body
			if _, err := io.WriteString(conn, head); err != nil {
				t.Fatalf("write the head: %v", err)
			}
			answer, readErr := http.ReadResponse(bufio.NewReader(conn), nil)
			if readErr != nil {
				t.Fatalf("read the answer: %v", readErr)
			}
			body, readErr := io.ReadAll(answer.Body)
			_ = answer.Body.Close()
			if readErr != nil {
				t.Fatalf("read the whole answer: %v", readErr)
			}
			if answer.StatusCode != http.StatusForbidden {
				t.Fatalf("a request carrying %s answered %d, want 403", c.name, answer.StatusCode)
			}
			if answer.ContentLength <= 0 || int64(len(body)) != answer.ContentLength {
				t.Fatalf("the answer was %d bytes and Content-Length said %d", len(body), answer.ContentLength)
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
			if envelope.Type != "error.response" || envelope.Payload.Error.Code != "cross_origin_request" {
				t.Fatalf("a request carrying %s was refused %s / %s, want 403 error.response / cross_origin_request", c.name, envelope.Type, envelope.Payload.Error.Code)
			}
			if envelope.Protocol != "open-agent-protocol" || envelope.Version != "0.1" || envelope.Profile != "open-agent-protocol.agent-control-core" {
				t.Fatalf("the answer was %s %s %s, want open-agent-protocol 0.1 open-agent-protocol.agent-control-core", envelope.Protocol, envelope.Version, envelope.Profile)
			}
			got := oapSyntheticID.FindStringSubmatch(envelope.ID)
			want := oapSyntheticID.FindStringSubmatch(envelope.InReply)
			if got == nil || want == nil || got[1] != "error" || want[1] != "request" || got[2] != want[2] {
				t.Fatalf("the answer was correlated as %q replying to %q, want oap-error-N replying to oap-request-N for the same N", envelope.ID, envelope.InReply)
			}
			t.Logf("%s: refused 403 cross_origin_request, correlated as %s replying to %s, %d-byte envelope", c.name, envelope.ID, envelope.InReply, len(body))
		})
	}
}

func TestHubAddrRefusesAHostOnlyWhenThereIsNoOriginHeaderToRefuseFirst(t *testing.T) {
	address, stop, cancel := startHubAddr(t)
	defer cancel()
	defer stop()
	for _, c := range []struct{ name, host, ctype, body string }{
		{name: "a refused Host with a media type the hub accepts", host: "evil.test", ctype: "application/json", body: "{}"},
		{name: "a refused Host beside a body with a refused media type", host: "evil.test", ctype: "text/plain", body: "xx"},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", address, 10*time.Second)
			if err != nil {
				t.Fatalf("dial the hub: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
			head := fmt.Sprintf("POST /adapters/a/sessions HTTP/1.1\r\nHost: %s\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n%s", c.host, c.ctype, len(c.body), c.body)
			if _, err := io.WriteString(conn, head); err != nil {
				t.Fatalf("write the head: %v", err)
			}
			answer, readErr := http.ReadResponse(bufio.NewReader(conn), nil)
			if readErr != nil {
				t.Fatalf("read the answer: %v", readErr)
			}
			body, readErr := io.ReadAll(answer.Body)
			_ = answer.Body.Close()
			if readErr != nil {
				t.Fatalf("read the whole answer: %v", readErr)
			}
			if answer.StatusCode != http.StatusForbidden {
				t.Fatalf("%s answered %d, want 403", c.name, answer.StatusCode)
			}
			if !strings.Contains(string(body), `"unrecognized_host"`) {
				t.Fatalf("%s was refused with %s, want unrecognized_host", c.name, body)
			}
			t.Logf("%s: refused 403 unrecognized_host, %d-byte envelope", c.name, len(body))
		})
	}
}
