package servehttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/validation"
)

const testTimeout = 5 * time.Second

func memoryRegistry(capacity int) *serve.Registry {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{JournalCapacity: capacity})); err != nil {
		panic(err)
	}
	return registry
}

// newServer serves one hub over the real codec on loopback and returns both,
// so tests can drive the HTTP surface and the hub's own sweep.
func newServer(t *testing.T, registry *serve.Registry, options Options) (*serve.Hub, *httptest.Server) {
	t.Helper()
	hub := serve.New(registry, serve.Options{})
	daemon, err := New(hub, options)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(daemon.Handler())
	t.Cleanup(server.Close)
	return hub, server
}

func newMemoryServer(t *testing.T, capacity int) *httptest.Server {
	_, server := newServer(t, memoryRegistry(capacity), Options{})
	return server
}

// --- HTTP helpers ---

func requestEnvelope(t *testing.T, typ protocol.EnvelopeType, id string, payload any, sessionID, runID, revision string) protocol.Envelope {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(id), payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = protocol.SessionID(sessionID)
	envelope.RunID = protocol.RunID(runID)
	envelope.CapabilityRevision = revision
	return envelope
}

func post(t *testing.T, server *httptest.Server, path, contentType string, body []byte) (*http.Response, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}

// postEnvelope posts one request envelope and asserts the response decodes as
// an envelope (204 responses excepted).
func postEnvelope(t *testing.T, server *httptest.Server, path string, envelope protocol.Envelope) (int, protocol.Envelope) {
	t.Helper()
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	response, data := post(t, server, path, "application/json", body)
	if response.StatusCode == http.StatusNoContent {
		return response.StatusCode, protocol.Envelope{}
	}
	parsed, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatalf("parse response envelope (status %d): %v: %s", response.StatusCode, err, data)
	}
	return response.StatusCode, parsed
}

// listSessions reads the session listing into a fresh value each call: a
// reused destination would keep fields that an omitted (omitempty) JSON key
// never overwrites.
func listSessions(t *testing.T, server *httptest.Server) []sessionInfo {
	t.Helper()
	var listing struct {
		Sessions []sessionInfo `json:"sessions"`
	}
	if status := getJSON(t, server, "/sessions", &listing); status != http.StatusOK {
		t.Fatalf("sessions listing status %d", status)
	}
	return listing.Sessions
}

func sessionAt(t *testing.T, sessions []sessionInfo, id string) sessionInfo {
	t.Helper()
	for _, entry := range sessions {
		if entry.SessionID == id {
			return entry
		}
	}
	t.Fatalf("session %q missing from listing", id)
	return sessionInfo{}
}

func getJSON(t *testing.T, server *httptest.Server, path string, dst any) int {
	t.Helper()
	response, err := server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode %s response: %v: %s", path, err, data)
	}
	return response.StatusCode
}

func requireEnvelopeType(t *testing.T, envelope protocol.Envelope, want protocol.EnvelopeType) {
	t.Helper()
	if envelope.Type != want {
		t.Fatalf("envelope type %s, want %s (%s)", envelope.Type, want, envelope.ID)
	}
}

