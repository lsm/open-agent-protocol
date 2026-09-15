package servestdio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
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

// TestRequestBudgetMatchesHTTP pins the inbound budget at its boundary on
// both transports: a request of exactly the budget is accepted (and refused
// on its own merits), one byte past it is refused for size. The refusal
// class differs by design — HTTP answers one request with request_too_large,
// the framing fails the frontend closed — but the boundary both enforce is
// the same 16 MiB, so a host can size requests without knowing which
// transport will carry them.
func TestRequestBudgetMatchesHTTP(t *testing.T) {
	budget := DefaultFrameLimit // the frame limit's documented anchor: servehttp's request-body budget

	// HTTP: the submit route reads its body through the daemon's byte cap.
	server, err := servehttp.New(newTestHub(t, 64, 64), servehttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	httpFrontend := httptest.NewServer(server.Handler())
	defer httpFrontend.Close()
	postCode := func(size int) string {
		t.Helper()
		response, err := http.Post(httpFrontend.URL+"/sessions/nope/submit", "application/json", strings.NewReader(strings.Repeat("a", size)))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		var envelope struct {
			Payload struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatalf("HTTP response is not an error envelope: %s", body)
		}
		return envelope.Payload.Error.Code
	}
	if code := postCode(budget); code != "malformed_json" {
		t.Fatalf("at-budget HTTP request refused as %s, want malformed_json — the budget must admit it", code)
	}
	if code := postCode(budget + 1); code != "request_too_large" {
		t.Fatalf("over-budget HTTP request refused as %s, want request_too_large", code)
	}

	// stdio: the same boundary as the default frame limit, one line per
	// request. The at-budget line is a well-formed frame the op layer
	// answers on its own merits; one byte past it is a framing defect the
	// frontend fails closed on.
	prefix, suffix := `{"id":1,"op":"bogus","request":"`, `"}`
	f := startFrontend(t, newTestHub(t, 64, 64), Options{})
	f.send(prefix + strings.Repeat("b", budget-len(prefix)-len(suffix)) + suffix)
	requireCode(t, f.expectResponse(1), "unknown_op")
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}

	over := startFrontend(t, newTestHub(t, 64, 64), Options{})
	over.send(prefix + strings.Repeat("b", budget+1-len(prefix)-len(suffix)) + suffix)
	select {
	case err := <-over.done:
		var defect *MalformedLineError
		if !errors.As(err, &defect) || !strings.Contains(defect.Detail, "frame limit") {
			t.Fatalf("over-budget line ended serving with %v, want a frame-limit defect", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("frontend did not stop after the oversized line")
	}
}
