package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var mediaSyntheticID = regexp.MustCompile(`^oap-(error|request)-(\d+)$`)

func TestHubAddrRefusesABodyWhoseMediaTypeIsNotJSONAndNothingElseIsWrong(t *testing.T) {
	address, stop, cancel := startHubAddr(t)
	defer cancel()
	defer stop()

	cases := []struct {
		name   string
		ctype  string
		body   string
		refuse bool
	}{
		{name: "text/plain with a body", ctype: "text/plain", body: "xx", refuse: true},
		{name: "application/json with a body", ctype: "application/json", body: "{}", refuse: false},
		{name: "application/json with a charset parameter", ctype: "application/json; charset=utf-8", body: "{}", refuse: false},
		{name: "text/plain with no body declared", ctype: "text/plain", body: "", refuse: false},
		{name: "application/json with a parameter that has no equals", ctype: "application/json; charset", body: "{}", refuse: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", address, 10*time.Second)
			if err != nil {
				t.Fatalf("dial the hub: %v", err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

			head := "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:1\r\nContent-Type: " + c.ctype +
				"\r\nContent-Length: " + strconv.Itoa(len(c.body)) + "\r\n\r\n" + c.body
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
			if !c.refuse {
				if answer.StatusCode == http.StatusUnsupportedMediaType {
					t.Fatalf("%s was refused 415, but the draft admits application/json and a body-less request is not gated: %s", c.name, body)
				}
				t.Logf("%s: not media-refused, answered %d, %d-byte body", c.name, answer.StatusCode, len(body))
				return
			}
			if answer.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("%s answered %d, want 415; the answer was %s", c.name, answer.StatusCode, body)
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
			if envelope.Type != "error.response" || envelope.Payload.Error.Code != "unsupported_media_type" {
				t.Fatalf("%s was refused %s / %s, want 415 error.response / unsupported_media_type", c.name, envelope.Type, envelope.Payload.Error.Code)
			}
			if envelope.Protocol != "open-agent-protocol" || envelope.Version != "0.1" || envelope.Profile != "open-agent-protocol.agent-control-core" {
				t.Fatalf("the answer was %s %s %s, want open-agent-protocol 0.1 open-agent-protocol.agent-control-core", envelope.Protocol, envelope.Version, envelope.Profile)
			}
			got := mediaSyntheticID.FindStringSubmatch(envelope.ID)
			want := mediaSyntheticID.FindStringSubmatch(envelope.InReply)
			if got == nil || want == nil || got[1] != "error" || want[1] != "request" || got[2] != want[2] {
				t.Fatalf("the answer was correlated as %q replying to %q, want oap-error-N replying to oap-request-N for the same N", envelope.ID, envelope.InReply)
			}
			if !strings.Contains(string(body), "text/plain") {
				t.Logf("the answer does not echo the media type, which is expected: %s", body)
			}
			t.Logf("%s: refused 415 unsupported_media_type, correlated as %s replying to %s, %d-byte envelope", c.name, envelope.ID, envelope.InReply, len(body))
		})
	}
}