func requireEnvelopeSchema(t *testing.T, envelope protocol.Envelope) {
	t.Helper()
	schema, err := validation.CompileSchemas()
	if err != nil {
		t.Fatal(err)
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(mustMarshal(t, envelope)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("envelope %s failed the OAP schema: %v", envelope.Type, err)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type errorPayload struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func requireErrorResponse(t *testing.T, status, wantStatus int, envelope protocol.Envelope, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status %d, want %d", status, wantStatus)
	}
	requireEnvelopeType(t, envelope, protocol.TypeErrorResponse)
	if envelope.InReplyTo == "" {
		t.Fatal("error response lacks correlation")
	}
	var payload errorPayload
	if err := envelope.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != wantCode {
		t.Fatalf("error code %q, want %q (message %q)", payload.Error.Code, wantCode, payload.Error.Message)
	}
	if payload.Error.Message == "" {
		t.Fatal("error message is empty")
	}
	requireEnvelopeSchema(t, envelope)
}

// --- SSE helpers ---

type sseEvent struct {
	name string
	id   string
	data string
}

type sseStream struct {
	t        *testing.T
	response *http.Response
	events   chan sseEvent
}

func connectSSE(t *testing.T, server *httptest.Server, path, lastEventID string) *sseStream {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("sse status %d: %s", response.StatusCode, data)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		_ = response.Body.Close()
		t.Fatalf("sse content type %q", got)
	}
	stream := &sseStream{t: t, response: response, events: make(chan sseEvent, 128)}
	t.Cleanup(stream.close)
	go stream.scan()
	return stream
}

func (stream *sseStream) scan() {
	defer close(stream.events)
	scanner := bufio.NewScanner(stream.response.Body)
	var current sseEvent
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if current.name != "" || current.id != "" || current.data != "" {
				stream.events <- current
			}
			current = sseEvent{}
		case strings.HasPrefix(line, "id: "):
			current.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			current.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

func (stream *sseStream) close() {
	_ = stream.response.Body.Close()
}

func (stream *sseStream) next() sseEvent {
	stream.t.Helper()
	select {
	case event, ok := <-stream.events:
		if !ok {
			stream.t.Fatal("sse stream ended early")
		}
		return event
	case <-time.After(testTimeout):
		stream.t.Fatal("timed out waiting for an sse event")
		return sseEvent{}
	}
}

// expectEnd asserts the stream terminates (terminal run, signal, or close).
func (stream *sseStream) expectEnd() {
	stream.t.Helper()
	for {
		select {
		case _, ok := <-stream.events:
			if !ok {
				return
			}
		case <-time.After(testTimeout):
			stream.t.Fatal("timed out waiting for the sse stream to end")
		}
	}
}

func (stream *sseStream) envelope() protocol.Envelope {
	stream.t.Helper()
	event := stream.next()
	if event.name != "" {
		stream.t.Fatalf("expected an envelope event, got named event %q", event.name)
	}
	envelope, err := protocol.ParseEnvelope([]byte(event.data))
	if err != nil {
		stream.t.Fatalf("parse sse envelope: %v (%s)", err, event.data)
	}
	// The SSE id line is the cursor contract: it must carry the envelope's
	// own sequence so Last-Event-ID reconnect maps onto Resume.AfterSequence.
	if envelope.Sequence != nil && event.id != strconv.FormatUint(*envelope.Sequence, 10) {
		stream.t.Fatalf("sse id %q does not match envelope sequence %d", event.id, *envelope.Sequence)
	}
	return envelope
}

// drainUntil reads envelope events until one of the wanted types arrives and
// returns every envelope read, in order.
func (stream *sseStream) drainUntil(want ...protocol.EnvelopeType) []protocol.Envelope {
	stream.t.Helper()
	var envelopes []protocol.Envelope
	for {
		envelope := stream.envelope()
		envelopes = append(envelopes, envelope)
		for _, typ := range want {
			if envelope.Type == typ {
				return envelopes
			}
		}
	}
}

func (stream *sseStream) signal(name string) map[string]any {
	stream.t.Helper()
	event := stream.next()
	if event.name != name {
		stream.t.Fatalf("expected named event %q, got %q (%s)", name, event.name, event.data)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.data), &payload); err != nil {
		stream.t.Fatalf("parse %s signal: %v (%s)", name, err, event.data)
	}
	return payload
}

func requireSequence(t *testing.T, envelopes []protocol.Envelope, first uint64) {
	t.Helper()
	for index, envelope := range envelopes {
		want := first + uint64(index)
		if envelope.Sequence == nil || *envelope.Sequence != want {
			t.Fatalf("envelope %d (%s) sequence %v, want %d", index, envelope.Type, envelope.Sequence, want)
		}
	}
}

// --- shared lifecycle driver ---

func openSession(t *testing.T, server *httptest.Server, adapter, sessionID string) protocol.Envelope {
	t.Helper()
	request := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-"+sessionID, protocol.SessionOpenRequest{SessionID: protocol.SessionID(sessionID)}, sessionID, "", "")
	status, response := postEnvelope(t, server, "/adapters/"+adapter+"/sessions", request)
	if status != http.StatusOK {
		t.Fatalf("open status %d", status)
	}
	requireEnvelopeType(t, response, protocol.TypeSessionOpenResponse)
	if response.InReplyTo != request.ID {
		t.Fatalf("open response correlation %q, want %q", response.InReplyTo, request.ID)
	}
	return response
}

func submitRun(t *testing.T, server *httptest.Server, sessionID, requestID string) (protocol.Envelope, protocol.MessageSubmitResponse) {
	t.Helper()
	request := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, requestID,
		protocol.MessageSubmitRequest{
			SessionID: protocol.SessionID(sessionID), Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("drive the scripted run")}},
		}, sessionID, "", "")
	status, response := postEnvelope(t, server, "/sessions/"+sessionID+"/submit", request)
	if status != http.StatusOK {
		t.Fatalf("submit status %d: %+v", status, response)
	}
	requireEnvelopeType(t, response, protocol.TypeSessionMessageSubmitResponse)
	var admission protocol.MessageSubmitResponse
	if err := response.DecodePayload(&admission); err != nil {
		t.Fatal(err)
	}
	if !admission.Accepted || admission.RunID == "" {
		t.Fatalf("admission not accepted: %+v", admission)
	}
	return response, admission
}

