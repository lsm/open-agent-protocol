package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const portGoldensPath = "testdata/port-goldens.json"

type portGoldens struct {
	CapabilityRevision string                        `json:"capability_revision"`
	Capabilities       protocol.CapabilityDescriptor `json:"capabilities"`
	Endpoint           string                        `json:"endpoint"`
	Requests           []capturedRequest             `json:"requests"`
}

type capturedRequest struct {
	Name    string            `json:"name"`
	Method  string            `json:"method"`
	Target  string            `json:"target"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body,omitempty"`
}

type requestRecorder struct {
	mu       sync.Mutex
	name     string
	captured []capturedRequest
}

func (r *requestRecorder) expect(name string) {
	r.mu.Lock()
	r.name = name
	r.mu.Unlock()
}

func (r *requestRecorder) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	payload, _ := io.ReadAll(request.Body)
	headers := map[string]string{}
	for _, name := range []string{"Accept", "Authorization", "Content-Type"} {
		if value := request.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	record := capturedRequest{Method: request.Method, Target: request.URL.RequestURI(), Headers: headers}
	if len(payload) > 0 {
		body := string(payload)
		record.Body = &body
	}
	r.mu.Lock()
	record.Name = r.name
	r.captured = append(r.captured, record)
	r.mu.Unlock()

	path := request.URL.Path
	switch {
	case path == "/base/api/session" && request.Method == http.MethodPost:
		_, _ = w.Write([]byte(`{"data":{"id":"ses_fake00000000000000","projectID":"prj_fake","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1,"updated":1},"title":"","location":{"directory":"/w"}}}`))
	case path == "/base/api/session/active":
		_, _ = w.Write([]byte(`{"data":{}}`))
	case bytes.HasSuffix([]byte(path), []byte("/prompt")):
		var sent native.PromptRequest
		_ = json.Unmarshal(payload, &sent)
		promoted := int64(1)
		admitted, _ := json.Marshal(map[string]any{"data": native.Admitted{AdmittedSeq: 1, ID: sent.ID, SessionID: "ses_fake00000000000000", Prompt: sent.Prompt, Delivery: sent.Delivery, TimeCreated: 1, PromotedSeq: &promoted}})
		_, _ = w.Write(admitted)
	case bytes.HasSuffix([]byte(path), []byte("/interrupt")):
		w.WriteHeader(http.StatusNoContent)
	case bytes.HasSuffix([]byte(path), []byte("/history")):
		_, _ = w.Write([]byte(`{"data":[],"hasMore":false}`))
	case bytes.HasSuffix([]byte(path), []byte("/event")):
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPortGoldensAreWhatTheGoAdapterSendsAndAdvertises(t *testing.T) {
	recorder := &requestRecorder{}
	server := httptest.NewServer(recorder)
	defer server.Close()
	ctx := context.Background()
	session := native.SessionID("ses_fake00000000000000")

	authed, err := httpapi.New(server.URL+"/base", httpapi.Options{Username: "opencode", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer authed.Close()
	bare, err := httpapi.New(server.URL+"/base", httpapi.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()

	steps := []struct {
		name string
		call func() error
	}{
		{"create-session", func() error {
			_, err := authed.CreateSession(ctx, httpapi.CreateSessionRequest{Agent: "build", Model: &native.ModelRef{ID: "fixture", ProviderID: "fixture", Variant: "high"}})
			return err
		}},
		{"create-session-bare", func() error {
			_, err := bare.CreateSession(ctx, httpapi.CreateSessionRequest{})
			return err
		}},
		{"prompt", func() error {
			_, err := authed.Prompt(ctx, session, native.PromptRequest{ID: "msg_fake0000000000000004", Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
			return err
		}},
		{"prompt-escaped-queue", func() error {
			_, err := authed.Prompt(ctx, session, native.PromptRequest{ID: "msg_fake0000000000000009", Prompt: native.Prompt{Text: "a<b>&c \"q\" \\   \n\t\r\b\f\x01\x1f\x7f é"}, Delivery: native.DeliveryQueue})
			return err
		}},
		{"interrupt", func() error { return authed.Interrupt(ctx, session) }},
		{"active", func() error {
			_, err := authed.Active(ctx)
			return err
		}},
		{"history", func() error {
			_, err := authed.History(ctx, session, 3, 100)
			return err
		}},
		{"subscribe", func() error {
			subscription, err := authed.Subscribe(ctx, session, -1)
			if err != nil {
				return err
			}
			<-subscription.Done()
			return subscription.Close()
		}},
		{"subscribe-after", func() error {
			subscription, err := authed.Subscribe(ctx, session, 5)
			if err != nil {
				return err
			}
			<-subscription.Done()
			return subscription.Close()
		}},
	}
	for _, step := range steps {
		recorder.expect(step.name)
		if err := step.call(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
	}

	adapter, err := New(Config{Endpoint: server.URL + "/base"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := adapter.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	captured := append([]capturedRequest(nil), recorder.captured...)
	recorder.mu.Unlock()
	if len(captured) != len(steps) {
		t.Fatalf("captured %d requests for %d calls", len(captured), len(steps))
	}
	got, err := json.MarshalIndent(portGoldens{CapabilityRevision: descriptor.CapabilityRevision, Capabilities: descriptor.Capabilities, Endpoint: "/base", Requests: captured}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.FromSlash(portGoldensPath)
	if os.Getenv("OAP_UPDATE_OPENCODE_PORT_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; set OAP_UPDATE_OPENCODE_PORT_GOLDENS=1 to record", err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("%s drifted from the Go adapter\nwant: %s\ngot:  %s", portGoldensPath, want, got)
	}
}
