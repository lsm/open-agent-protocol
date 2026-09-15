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

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/serve/servehttp"
)

// failingProbeAdapter registers as an adapter but fails every probe with an
// over-long diagnostic, so the listings' error branch — and the message
// trimming both transports apply to it — is part of the pinned parity rather
// than an untested copy.
type failingProbeAdapter struct{}

func (failingProbeAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{}, errors.New(strings.Repeat("x", 400))
}

func (failingProbeAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return nil, errors.New("unreachable: every probe fails before any open")
}

// The cross-transport parity checks: the op mirror claims servehttp's bodies
// verbatim, and the inbound budget claims the same 16 MiB both transports
// accept. These tests pin both claims behaviorally — the listings byte for
// byte over equal hubs, and the budget at its exact boundary — so the parity
// is checked, not hoped for, as the two transports evolve.

// TestListingsMatchHTTP serves equal hubs over HTTP and stdio and requires
// the adapters and sessions documents to be byte-equal: the listings are
// transport-neutral presentations of the same hub state — a healthy and a
// probe-failing adapter, open and closed sessions — so any drift between the
// mirrored presentation helpers, error branch and message trimming included,
// fails here.
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

	// A closed session stays listed on both transports; the listing remains
	// equal after the state change each surface drove itself.
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

// createdAtField matches the hub-stamped creation times inside one sessions
// listing. The two hubs stamp their own wall clocks microseconds apart while
// RFC3339 carries second resolution, so the values are normalized before the
// byte comparison — every other byte of the two documents must still match,
// and the match itself requires the field to be present and non-empty on
// both sides.
var createdAtField = regexp.MustCompile(`"created_at":"[^"]+"`)

func normalizedCreatedAt(t *testing.T, body string) string {
	t.Helper()
	if !createdAtField.MatchString(body) {
		t.Fatalf("listing carries no creation time: %s", body)
	}
	return createdAtField.ReplaceAllString(body, `"created_at":"<normalized>"`)
}

// TestRequestBudgetMatchesHTTP carries one schema-valid run-cancel envelope
// — padded to a chosen size — across both transports: the envelope, not the
// line, is the budgeted unit (servehttp reads it as the body limit, the op
// gate budgets the request param), so a wrapper's bytes cannot make one
// transport accept what the other refuses. At the budget both transports
// accept the envelope and refuse it on identical merits (the submit route
// answers type_mismatch); one byte over, both refuse it for size.
func TestRequestBudgetMatchesHTTP(t *testing.T) {
	cancelEnvelope := func(pad int) []byte {
		t.Helper()
		envelope, err := protocol.NewEnvelope(protocol.TypeRunCancelRequest, protocol.EnvelopeID("budget"), protocol.RunCancelRequest{
			SessionID: "budget", RunID: "run-9", Reason: strings.Repeat("r", pad),
		})
		if err != nil {
			t.Fatal(err)
		}
		// The envelope schema's run def requires the addressing on the
		// envelope itself, not only in the payload.
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
		// The reason field is omitempty, so the empty skeleton omits it
		// entirely; measuring the one-rune form makes the padding linear.
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

	// HTTP: the submit route budgets the body, then judges the envelope.
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

	// stdio: the same envelope rides the request param of a submit line —
	// the wrapper's bytes land inside the frame limit's allowance, not the
	// envelope budget.
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

	// The wrapper allowance covers every address the HTTP surface can
	// accept — Go's server admits request lines only within its 1 MiB
	// header limit — so a near-limit envelope addressed by a large id rides
	// both transports to the same op-level verdict instead of
	// framing-defecting on one.
	longAddress := strings.Repeat("a", 512<<10)
	envelope := sizedEnvelope(maxEnvelopeBytes)
	if code := postCode(envelope, longAddress); code != "type_mismatch" {
		t.Fatalf("long addressing: HTTP refused as %s, want type_mismatch", code)
	}
	if code := stdioCode(nextBudgetID, envelope, longAddress); code != "type_mismatch" {
		t.Fatalf("long addressing: stdio refused as %s, want type_mismatch", code)
	}
	nextBudgetID++

	// The encodings expand the same raw address differently — a NUL is
	// three bytes as %00 in the HTTP target and six as a JSON \u escape
	// of the same byte in the wrapper — so the allowance is sized for the
	// expansion ratio, not the raw byte count: the control-character id
	// that worst-cases the ratio rides both transports too.
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

	// The frame limit itself still bounds the line: a line of exactly the
	// limit frames (and is answered on its merits), one byte past it is the
	// fail-closed defect — the wrapper allowance is headroom, not a second
	// budget.
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