func resolvePermission(t *testing.T, server *httptest.Server, requested protocol.PermissionRequestedPayload, requestID string) {
	t.Helper()
	resolve(t, server, requested.SessionID, requested.RunID, requestID, protocol.TypeActionPermissionResolveRequest, protocol.PermissionResolveRequest{
		InteractionID: requested.InteractionID, RequestedBy: requested.RequestedBy, RespondedBy: requested.RespondedBy,
		SessionID: requested.SessionID, RunID: requested.RunID, ChoiceID: "approve", Granted: true,
	})
}

func resolveInput(t *testing.T, server *httptest.Server, requested protocol.UserInputRequestedPayload, requestID string) {
	t.Helper()
	resolve(t, server, requested.SessionID, requested.RunID, requestID, protocol.TypeUserInputResolveRequest, protocol.UserInputResolveRequest{
		InteractionID: requested.InteractionID, RequestedBy: requested.RequestedBy, RespondedBy: requested.RespondedBy,
		SessionID: requested.SessionID, RunID: requested.RunID,
		Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
	})
}

func resolve(t *testing.T, server *httptest.Server, sessionID protocol.SessionID, runID protocol.RunID, requestID string, typ protocol.EnvelopeType, payload any) {
	t.Helper()
	request := requestEnvelope(t, typ, requestID, payload, string(sessionID), string(runID), "")
	status, response := postEnvelope(t, server, "/sessions/"+string(sessionID)+"/resolve", request)
	if status != http.StatusOK {
		t.Fatalf("resolve status %d: %+v", status, response)
	}
	if response.InReplyTo != request.ID {
		t.Fatalf("resolve response correlation %q", response.InReplyTo)
	}
	switch typ {
	case protocol.TypeActionPermissionResolveRequest:
		requireEnvelopeType(t, response, protocol.TypeActionPermissionResolveResponse)
	default:
		requireEnvelopeType(t, response, protocol.TypeUserInputResolveResponse)
	}
}

