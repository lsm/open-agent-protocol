package servestdio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
	"github.com/lsm/open-agent-protocol/go/serve/servehttp"
)

type failingProbeAdapter struct{}

func (failingProbeAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{}, errors.New(strings.Repeat("x", 400))
}

func (failingProbeAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return nil, errors.New("unreachable: every probe fails before any open")
}

func TestListingsMatchHTTP(t *testing.T) {
	httpHub, stdioHub := newTestHub(t, 64, 64), newTestHub(t, 64, 64)
	for _, hub := range []*serve.Hub{httpHub, stdioHub} {
		if err := hub.Registry().Register("broken", failingProbeAdapter{}); err != nil {
			t.Fatal(err)
		}
		openSession(t, hub, "list-a")
		openSession(t, hub, "list-b")
	}
	server, err := servehttp.New(httpHub, servehttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	httpFrontend := httptest.NewServer(server.Handler())
	defer httpFrontend.Close()
	f := startFrontend(t, stdioHub, Options{})

	get := func(path string) string {
		t.Helper()
		response, err := http.Get(httpFrontend.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %s: %s", path, response.Status, body)
		}
		return string(body)
	}
	stdio := func(id int64, line string) string {
		t.Helper()
		f.send(line)
		return string(f.expectResponse(id).Result)
	}

	if httpBody, result := get("/adapters"), stdio(1, `{"id":1,"op":"adapters"}`); httpBody != result {
		t.Fatalf("adapters listing drifted:\n http %s\nstdio %s", httpBody, result)
	}
	if httpBody, result := get("/sessions"), stdio(2, `{"id":2,"op":"sessions"}`); normalizedCreatedAt(t, httpBody) != normalizedCreatedAt(t, result) {
		t.Fatalf("sessions listing drifted:\n http %s\nstdio %s", httpBody, result)
	}

	closeResponse, err := http.Post(httpFrontend.URL+"/sessions/list-b/close", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	closeBody, _ := io.ReadAll(closeResponse.Body)
	closeResponse.Body.Close()
	if closeResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("HTTP close: %s: %s", closeResponse.Status, closeBody)
	}
	f.send(`{"id":3,"op":"close","session_id":"list-b"}`)
	requireOK(t, f.expectResponse(3))
	if httpBody, result := get("/sessions"), stdio(4, `{"id":4,"op":"sessions"}`); normalizedCreatedAt(t, httpBody) != normalizedCreatedAt(t, result) {
		t.Fatalf("sessions listing drifted after close:\n http %s\nstdio %s", httpBody, result)
	}

	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

var createdAtField = regexp.MustCompile(`"created_at":"[^"]+"`)

func normalizedCreatedAt(t *testing.T, body string) string {
	t.Helper()
	if !createdAtField.MatchString(body) {
		t.Fatalf("listing carries no creation time: %s", body)
	}
	return createdAtField.ReplaceAllString(body, `"created_at":"<normalized>"`)
}

func TestToolsOpMatchesHTTP(t *testing.T) {
	httpHub, stdioHub := newTestHub(t, 64, 64), newTestHub(t, 64, 64)
	for _, hub := range []*serve.Hub{httpHub, stdioHub} {
		openSession(t, hub, "parity-tools")
	}
	server, err := servehttp.New(httpHub, servehttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	httpFrontend := httptest.NewServer(server.Handler())
	defer httpFrontend.Close()
	f := startFrontend(t, stdioHub, Options{})

	httpResponse, err := http.Get(httpFrontend.URL + "/sessions/parity-tools/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer httpResponse.Body.Close()
	httpBody, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if httpResponse.StatusCode != http.StatusOK {
		t.Fatalf("GET tools: %s: %s", httpResponse.Status, httpBody)
	}
	var overHTTP struct {
		Type     string          `json:"type"`
		Revision string          `json:"capability_revision"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(httpBody, &overHTTP); err != nil {
		t.Fatal(err)
	}

	f.send(`{"id":1,"op":"tools","session_id":"parity-tools"}`)
	response := f.expectResponse(1)
	requireOK(t, response)
	var overStdio struct {
		Type     string          `json:"type"`
		Revision string          `json:"capability_revision"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(response.Result, &overStdio); err != nil {
		t.Fatal(err)
	}
	if overStdio.Type != overHTTP.Type {
		t.Fatalf("stdio answered %s, HTTP answered %s", overStdio.Type, overHTTP.Type)
	}

	if overStdio.Revision == "" || overStdio.Revision != overHTTP.Revision {
		t.Fatalf("stdio revision %q, HTTP revision %q", overStdio.Revision, overHTTP.Revision)
	}
	if !bytes.Equal(overStdio.Payload, overHTTP.Payload) {
		t.Fatalf("catalog payloads differ\nstdio: %s\nhttp:  %s", overStdio.Payload, overHTTP.Payload)
	}

	unknown, err := http.Get(httpFrontend.URL + "/sessions/nobody/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer unknown.Body.Close()
	unknownBody, err := io.ReadAll(unknown.Body)
	if err != nil {
		t.Fatal(err)
	}
	var httpFailure struct {
		Payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(unknownBody, &httpFailure); err != nil {
		t.Fatal(err)
	}
	f.send(`{"id":2,"op":"tools","session_id":"nobody"}`)
	requireCode(t, f.expectResponse(2), httpFailure.Payload.Error.Code)
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func TestOpenOpMatchesHTTP(t *testing.T) {
	httpHub, stdioHub := newTestHub(t, 64, 64), newTestHub(t, 64, 64)

	for _, hub := range []*serve.Hub{httpHub, stdioHub} {
		if err := hub.Registry().RegisterToolSource("workspace-files", protocol.ToolSourceAttachment{
			Kind: protocol.ToolSourceProcess, Protocol: protocol.ToolSourceMCP,
			DisplayName: "Workspace Files", Endpoint: "stdio:workspace-files",
			Command: "/usr/local/bin/mcp-filesystem", Args: []string{"--root", "/workspace"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	server, err := servehttp.New(httpHub, servehttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	httpFrontend := httptest.NewServer(server.Handler())
	defer httpFrontend.Close()
	f := startFrontend(t, stdioHub, Options{})

	payload := func(sessionID string, sources []protocol.ToolSourceAttachment) protocol.SessionOpenRequest {
		return protocol.SessionOpenRequest{SessionID: protocol.SessionID(sessionID), ToolSources: sources}
	}
	postOpen := func(t *testing.T, request json.RawMessage) (int, json.RawMessage) {
		t.Helper()
		response, err := http.Post(httpFrontend.URL+"/adapters/memory/sessions", "application/json", bytes.NewReader(request))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body
	}

	t.Run("a plain open publishes the same state", func(t *testing.T) {
		request := requestEnvelope(t, "req-parity-open", protocol.TypeSessionOpenRequest, payload("parity-open", nil), "", "")
		status, body := postOpen(t, request)
		if status != http.StatusOK {
			t.Fatalf("POST sessions: %d: %s", status, body)
		}
		f.send(fmt.Sprintf(`{"id":1,"op":"open","adapter":"memory","request":%s}`, request))
		response := f.expectResponse(1)
		requireOK(t, response)
		requireEqualPayloads(t, response.Result, body)
	})

	t.Run("a wire-supplied command is refused on both", func(t *testing.T) {
		sources := []protocol.ToolSourceAttachment{{
			ID: "workspace-files", Kind: protocol.ToolSourceProcess, Command: "/bin/sh", Args: []string{"-c", "true"},
		}}
		request := requestEnvelope(t, "req-parity-command", protocol.TypeSessionOpenRequest, payload("parity-command", sources), "", "")
		status, body := postOpen(t, request)
		if status == http.StatusOK {
			t.Fatalf("HTTP accepted a wire-supplied command: %s", body)
		}
		f.send(fmt.Sprintf(`{"id":2,"op":"open","adapter":"memory","request":%s}`, request))
		requireCode(t, f.expectResponse(2), errorCode(t, body))
	})

	t.Run("a configured source named by id is admitted on both", func(t *testing.T) {
		sources := []protocol.ToolSourceAttachment{{ID: "workspace-files", Kind: protocol.ToolSourceProcess}}
		request := requestEnvelope(t, "req-parity-source", protocol.TypeSessionOpenRequest, payload("parity-source", sources), "", "")
		status, body := postOpen(t, request)
		if status != http.StatusOK {
			t.Fatalf("POST sessions with a configured source: %d: %s", status, body)
		}
		f.send(fmt.Sprintf(`{"id":3,"op":"open","adapter":"memory","request":%s}`, request))
		response := f.expectResponse(3)
		requireOK(t, response)
		requireEqualPayloads(t, response.Result, body)
	})

	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func requireEqualPayloads(t *testing.T, overStdio, overHTTP json.RawMessage) {
	t.Helper()
	var stdioEnvelope, httpEnvelope struct {
		Type     string          `json:"type"`
		Session  string          `json:"session_id"`
		Reply    string          `json:"in_reply_to"`
		Revision string          `json:"capability_revision"`
		Payload  json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(overStdio, &stdioEnvelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(overHTTP, &httpEnvelope); err != nil {
		t.Fatal(err)
	}
	if stdioEnvelope.Type != httpEnvelope.Type || stdioEnvelope.Session != httpEnvelope.Session ||
		stdioEnvelope.Reply != httpEnvelope.Reply || stdioEnvelope.Revision != httpEnvelope.Revision {
		t.Fatalf("envelope headers differ\nstdio: %+v\nhttp:  %+v", stdioEnvelope, httpEnvelope)
	}
	if !bytes.Equal(stdioEnvelope.Payload, httpEnvelope.Payload) {
		t.Fatalf("open payloads differ\nstdio: %s\nhttp:  %s", stdioEnvelope.Payload, httpEnvelope.Payload)
	}
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var failure struct {
		Payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Payload.Error.Code == "" {
		t.Fatalf("HTTP body carries no error code: %s", body)
	}
	return failure.Payload.Error.Code
}

func TestRequestBudgetMatchesHTTP(t *testing.T) {
	cancelEnvelope := func(pad int) []byte {
		t.Helper()
		envelope, err := protocol.NewEnvelope(protocol.TypeRunCancelRequest, protocol.EnvelopeID("budget"), protocol.RunCancelRequest{
			SessionID: "budget", RunID: "run-9", Reason: strings.Repeat("r", pad),
		})
		if err != nil {
			t.Fatal(err)
		}

		envelope.SessionID = "budget"
		envelope.RunID = "run-9"
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	sizedEnvelope := func(size int) []byte {
		t.Helper()

		pad := size - len(cancelEnvelope(1)) + 1
		if pad < 1 {
			t.Fatalf("envelope skeleton already exceeds %d bytes", size)
			return nil
		}
		data := cancelEnvelope(pad)
		if len(data) != size {
			t.Fatalf("padded envelope is %d bytes, want %d", len(data), size)
		}
		return data
	}

	server, err := servehttp.New(newTestHub(t, 64, 64), servehttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	httpFrontend := httptest.NewServer(server.Handler())
	defer httpFrontend.Close()
	postCode := func(body []byte, sessionID string) string {
		t.Helper()
		response, err := http.Post(httpFrontend.URL+"/sessions/"+sessionID+"/submit", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		var envelope struct {
			Payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("HTTP response is not an error envelope: %s", data)
		}
		return envelope.Payload.Error.Code
	}

	f := startFrontend(t, newTestHub(t, 64, 64), Options{})
	nextBudgetID := int64(1)
	stdioCode := func(id int64, envelope []byte, sessionID string) string {
		t.Helper()
		f.send(`{"id":` + fmt.Sprint(id) + `,"op":"submit","session_id":"` + sessionID + `","request":` + string(envelope) + `}`)
		return f.expectResponse(id).Error.Code
	}

	for _, row := range []struct {
		name     string
		size     int
		httpCode string
		code     string
	}{
		{"at the budget", maxEnvelopeBytes, "type_mismatch", "type_mismatch"},
		{"one byte over", maxEnvelopeBytes + 1, "request_too_large", "request_too_large"},
	} {
		envelope := sizedEnvelope(row.size)
		if code := postCode(envelope, "nope"); code != row.httpCode {
			t.Fatalf("%s: HTTP refused as %s, want %s", row.name, code, row.httpCode)
		}
		if code := stdioCode(nextBudgetID, envelope, "nope"); code != row.code {
			t.Fatalf("%s: stdio refused as %s, want %s", row.name, code, row.code)
		}
		nextBudgetID++
	}

	longAddress := strings.Repeat("a", 512<<10)
	envelope := sizedEnvelope(maxEnvelopeBytes)
	if code := postCode(envelope, longAddress); code != "type_mismatch" {
		t.Fatalf("long addressing: HTTP refused as %s, want type_mismatch", code)
	}
	if code := stdioCode(nextBudgetID, envelope, longAddress); code != "type_mismatch" {
		t.Fatalf("long addressing: stdio refused as %s, want type_mismatch", code)
	}
	nextBudgetID++

	nulAddress := strings.Repeat("%00", 200<<10)
	nulLineID := strings.Repeat("\\u0000", 200<<10)
	if code := postCode(envelope, nulAddress); code != "type_mismatch" {
		t.Fatalf("encoded addressing: HTTP refused as %s, want type_mismatch", code)
	}
	if code := stdioCode(nextBudgetID, envelope, nulLineID); code != "type_mismatch" {
		t.Fatalf("encoded addressing: stdio refused as %s, want type_mismatch", code)
	}
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}

	prefix, suffix := `{"id":1,"op":"bogus","request":"`, `"}`
	atLimit := startFrontend(t, newTestHub(t, 64, 64), Options{})
	atLimit.send(prefix + strings.Repeat("b", DefaultFrameLimit-len(prefix)-len(suffix)) + suffix)
	requireCode(t, atLimit.expectResponse(1), "unknown_op")
	if err := atLimit.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	over := startFrontend(t, newTestHub(t, 64, 64), Options{})
	over.send(prefix + strings.Repeat("b", DefaultFrameLimit+1-len(prefix)-len(suffix)) + suffix)
	select {
	case err := <-over.done:
		var defect *MalformedLineError
		if !errors.As(err, &defect) || !strings.Contains(defect.Detail, "frame limit") {
			t.Fatalf("over-limit line ended serving with %v, want a frame-limit defect", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("frontend did not stop after the oversized line")
	}
}