func permissionRequestAt(t *testing.T, envelopes []protocol.Envelope) protocol.PermissionRequestedPayload {
	t.Helper()
	for _, envelope := range envelopes {
		if envelope.Type != protocol.TypeActionPermissionRequested {
			continue
		}
		var payload protocol.PermissionRequestedPayload
		if err := envelope.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	t.Fatal("no permission request in envelopes")
	return protocol.PermissionRequestedPayload{}
}

func inputRequestAt(t *testing.T, envelopes []protocol.Envelope) protocol.UserInputRequestedPayload {
	t.Helper()
	for _, envelope := range envelopes {
		if envelope.Type != protocol.TypeUserInputRequested {
			continue
		}
		var payload protocol.UserInputRequestedPayload
		if err := envelope.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	t.Fatal("no user input request in envelopes")
	return protocol.UserInputRequestedPayload{}
}

// goldenRun drives the memory reference adapter's scripted interaction to
// completion over HTTP and returns the run id plus every SSE envelope in
// emission order. The stream is expected to close on the terminal event.
func goldenRun(t *testing.T, server *httptest.Server, sessionID, requestID string) (protocol.RunID, []protocol.Envelope) {
	t.Helper()
	stream := connectSSE(t, server, "/sessions/"+sessionID+"/events", "")
	_, admission := submitRun(t, server, sessionID, requestID)

	initial := stream.drainUntil(protocol.TypeActionPermissionRequested)
	if len(initial) != 4 {
		t.Fatalf("initial events %d, want 4", len(initial))
	}
	requireSequence(t, initial, 1)
	resolvePermission(t, server, permissionRequestAt(t, initial), "resolve-permission-"+requestID)

	middle := stream.drainUntil(protocol.TypeRunStatusUpdated)
	if len(middle) != 5 {
		t.Fatalf("middle events %d, want 5", len(middle))
	}
	requireSequence(t, middle, 5)
	resolveInput(t, server, inputRequestAt(t, middle), "resolve-input-"+requestID)

	final := stream.drainUntil(protocol.TypeRunCompleted)
	if len(final) != 3 {
		t.Fatalf("final events %d, want 3", len(final))
	}
	requireSequence(t, final, 10)
	stream.expectEnd()

	envelopes := append(append(append([]protocol.Envelope{}, initial...), middle...), final...)
	if envelopes[len(envelopes)-1].Type != protocol.TypeRunCompleted {
		t.Fatal("terminal event is not run.completed")
	}
	return admission.RunID, envelopes
}

// --- management surfaces ---

func TestAdaptersListing(t *testing.T) {
	server := newMemoryServer(t, 0)
	var listing struct {
		Adapters []adapterInfo `json:"adapters"`
	}
	if status := getJSON(t, server, "/adapters", &listing); status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if len(listing.Adapters) != 1 {
		t.Fatalf("adapters: %+v", listing.Adapters)
	}
	info := listing.Adapters[0]
	if info.Name != "memory" || info.CapabilityRevision != base.CapabilityRevision {
		t.Fatalf("adapter info: %+v", info)
	}
	if info.Capabilities == nil || info.Capabilities.Endpoint.ID != "reference.memory" {
		t.Fatalf("adapter descriptor: %+v", info.Capabilities)
	}
	if info.Error != "" {
		t.Fatalf("probe error: %s", info.Error)
	}
}

func TestCapabilitiesEndpoint(t *testing.T) {
	server := newMemoryServer(t, 0)
	response, err := server.Client().Get(server.URL + "/adapters/memory/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", response.StatusCode, data)
	}
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireEnvelopeType(t, envelope, protocol.TypeCapabilitiesResponse)
	if envelope.CapabilityRevision != base.CapabilityRevision {
		t.Fatalf("capability revision %q", envelope.CapabilityRevision)
	}
	if envelope.InReplyTo == "" {
		t.Fatal("capabilities response lacks a correlation id")
	}
	var payload protocol.CapabilitiesResponse
	if err := envelope.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Endpoint.ID != "reference.memory" {
		t.Fatalf("endpoint: %+v", payload.Endpoint)
	}
	requireEnvelopeSchema(t, envelope)

	// An unknown adapter is still an envelope-surfaced error.
	missing, err := server.Client().Get(server.URL + "/adapters/ghost/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(missing.Body)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown adapter status %d", missing.StatusCode)
	}
	errorEnvelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, missing.StatusCode, http.StatusNotFound, errorEnvelope, "unknown_adapter")

	status, errorEnvelope := postEnvelope(t, server, "/adapters/ghost/sessions",
		requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-ghost", protocol.SessionOpenRequest{}, "", "", ""))
	requireErrorResponse(t, status, http.StatusNotFound, errorEnvelope, "unknown_adapter")
}

func TestSessionsListingAcrossLifecycle(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "listing")

	sessions := listSessions(t, server)
	if entry := sessionAt(t, sessions, "listing"); entry.Adapter != "memory" || entry.Status != "idle" || entry.ActiveRunID != "" {
		t.Fatalf("idle listing: %+v", entry)
	}
	if _, err := time.Parse(time.RFC3339, sessionAt(t, sessions, "listing").CreatedAt); err != nil {
		t.Fatalf("created_at %q: %v", sessionAt(t, sessions, "listing").CreatedAt, err)
	}

	stream := connectSSE(t, server, "/sessions/listing/events", "")
	_, admission := submitRun(t, server, "listing", "submit-listing")
	if entry := sessionAt(t, listSessions(t, server), "listing"); entry.Status != "running" || entry.ActiveRunID != string(admission.RunID) {
		t.Fatalf("running listing: %+v", entry)
	}

	// Settle the run through the scripted gates before re-reading the list.
	initial := stream.drainUntil(protocol.TypeActionPermissionRequested)
	resolvePermission(t, server, permissionRequestAt(t, initial), "resolve-listing-1")
	middle := stream.drainUntil(protocol.TypeRunStatusUpdated)
	resolveInput(t, server, inputRequestAt(t, middle), "resolve-listing-2")
	stream.drainUntil(protocol.TypeRunCompleted)
	stream.expectEnd()

	if entry := sessionAt(t, listSessions(t, server), "listing"); entry.Status != "idle" || entry.ActiveRunID != "" {
		t.Fatalf("settled listing: %+v", entry)
	}

	response, _ := post(t, server, "/sessions/listing/close", "", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("close status %d", response.StatusCode)
	}
	if entry := sessionAt(t, listSessions(t, server), "listing"); entry.Status != "closed" || entry.ActiveRunID != "" {
		t.Fatalf("closed listing: %+v", entry)
	}
}

// --- open, submit, resolve, cancel, state, close ---

func TestOpenSession(t *testing.T) {
	server := newMemoryServer(t, 0)
	response := openSession(t, server, "memory", "open-test")
	var payload protocol.SessionOpenResponse
	if err := response.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.SessionID != "open-test" || payload.Status != protocol.SessionIdle {
		t.Fatalf("open payload: %+v", payload)
	}
	if response.SessionID != "open-test" {
		t.Fatalf("envelope session scope: %q", response.SessionID)
	}
	requireEnvelopeSchema(t, response)

	// Opening the same explicit id again is rejected, and the duplicate
	// adapter session is not left behind.
	request := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-dup", protocol.SessionOpenRequest{SessionID: "open-test"}, "open-test", "", "")
	status, errorEnvelope := postEnvelope(t, server, "/adapters/memory/sessions", request)
	requireErrorResponse(t, status, http.StatusConflict, errorEnvelope, "session_exists")
}

func TestOpenSessionAssignsIdentifier(t *testing.T) {
	server := newMemoryServer(t, 0)
	request := requestEnvelope(t, protocol.TypeSessionOpenRequest, "open-anon", protocol.SessionOpenRequest{}, "", "", "")
	status, response := postEnvelope(t, server, "/adapters/memory/sessions", request)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	var payload protocol.SessionOpenResponse
	if err := response.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.SessionID == "" {
		t.Fatal("adapter-assigned session id is empty")
	}
}

func TestSubmitRejections(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "reject")

	// Malformed JSON: the error response is still schema-valid and correlated.
	response, data := post(t, server, "/sessions/reject/submit", "application/json", []byte(`{"adapters":`))
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed status %d: %s", response.StatusCode, data)
	}
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, response.StatusCode, http.StatusBadRequest, envelope, "malformed_json")

	// Schema-invalid envelope (missing required delivery).
	invalid := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-invalid", map[string]any{"session_id": "reject", "messages": []any{map[string]any{"role": "user", "content": "x"}}}, "reject", "", "")
	status, errorEnvelope := postEnvelope(t, server, "/sessions/reject/submit", invalid)
	requireErrorResponse(t, status, http.StatusBadRequest, errorEnvelope, "schema_invalid")

	// Wrong envelope type for the endpoint.
	wrongType := requestEnvelope(t, protocol.TypeSessionOpenRequest, "submit-wrong", protocol.SessionOpenRequest{}, "", "", "")
	status, errorEnvelope = postEnvelope(t, server, "/sessions/reject/submit", wrongType)
	requireErrorResponse(t, status, http.StatusBadRequest, errorEnvelope, "type_mismatch")

	// Payload addresses a different session than the URL.
	mismatch := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-mismatch", protocol.MessageSubmitRequest{
		SessionID: "other", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "other", "", "")
	status, errorEnvelope = postEnvelope(t, server, "/sessions/reject/submit", mismatch)
	requireErrorResponse(t, status, http.StatusBadRequest, errorEnvelope, "scope_mismatch")

	// Unknown session.
	unknown := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-unknown", protocol.MessageSubmitRequest{
		SessionID: "ghost", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "ghost", "", "")
	status, errorEnvelope = postEnvelope(t, server, "/sessions/ghost/submit", unknown)
	requireErrorResponse(t, status, http.StatusNotFound, errorEnvelope, "unknown_session")

	// The adapter refuses one control of the submission itself, and the codec
	// relays that refusal under its own typed code rather than flattening it
	// to invalid_submission: a caller must learn what to stop sending.
	rejected := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-refused", protocol.MessageSubmitRequest{
		SessionID: "reject", Delivery: protocol.DeliveryAuto, ToolChoice: json.RawMessage(`{"mode":"named","name":"absent_tool"}`),
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "reject", "", "")
	status, errorEnvelope = postEnvelope(t, server, "/sessions/reject/submit", rejected)
	requireErrorResponse(t, status, http.StatusBadRequest, errorEnvelope, "unsupported_feature")
	var refusal protocol.ErrorResponse
	if err := errorEnvelope.DecodePayload(&refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Error.Details["feature"] != protocol.FeatureToolSelection || refusal.Error.Details["reason"] != "unsatisfiable" || refusal.Error.Details["tool"] != "absent_tool" {
		t.Fatalf("control refusal details = %+v", refusal.Error.Details)
	}

	// A second submission while the run is active reserves the one queued
	// slot the reference adapter discloses; the third exceeds the bound and
	// conflicts, which is the wire's run_active.
	_, admission := submitRun(t, server, "reject", "submit-active")
	queued := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-second", protocol.MessageSubmitRequest{
		SessionID: "reject", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}},
	}, "reject", string(admission.RunID), "")
	status, reservation := postEnvelope(t, server, "/sessions/reject/submit", queued)
	if status != http.StatusOK {
		t.Fatalf("reservation status = %d, want 200 (%s)", status, reservation.Payload)
	}
	var reserved protocol.MessageSubmitResponse
	if err := reservation.DecodePayload(&reserved); err != nil {
		t.Fatal(err)
	}
	if reserved.Admission != protocol.AdmissionQueued || reserved.EffectiveDelivery != protocol.EffectiveDeliveryQueue || reserved.DeliveryResolution != "session_busy" {
		t.Fatalf("reservation = %+v", reserved)
	}
	active := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-third", protocol.MessageSubmitRequest{
		SessionID: "reject", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("and again")}},
	}, "reject", string(admission.RunID), "")
	status, errorEnvelope = postEnvelope(t, server, "/sessions/reject/submit", active)
	requireErrorResponse(t, status, http.StatusConflict, errorEnvelope, "run_active")
}

func TestResolveRejections(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "resolve-reject")

	unknownRun := requestEnvelope(t, protocol.TypeActionPermissionResolveRequest, "resolve-unknown", protocol.PermissionResolveRequest{
		InteractionID: "interaction-1", RequestedBy: "agent", RespondedBy: serve.DefaultParticipant,
		SessionID: "resolve-reject", RunID: "run-404", ChoiceID: "approve", Granted: true,
	}, "resolve-reject", "run-404", "")
	status, errorEnvelope := postEnvelope(t, server, "/sessions/resolve-reject/resolve", unknownRun)
	requireErrorResponse(t, status, http.StatusNotFound, errorEnvelope, "run_not_found")

	// The scripted gate only resolves for the declared responder.
	_, admission := submitRun(t, server, "resolve-reject", "submit-resolve")
	wrongResponder := requestEnvelope(t, protocol.TypeActionPermissionResolveRequest, "resolve-wrong", protocol.PermissionResolveRequest{
		InteractionID: "interaction-1", RequestedBy: "agent", RespondedBy: "someone-else",
		SessionID: "resolve-reject", RunID: admission.RunID, ChoiceID: "approve", Granted: true,
	}, "resolve-reject", string(admission.RunID), "")
	status, errorEnvelope = postEnvelope(t, server, "/sessions/resolve-reject/resolve", wrongResponder)
	requireErrorResponse(t, status, http.StatusConflict, errorEnvelope, "resolution_rejected")
}

func TestStateEndpoint(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "state-test")

	response, err := server.Client().Get(server.URL + "/sessions/state-test/state")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", response.StatusCode, data)
	}
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireEnvelopeType(t, envelope, protocol.TypeSessionStateResponse)
	if envelope.InReplyTo == "" {
		t.Fatal("state response lacks a correlation id")
	}
	var state protocol.SessionState
	if err := envelope.DecodePayload(&state); err != nil {
		t.Fatal(err)
	}
	if state.SessionID != "state-test" || state.Status != protocol.SessionIdle {
		t.Fatalf("state: %+v", state)
	}
	requireEnvelopeSchema(t, envelope)

	missing, err := server.Client().Get(server.URL + "/sessions/ghost/state")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(missing.Body)
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown session state status %d", missing.StatusCode)
	}
	missingEnvelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, missing.StatusCode, http.StatusNotFound, missingEnvelope, "unknown_session")
}

func TestCloseAfterCancellation(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "close-cancel")
	stream := connectSSE(t, server, "/sessions/close-cancel/events", "")
	_, admission := submitRun(t, server, "close-cancel", "submit-close-cancel")

	response, data := post(t, server, "/sessions/close-cancel/close", "", nil)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("active close status %d: %s", response.StatusCode, data)
	}

	cancel := requestEnvelope(t, protocol.TypeRunCancelRequest, "cancel-close-cancel", protocol.RunCancelRequest{SessionID: "close-cancel", RunID: admission.RunID}, "close-cancel", string(admission.RunID), "")
	status, cancelResponse := postEnvelope(t, server, "/sessions/close-cancel/cancel", cancel)
	if status != http.StatusOK {
		t.Fatalf("cancel status %d", status)
	}
	requireEnvelopeType(t, cancelResponse, protocol.TypeRunCancelResponse)
	var ack protocol.RunCancelResponse
	if err := cancelResponse.DecodePayload(&ack); err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.Status != protocol.RunCancelling {
		t.Fatalf("cancel ack: %+v", ack)
	}
	envelopes := stream.drainUntil(protocol.TypeRunCancelled)
	requireSequence(t, envelopes, 1)
	stream.expectEnd()

	response, data = post(t, server, "/sessions/close-cancel/close", "", nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("close status %d: %s", response.StatusCode, data)
	}

	// State after close reports the closed status through the state endpoint.
	stateResponse, err := server.Client().Get(server.URL + "/sessions/close-cancel/state")
	if err != nil {
		t.Fatal(err)
	}
	defer stateResponse.Body.Close()
	stateData, _ := io.ReadAll(stateResponse.Body)
	envelope, err := protocol.ParseEnvelope(stateData)
	if err != nil {
		t.Fatal(err)
	}
	var state protocol.SessionState
	if err := envelope.DecodePayload(&state); err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionClosed {
		t.Fatalf("state after close: %+v", state)
	}

	// Submitting to a closed session is rejected with the adapter's own error.
	submit := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-closed", protocol.MessageSubmitRequest{
		SessionID: "close-cancel", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "close-cancel", "", "")
	status, errorEnvelope := postEnvelope(t, server, "/sessions/close-cancel/submit", submit)
	requireErrorResponse(t, status, http.StatusConflict, errorEnvelope, "session_closed")
}

func TestCancelRejections(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "cancel-reject")

	unknown := requestEnvelope(t, protocol.TypeRunCancelRequest, "cancel-unknown", protocol.RunCancelRequest{SessionID: "cancel-reject", RunID: "run-404"}, "cancel-reject", "run-404", "")
	status, errorEnvelope := postEnvelope(t, server, "/sessions/cancel-reject/cancel", unknown)
	requireErrorResponse(t, status, http.StatusNotFound, errorEnvelope, "run_not_found")

	runID, _ := goldenRun(t, server, "cancel-reject", "submit-cancel-reject")
	terminal := requestEnvelope(t, protocol.TypeRunCancelRequest, "cancel-terminal", protocol.RunCancelRequest{SessionID: "cancel-reject", RunID: runID}, "cancel-reject", string(runID), "")
	status, errorEnvelope = postEnvelope(t, server, "/sessions/cancel-reject/cancel", terminal)
	requireErrorResponse(t, status, http.StatusConflict, errorEnvelope, "run_terminal")
}

func TestHostAllowlist(t *testing.T) {
	_, server := newServer(t, memoryRegistry(0), Options{HostAllowlist: []string{"localhost", "127.0.0.1", "::1"}})

	// A rebinned or cross-origin request names a foreign Host and is
	// refused before any route runs.
	request, err := http.NewRequest(http.MethodGet, server.URL+"/adapters", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "evil.example.com"
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign host status %d: %s", response.StatusCode, data)
	}

	// The loopback names (with any port) still serve.
	for _, host := range []string{"localhost", "127.0.0.1"} {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/adapters", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host + ":9999"
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		data, _ = io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("host %q status %d: %s", host, response.StatusCode, data)
		}
	}
}

func TestCapabilitiesRequiresDescriptorRevision(t *testing.T) {
	// A programmatically registered adapter that probes without a revision
	// must not be relayed into a schema-invalid capabilities response.
	registry := serve.NewRegistry()
	if err := registry.Register("bare", bareAdapter{}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	response, err := server.Client().Get(server.URL + "/adapters/bare/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("bare descriptor status %d: %s", response.StatusCode, data)
	}
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, response.StatusCode, http.StatusInternalServerError, envelope, "probe_failed")

	var listing struct {
		Adapters []adapterInfo `json:"adapters"`
	}
	getJSON(t, server, "/adapters", &listing)
	if len(listing.Adapters) != 1 || listing.Adapters[0].Name != "bare" || listing.Adapters[0].Capabilities != nil {
		t.Fatalf("bare adapter listing: %+v", listing.Adapters)
	}
}

type bareAdapter struct{}

func (bareAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "bare"}}}, nil
}
func (bareAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return nil, errors.New("not used")
}

// noControlsAdapter wraps the memory adapter as an endpoint that advertises no
// per-submit run control, so an unadvertised-control refusal is reachable here
// at all.
type noControlsAdapter struct{ inner base.Adapter }

func (a noControlsAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	return a.inner.Probe(ctx)
}

func (a noControlsAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	entry, err := a.inner.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return noControlsSession{entry}, nil
}

type noControlsSession struct{ base.Session }

func (s noControlsSession) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	if err := base.RefuseUnadvertisedControls(request); err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	return s.Session.Submit(ctx, request)
}

// Wire and schema validity are the floor beneath the refusal ladder, not a
// rung of it: an envelope the protocol cannot read carries no controls to
// judge, because the bytes in the control positions are not a policy or a
// selection until the message is one at all. So the frontend validates before
// it decodes, and a submit that is schema-invalid is answered schema_invalid
// whatever sits in those positions — the endpoint is never reached, and could
// not honestly answer about a control it never received. The ordering is
// deliberate, and it is the same one the validator keeps: its semantic phase,
// where every control rule lives, runs only on a trace whose decode and schema
// phases were clean (decision 0005).
func TestSchemaValidityPrecedesTheControlGate(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", noControlsAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	openSession(t, server, "memory", "floor")

	// Schema-invalid (delivery is required) and carrying a control this
	// endpoint advertises nowhere.
	invalid := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-floor", map[string]any{
		"session_id":   "floor",
		"messages":     []any{map[string]any{"role": "user", "content": "x"}},
		"instructions": "be terse",
	}, "floor", "", "")
	status, errorEnvelope := postEnvelope(t, server, "/sessions/floor/submit", invalid)
	requireErrorResponse(t, status, http.StatusBadRequest, errorEnvelope, "schema_invalid")

	// Repair the envelope and the same control is refused under its own key,
	// so the first answer was about the message and not about the control.
	valid := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-floor-2", protocol.MessageSubmitRequest{
		SessionID: "floor", Delivery: protocol.DeliveryAuto,
		Instructions: protocol.ControlValue("be terse"),
		Messages:     []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, "floor", "", "")
	status, errorEnvelope = postEnvelope(t, server, "/sessions/floor/submit", valid)
	requireErrorResponse(t, status, http.StatusBadRequest, errorEnvelope, "unsupported_feature")
	var refusal protocol.ErrorResponse
	if err := errorEnvelope.DecodePayload(&refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Error.Details["feature"] != protocol.FeatureInstructions {
		t.Fatalf("control refusal details = %+v", refusal.Error.Details)
	}
}
