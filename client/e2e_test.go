package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/serve/servehttp"
)

const testTimeout = 5 * time.Second

// newDaemon serves one registry over the real daemon handler on loopback.
func newDaemon(t *testing.T, registry *serve.Registry) *httptest.Server {
	t.Helper()
	hub := serve.New(registry, serve.Options{})
	daemon, err := servehttp.New(hub, servehttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(daemon.Handler())
	t.Cleanup(server.Close)
	return server
}

func memoryRegistry(capacity int) *serve.Registry {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{JournalCapacity: capacity})); err != nil {
		panic(err)
	}
	return registry
}

func dial(t *testing.T, server *httptest.Server, opts ...Option) *Client {
	t.Helper()
	return New(server.URL, opts...)
}

func openMemorySession(t *testing.T, c *Client, sessionID string) *Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	session, err := c.Open(ctx, "memory", protocol.SessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// readUntil reads envelopes through the first one matching stop, failing on
// any stream error.
func readUntil(t *testing.T, stream *EventStream, stop func(protocol.Envelope) bool) []protocol.Envelope {
	t.Helper()
	var seen []protocol.Envelope
	for {
		envelope, err := stream.Next()
		if err != nil {
			t.Fatalf("stream error after %d envelopes: %v", len(seen), err)
		}
		seen = append(seen, envelope)
		if stop(envelope) {
			return seen
		}
	}
}

func typeStop(typ protocol.EnvelopeType) func(protocol.Envelope) bool {
	return func(envelope protocol.Envelope) bool { return envelope.Type == typ }
}

func requireSequences(t *testing.T, envelopes []protocol.Envelope, runID protocol.RunID) {
	t.Helper()
	for index, envelope := range envelopes {
		if envelope.RunID != runID {
			t.Fatalf("envelope %d run %q, want %q", index, envelope.RunID, runID)
		}
		if envelope.Sequence == nil || *envelope.Sequence != uint64(index+1) {
			t.Fatalf("envelope %d sequence %v, want %d", index, envelope.Sequence, index+1)
		}
	}
}

// submitGolden starts one scripted memory run that parks at the permission
// gate after four envelopes.
func submitGolden(t *testing.T, session *Session, prompt string) protocol.RunID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	admission, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(prompt)}},
		Delivery: protocol.DeliveryAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !admission.Accepted || admission.RunID == "" {
		t.Fatalf("admission not accepted: %+v", admission)
	}
	return admission.RunID
}

// resolveGate resolves the pending permission or input gate using the
// interaction envelope the run parked on.
func resolveGate(t *testing.T, session *Session, requested protocol.Envelope) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	switch requested.Type {
	case protocol.TypeActionPermissionRequested:
		var permission protocol.PermissionRequestedPayload
		if err := requested.DecodePayload(&permission); err != nil {
			t.Fatal(err)
		}
		err := session.ResolvePermission(ctx, protocol.PermissionResolveRequest{
			InteractionID: permission.InteractionID, RequestedBy: permission.RequestedBy,
			RespondedBy: permission.RespondedBy, SessionID: requested.SessionID, RunID: requested.RunID,
			ChoiceID: "approve", Granted: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	case protocol.TypeUserInputRequested:
		var input protocol.UserInputRequestedPayload
		if err := requested.DecodePayload(&input); err != nil {
			t.Fatal(err)
		}
		err := session.ResolveInput(ctx, protocol.UserInputResolveRequest{
			InteractionID: input.InteractionID, RequestedBy: input.RequestedBy,
			RespondedBy: input.RespondedBy, SessionID: requested.SessionID, RunID: requested.RunID,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("cannot resolve %s", requested.Type)
	}
}

// --- daemon-side scripted defects ---

// overflowAdapter wraps the memory adapter so its run streams report an
// event-stream overflow at a fixed 1-based result index, exercising the
// daemon's overflow signal without a child process.
type overflowAdapter struct {
	memory   base.Adapter
	overflow int
}

func (a *overflowAdapter) Probe(ctx context.Context) (base.Descriptor, error) {
	return a.memory.Probe(ctx)
}

func (a *overflowAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := a.memory.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return &overflowSession{Session: session, overflow: a.overflow}, nil
}

type overflowSession struct {
	base.Session
	overflow int
}

func (s *overflowSession) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	admission, stream, err := s.Session.Submit(ctx, request)
	if err != nil {
		return admission, stream, err
	}
	return admission, injectOverflow(stream, s.overflow), nil
}

func injectOverflow(stream base.EventStream, overflowAt int) base.EventStream {
	out := make(chan base.Result, 32)
	go func() {
		defer close(out)
		index := 0
		for result := range stream {
			index++
			if overflowAt != 0 && index == overflowAt {
				out <- base.Result{Error: base.ErrEventStreamOverflow}
			}
			out <- result
		}
	}()
	return out
}

func overflowRegistry(overflowAt int) *serve.Registry {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", &overflowAdapter{memory: base.NewMemory(base.Config{}), overflow: overflowAt}); err != nil {
		panic(err)
	}
	return registry
}

// --- client-side scripted disconnects ---

// droppingTransport kills the first N event-stream connections after they
// deliver a fixed number of complete SSE frames, simulating a mid-stream
// connection drop; every other request passes through untouched.
type droppingTransport struct {
	inner  http.RoundTripper
	drops  int
	frames int

	mu          sync.Mutex
	remaining   int
	connections int
}

func newDroppingTransport(drops, frames int) *droppingTransport {
	return &droppingTransport{inner: http.DefaultTransport, drops: drops, frames: frames, remaining: drops}
}

func (d *droppingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := d.inner.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(request.URL.Path, "/events") {
		return response, nil
	}
	d.mu.Lock()
	affect := d.remaining > 0
	if affect {
		d.remaining--
	}
	d.connections++
	d.mu.Unlock()
	if !affect {
		return response, nil
	}
	response.Body = &frameLimitedBody{ReadCloser: response.Body, frames: d.frames}
	return response, nil
}

func (d *droppingTransport) streamConnections() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connections
}

// frameLimitedBody reads through the frames-th complete frame boundary, then
// fails: the bytes after the boundary are data lost in transit.
type frameLimitedBody struct {
	io.ReadCloser
	frames int
	seen   int
	last   byte
	done   bool
}

func (b *frameLimitedBody) Read(p []byte) (int, error) {
	if b.frames <= 0 || b.done {
		return 0, io.ErrUnexpectedEOF
	}
	n, err := b.ReadCloser.Read(p)
	for i := 0; i < n; i++ {
		if b.last == '\n' && p[i] == '\n' {
			b.seen++
			if b.seen >= b.frames {
				b.done = true
				b.last = p[i]
				return i + 1, nil
			}
		}
		b.last = p[i]
	}
	if err != nil {
		return n, err
	}
	return n, nil
}

// --- discovery and lifecycle ---

func TestClientDiscovery(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	adapters, err := c.Adapters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 1 || adapters[0].Name != "memory" || adapters[0].Error != "" {
		t.Fatalf("adapter listing: %+v", adapters)
	}

	caps, err := c.Capabilities(ctx, "memory")
	if err != nil {
		t.Fatal(err)
	}
	if caps.Revision != base.CapabilityRevision {
		t.Fatalf("capability revision %q, want %q", caps.Revision, base.CapabilityRevision)
	}
	if caps.Descriptor.Endpoint.ID != "reference.memory" {
		t.Fatalf("capability endpoint %+v", caps.Descriptor.Endpoint)
	}

	_, err = c.Capabilities(ctx, "ghost")
	code, ok := ErrorCode(err)
	if !ok || code != "unknown_adapter" {
		t.Fatalf("unknown adapter error: %v (code %q, %v)", err, code, ok)
	}
}

func TestClientOpenGeneratesSessionID(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	session, err := c.Open(ctx, "memory", "")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID() == "" || session.Adapter() != "memory" {
		t.Fatalf("session id %q adapter %q", session.ID(), session.Adapter())
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID != session.ID() || state.Status != protocol.SessionIdle {
		t.Fatalf("state %+v", state)
	}
}

// TestClientAttachesSourcesAndReadsTheCatalog drives the tool-sources unit
// end to end over the real daemon wire: an open that attaches a source, the
// state and the catalog that publish it back, and every listed tool resolving
// to a declared source.
func TestClientAttachesSourcesAndReadsTheCatalog(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server, WithEnvelopeValidation())
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	session, err := c.Open(ctx, "memory", "catalog", AttachToolSources(protocol.ToolSourceAttachment{
		ID: "client-mcp", Kind: protocol.ToolSourceLocal, Protocol: protocol.ToolSourceMCP, Endpoint: "stdio:client-mcp",
	}))
	if err != nil {
		t.Fatal(err)
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !namesSource(state.Sources, "client-mcp") {
		t.Fatalf("session state omits the attached source: %+v", state.Sources)
	}

	listing, err := session.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog := listing.Tools
	// The listing comes back bound to the descriptor snapshot that governs it,
	// end to end over the real wire: the adapter serves the revision with the
	// catalog, the daemon stamps it on the response, and the client hands the
	// pair back. A caller caches the listing against this revision and drops it
	// when capabilities.updated reports another.
	capabilities, err := c.Capabilities(ctx, "memory")
	if err != nil {
		t.Fatal(err)
	}
	if listing.Revision == "" || listing.Revision != capabilities.Revision {
		t.Fatalf("catalog revision %q, want the descriptor's %q", listing.Revision, capabilities.Revision)
	}
	if catalog.SessionID != session.ID() {
		t.Fatalf("catalog names session %q, want %q", catalog.SessionID, session.ID())
	}
	if !namesSource(catalog.Sources, "client-mcp") {
		t.Fatalf("the catalog omits the attached source: %+v", catalog.Sources)
	}
	if len(catalog.Tools) == 0 {
		t.Fatal("the catalog lists no tools")
	}
	for _, tool := range catalog.Tools {
		if !namesSource(catalog.Sources, tool.Source) {
			t.Fatalf("tool %q names source %q, which the catalog does not declare", tool.Name, tool.Source)
		}
	}

	// The degraded opt-in rides the query and is accepted by an endpoint that
	// does not need it, so a caller consenting in advance is never refused
	// for consenting.
	if _, err := session.Tools(ctx, AllowDegradedTools(protocol.FeatureToolsList)); err != nil {
		t.Fatalf("catalog with an opt-in: %v", err)
	}
}

func namesSource(sources []protocol.ToolSourceDescriptor, id string) bool {
	for _, source := range sources {
		if source.ID == id {
			return true
		}
	}
	return false
}

// TestClientLifecycleGolden drives the full scripted lifecycle end to end:
// open, subscribe, submit, permission gate, input gate, terminal settlement,
// state, close — with dev-mode envelope validation on.
func TestClientLifecycleGolden(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server, WithEnvelopeValidation())
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, err := c.Open(ctx, "memory", "lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	stream := session.Events(ctx)
	runID := submitGolden(t, session, "drive the lifecycle")

	initial := readUntil(t, stream, typeStop(protocol.TypeActionPermissionRequested))
	if len(initial) != 4 {
		t.Fatalf("initial burst %d envelopes, want 4", len(initial))
	}
	resolveGate(t, session, initial[len(initial)-1])

	middle := readUntil(t, stream, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))

	final := readUntil(t, stream, typeStop(protocol.TypeRunCompleted))
	all := append(append([]protocol.Envelope(nil), initial...), append(middle, final...)...)
	requireSequences(t, all, runID)

	// The stream ends cleanly at the terminal event.
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal Next error %v, want io.EOF", err)
	}

	// The convenience accessor decodes the final response.
	completed := final[len(final)-1]
	if text, ok := FinalText(completed); !ok || text != "The golden script completed." {
		t.Fatalf("final text %q, %v", text, ok)
	}

	// Authoritative state after settlement, then close.
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle || state.ActiveRunID != "" {
		t.Fatalf("settled state %+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func envelopeOfType(t *testing.T, envelopes []protocol.Envelope, typ protocol.EnvelopeType) protocol.Envelope {
	t.Helper()
	for _, envelope := range envelopes {
		if envelope.Type == typ {
			return envelope
		}
	}
	t.Fatalf("no %s in %d envelopes", typ, len(envelopes))
	return protocol.Envelope{}
}

func TestClientCancelPath(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "cancel")
	stream := session.Events(ctx)
	runID := submitGolden(t, session, "to be cancelled")
	initial := readUntil(t, stream, typeStop(protocol.TypeActionPermissionRequested))

	ack, err := session.Cancel(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted {
		t.Fatalf("cancel ack %+v", ack)
	}
	settled := readUntil(t, stream, typeStop(protocol.TypeRunCancelled))
	// The cancel settlement is the suffix after the initial burst: the
	// cancelling status update, the closed gates, and the terminal event.
	if len(settled) != 4 {
		t.Fatalf("cancel settlement delivered %d envelopes, want 4", len(settled))
	}
	for index, envelope := range settled {
		if envelope.Sequence == nil || *envelope.Sequence != uint64(5+index) {
			t.Fatalf("settlement envelope %d sequence %v, want %d", index, envelope.Sequence, 5+index)
		}
	}
	requireSequences(t, append(append([]protocol.Envelope(nil), initial...), settled...), runID)
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}

	// Re-cancelling a cancelled run is idempotent: the acknowledgement
	// reports the settled status without an error.
	again, err := session.Cancel(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Accepted || again.Status != protocol.RunCancelled {
		t.Fatalf("second cancel ack %+v", again)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestClientCloseRefusesActiveRun(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "close-active")
	submitGolden(t, session, "parks at the gate")

	err := session.Close(ctx)
	code, ok := ErrorCode(err)
	if !ok || code != "run_active" {
		t.Fatalf("close with active run: %v (code %q, %v)", err, code, ok)
	}

	// Settle, then close, then confirm the closed session still reports its
	// final state.
	if _, err := session.Cancel(ctx, mustStateRun(t, session)); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionClosed {
		t.Fatalf("closed session state %+v", state)
	}
}

func mustStateRun(t *testing.T, session *Session) protocol.RunID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveRunID == "" {
		t.Fatalf("no active run in %+v", state)
	}
	return state.ActiveRunID
}

// --- cursor resume ---

// TestClientInvisibleResumeAfterDrop is the headline guarantee: a mid-stream
// connection drop is resumed with the last observed sequence, the suffix
// replays exactly, and the consumer never sees a duplicate or an error.
func TestClientInvisibleResumeAfterDrop(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	transport := newDroppingTransport(1, 4)
	c := dial(t, server, WithHTTPClient(&http.Client{Transport: transport}))
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "resume")
	stream := session.Events(ctx)
	runID := submitGolden(t, session, "resume me")

	// Four envelopes arrive on the first connection before it dies.
	initial := readUntil(t, stream, typeStop(protocol.TypeActionPermissionRequested))
	if len(initial) != 4 {
		t.Fatalf("initial burst %d envelopes, want 4", len(initial))
	}
	resolveGate(t, session, initial[len(initial)-1])
	middle := readUntil(t, stream, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))
	final := readUntil(t, stream, typeStop(protocol.TypeRunCompleted))

	all := append(append([]protocol.Envelope(nil), initial...), append(middle, final...)...)
	requireSequences(t, all, runID)
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}
	if got := transport.streamConnections(); got < 2 {
		t.Fatalf("event stream used %d connections, want at least 2", got)
	}
}

// TestClientStrictResumeReportsDrop pins strict mode: the same drop surfaces
// as a DisconnectError carrying the cursor, and EventsAfter resumes by hand.
func TestClientStrictResumeReportsDrop(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	transport := newDroppingTransport(1, 4)
	c := dial(t, server, WithHTTPClient(&http.Client{Transport: transport}), WithStrictResume())
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "strict")
	stream := session.Events(ctx)
	runID := submitGolden(t, session, "strict drop")

	initial := readUntil(t, stream, typeStop(protocol.TypeActionPermissionRequested))
	resolveGate(t, session, initial[len(initial)-1])

	// The parked connection then dies; strict mode reports instead of
	// resuming, with the position the consumer last saw.
	_, err := stream.Next()
	var disconnect *DisconnectError
	if !errors.As(err, &disconnect) {
		t.Fatalf("error %v (%T), want DisconnectError", err, err)
	}
	if disconnect.RunID != runID || disconnect.LastSequence != 4 {
		t.Fatalf("disconnect cursor %+v, want run %s sequence 4", disconnect, runID)
	}
	// The error is terminal for the stream.
	if _, err := stream.Next(); !errors.Is(err, disconnect) && err.Error() != disconnect.Error() {
		t.Fatalf("repeated Next error %v, want the same disconnect", err)
	}

	// Manual resume replays the suffix and finishes the run.
	resumed := session.EventsAfter(ctx, runID, 4)
	middle := readUntil(t, resumed, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))
	final := readUntil(t, resumed, typeStop(protocol.TypeRunCompleted))
	all := append(append([]protocol.Envelope(nil), initial...), append(middle, final...)...)
	requireSequences(t, all, runID)
	if _, err := resumed.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}
}

// TestClientResumeAfterRunSettled covers reconnecting after the tracked run
// already finished while the client was away: the daemon replays the unseen
// suffix and ends the stream at the terminal event.
func TestClientResumeAfterRunSettled(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	transport := newDroppingTransport(1, 4)
	c := dial(t, server, WithHTTPClient(&http.Client{Transport: transport}))
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "settled")
	stream := session.Events(ctx)
	runID := submitGolden(t, session, "finish without me")
	initial := readUntil(t, stream, typeStop(protocol.TypeActionPermissionRequested))

	// The connection is dead; settle the run through a second stream that
	// subscribes before the resolves.
	settleRun(t, session, initial[len(initial)-1])

	suffix := readUntil(t, stream, typeStop(protocol.TypeRunCompleted))
	all := append([]protocol.Envelope(nil), initial...)
	all = append(all, suffix...)
	requireSequences(t, all, runID)
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}
}

// settleRun drives one parked run to completion through a short-lived helper
// stream subscribed before the resolves, leaving the journal terminal.
func settleRun(t *testing.T, session *Session, permission protocol.Envelope) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	helper := session.Events(ctx)
	resolveGate(t, session, permission)
	middle := readUntil(t, helper, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))
	readUntil(t, helper, typeStop(protocol.TypeRunCompleted))
	if _, err := helper.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("helper stream error %v, want io.EOF", err)
	}
}

// TestClientSpeculativeReconnectLosesNothing covers a drop before the first
// envelope: the reconnect replays the current run from its start, so nothing
// published during the gap is lost.
func TestClientSpeculativeReconnectLosesNothing(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	transport := newDroppingTransport(1, 0)
	c := dial(t, server, WithHTTPClient(&http.Client{Transport: transport}))
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "speculative")
	runID := submitGolden(t, session, "starts without a subscriber")

	// The run is already parked at its gate; the first connection dies
	// immediately, and the reconnect must replay from sequence 1.
	stream := session.Events(ctx)
	initial := readUntil(t, stream, typeStop(protocol.TypeActionPermissionRequested))
	if len(initial) != 4 {
		t.Fatalf("replayed burst %d envelopes, want 4", len(initial))
	}
	requireSequences(t, initial, runID)
	resolveGate(t, session, initial[len(initial)-1])
	middle := readUntil(t, stream, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))
	final := readUntil(t, stream, typeStop(protocol.TypeRunCompleted))
	all := append(append([]protocol.Envelope(nil), initial...), append(middle, final...)...)
	requireSequences(t, all, runID)
}

// TestClientEventsAfterSuffix pins the manual replay surface: a cursor
// replays exactly the suffix, then ends at the terminal event.
func TestClientEventsAfterSuffix(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "suffix")
	runID := driveToCompletion(t, session, "complete quietly")

	stream := session.EventsAfter(ctx, runID, 9)
	replayed := readUntil(t, stream, typeStop(protocol.TypeRunCompleted))
	if len(replayed) != 3 {
		t.Fatalf("replayed %d envelopes, want 3", len(replayed))
	}
	for index, envelope := range replayed {
		if envelope.Sequence == nil || *envelope.Sequence != uint64(10+index) {
			t.Fatalf("replayed envelope %d sequence %v, want %d", index, envelope.Sequence, 10+index)
		}
		if envelope.RunID != runID {
			t.Fatalf("replayed envelope run %q, want %q", envelope.RunID, runID)
		}
	}
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}
}

// driveToCompletion runs the scripted gates to completion synchronously,
// using one event stream consumed to its end.
func driveToCompletion(t *testing.T, session *Session, prompt string) protocol.RunID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	stream := session.Events(ctx)
	runID := submitGolden(t, session, prompt)
	initial := readUntil(t, stream, typeStop(protocol.TypeActionPermissionRequested))
	resolveGate(t, session, initial[len(initial)-1])
	middle := readUntil(t, stream, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))
	readUntil(t, stream, typeStop(protocol.TypeRunCompleted))
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}
	return runID
}

// --- daemon signals as typed errors ---

func TestClientOverflowSignal(t *testing.T) {
	server := newDaemon(t, overflowRegistry(2))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "overflow")
	stream := session.Events(ctx)
	runID := submitGolden(t, session, "overflow the stream")

	first, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.Type != protocol.TypeRunStarted || first.Sequence == nil || *first.Sequence != 1 {
		t.Fatalf("first envelope %s sequence %v", first.Type, first.Sequence)
	}
	_, err = stream.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error %v (%T), want OverflowError", err, err)
	}
	if overflow.LastSequence != 1 || overflow.RunID != runID {
		t.Fatalf("overflow signal %+v, want sequence 1 run %s", overflow, runID)
	}
	// The signal is terminal for the stream; the documented recovery is a
	// cursor resume from the last delivered sequence.
	if _, err := stream.Next(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("post-signal Next returned %v, want the sticky overflow", err)
	}
}

func TestClientReplayGapSignal(t *testing.T) {
	// Journal capacity 2 retains only the run's last two sequences.
	server := newDaemon(t, memoryRegistry(2))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "gap")
	gapRun := driveToCompletion(t, session, "expire the cursor")

	stream := session.EventsAfter(ctx, gapRun, 1)
	_, err := stream.Next()
	var gap *ReplayGapError
	if !errors.As(err, &gap) {
		t.Fatalf("error %v (%T), want ReplayGapError", err, err)
	}
	if gap.RequestedAfter != 1 || gap.OldestAvailable != 11 || gap.LatestAvailable != 12 {
		t.Fatalf("gap signal %+v, want after 1 retained 11..12", gap)
	}
	if gap.RunID != gapRun {
		t.Fatalf("gap signal run %q, want %q", gap.RunID, gapRun)
	}

	// A consumer that accepts the loss resumes at oldest_available - 1 and
	// still sees the retained suffix.
	recovered := session.EventsAfter(ctx, gapRun, gap.OldestAvailable-1)
	replayed := readUntil(t, recovered, typeStop(protocol.TypeRunCompleted))
	if len(replayed) != 2 {
		t.Fatalf("recovered replay %d envelopes, want 2", len(replayed))
	}
}

// --- stream-defect detection against a hand-rolled wire ---

// defectServer serves one canned SSE body, for stream frames the real daemon
// would never emit.
func defectServer(t *testing.T, body string, opts ...Option) *Client {
	t.Helper()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(server.URL, opts...)
}

func openDefectSession(t *testing.T, c *Client) *Session {
	t.Helper()
	session := &Session{client: c, id: "wire", adapter: "memory"}
	return session
}

func validEventEnvelope(t *testing.T, typ protocol.EnvelopeType, sequence uint64) []byte {
	t.Helper()
	return validRunEnvelope(t, typ, "wire-run", sequence)
}

func validRunEnvelope(t *testing.T, typ protocol.EnvelopeType, runID string, sequence uint64) []byte {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(fmt.Sprintf("wire-%s-%d", runID, sequence)), protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = "wire"
	envelope.RunID = protocol.RunID(runID)
	envelope.Sequence = &sequence
	data, err := envelope.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestClientRejectsMalformedFrames(t *testing.T) {
	c := defectServer(t, ": keepalive\ndata: {not an envelope\n\n")
	stream := openDefectSession(t, c).Events(context.Background())
	_, err := stream.Next()
	var malformed *MalformedFrameError
	if !errors.As(err, &malformed) {
		t.Fatalf("error %v (%T), want MalformedFrameError", err, err)
	}
}

func TestClientRejectsFrameIDDisagreement(t *testing.T) {
	c := defectServer(t, fmt.Sprintf("id: 7\ndata: %s\n\n", validEventEnvelope(t, protocol.TypeRunStatusUpdated, 3)))
	stream := openDefectSession(t, c).Events(context.Background())
	_, err := stream.Next()
	var malformed *MalformedFrameError
	if !errors.As(err, &malformed) {
		t.Fatalf("error %v (%T), want MalformedFrameError", err, err)
	}
}

func TestClientRejectsDuplicateSequence(t *testing.T) {
	envelope := validEventEnvelope(t, protocol.TypeRunStatusUpdated, 2)
	c := defectServer(t, fmt.Sprintf("id: 2\ndata: %s\n\nid: 2\ndata: %s\n\n", envelope, envelope))
	stream := openDefectSession(t, c).Events(context.Background())
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := stream.Next()
	var duplicate *DuplicateSequenceError
	if !errors.As(err, &duplicate) {
		t.Fatalf("error %v (%T), want DuplicateSequenceError", err, err)
	}
	if duplicate.Sequence != 2 {
		t.Fatalf("duplicate sequence %d, want 2", duplicate.Sequence)
	}
}

func TestClientRejectsBadResumeSuffix(t *testing.T) {
	// A cursor at 4 must be continued at 5 exactly; a suffix that starts at
	// 9 skipped five envelopes and is surfaced, not accepted. The envelope
	// carries its run id: the first replayed envelope establishes the run the
	// cursor belongs to without relaxing the sequence expectation.
	envelope := validEventEnvelope(t, protocol.TypeRunStatusUpdated, 9)
	c := defectServer(t, fmt.Sprintf("id: 9\ndata: %s\n\n", envelope))
	stream := openDefectSession(t, c).EventsAfter(context.Background(), "wire-run", 4)
	_, err := stream.Next()
	var gap *SequenceGapError
	if !errors.As(err, &gap) {
		t.Fatalf("error %v (%T), want SequenceGapError", err, err)
	}
	if gap.Expected != 5 || gap.Observed != 9 {
		t.Fatalf("gap %+v, want expected 5 observed 9", gap)
	}
}

func TestClientRejectsGapAfterZeroCursor(t *testing.T) {
	// A cursor of zero requests the run from its beginning — unlike a fresh
	// live subscription that may join mid-run — so the first replayed
	// envelope must be sequence 1.
	envelope := validRunEnvelope(t, protocol.TypeRunStatusUpdated, "wire-run", 2)
	c := defectServer(t, fmt.Sprintf("id: 2\ndata: %s\n\n", envelope))
	stream := openDefectSession(t, c).EventsAfter(context.Background(), "wire-run", 0)
	_, err := stream.Next()
	var gap *SequenceGapError
	if !errors.As(err, &gap) {
		t.Fatalf("error %v (%T), want SequenceGapError", err, err)
	}
	if gap.Expected != 1 || gap.Observed != 2 {
		t.Fatalf("gap %+v, want expected 1 observed 2", gap)
	}
}

func TestClientRejectsRunMismatchOnManualResume(t *testing.T) {
	// A manual resume bound to a run must not silently adopt whichever run
	// the daemon applied the cursor to: a first envelope from another run
	// surfaces a mismatch instead of mixing two runs into one recovery.
	envelope := validRunEnvelope(t, protocol.TypeRunStatusUpdated, "other-run", 5)
	c := defectServer(t, fmt.Sprintf("id: 5\ndata: %s\n\n", envelope))
	stream := openDefectSession(t, c).EventsAfter(context.Background(), "bound-run", 4)
	_, err := stream.Next()
	var mismatch *ResumeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error %v (%T), want ResumeMismatchError", err, err)
	}
	if mismatch.ExpectedRunID != "bound-run" || mismatch.ObservedRunID != "other-run" {
		t.Fatalf("mismatch %+v, want bound-run -> other-run", mismatch)
	}
	if mismatch.AfterSequence != 4 || mismatch.ObservedSequence != 5 {
		t.Fatalf("mismatch %+v, want after 4 observed 5", mismatch)
	}
}

func TestClientOverflowCursorTrustsSignal(t *testing.T) {
	// The hub's overflow signal carries the full recovery cursor: the run
	// whose delivery was lost and the consumer's last position in it —
	// possibly a run this connection never reached. The client trusts it
	// as given; overwriting it with the connection's own last position
	// would strand whichever run the signal names.
	envelope := validRunEnvelope(t, protocol.TypeRunStatusUpdated, "consumed-run", 1)
	body := fmt.Sprintf("id: 1\ndata: %s\n\nevent: oap-overflow\ndata: {\"run_id\":\"dropped-run\",\"last_sequence\":7,\"message\":\"fell behind\"}\n\n", envelope)
	c := defectServer(t, body)
	stream := openDefectSession(t, c).Events(context.Background())
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := stream.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "dropped-run" || overflow.LastSequence != 7 {
		t.Fatalf("overflow cursor %+v, want dropped-run at 7 — the signal's cursor", overflow)
	}
}

func TestClientRejectsForeignSessionEvents(t *testing.T) {
	// An envelope naming another session never belongs on this stream: a
	// misrouted stream must not deliver another session's content as this
	// one's, whatever its sequence numbers say.
	envelope, err := protocol.NewEnvelope(protocol.TypeRunStatusUpdated, protocol.EnvelopeID("wire-foreign"), protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = "other-session"
	one := uint64(1)
	envelope.Sequence = &one
	data, err := envelope.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	c := defectServer(t, fmt.Sprintf("data: %s\n\n", data))
	stream := openDefectSession(t, c).Events(context.Background())
	_, err = stream.Next()
	var malformed *MalformedFrameError
	if !errors.As(err, &malformed) {
		t.Fatalf("error %v (%T), want MalformedFrameError", err, err)
	}
	if !strings.Contains(malformed.Error(), "other-session") {
		t.Fatalf("malformed detail %q, want the foreign session named", malformed.Error())
	}
}

func TestClientRejectsOutOfScopeResponse(t *testing.T) {
	// A correlated response scoped to another session is another operation's
	// answer: the protocol validator rejects such pairs, and so must the
	// client.
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		response, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID("scoped-response"), protocol.SessionOpenResponse{
			SessionID: "other-session", Status: protocol.SessionIdle,
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if request, err := protocol.ParseEnvelope(body); err == nil {
			response.InReplyTo = request.ID
		}
		response.SessionID = "other-session"
		encoded, _ := response.MarshalJSON()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
	})
	_, err := c.Open(context.Background(), "memory", "wire")
	if err == nil || !strings.Contains(err.Error(), "other-session") {
		t.Fatalf("out-of-scope response error: %v", err)
	}
}

func TestClientRequestIDsUniqueAcrossClients(t *testing.T) {
	// OAP envelope ids are trace-unique: two clients in one process must not
	// mint the same request id, or a combined trace reports duplicates and
	// in_reply_to values become ambiguous.
	var mu sync.Mutex
	var ids []string
	submit := func(c *Client, sessionID string) {
		session := &Session{client: c, id: protocol.SessionID(sessionID), adapter: "memory"}
		_, _ = session.Submit(context.Background(), protocol.MessageSubmitRequest{
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
			Delivery: protocol.DeliveryAuto,
		})
	}
	for index := range 2 {
		c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if envelope, err := protocol.ParseEnvelope(body); err == nil {
				mu.Lock()
				ids = append(ids, string(envelope.ID))
				mu.Unlock()
			}
			w.WriteHeader(http.StatusNoContent)
		})
		submit(c, fmt.Sprintf("wire-%d", index))
	}
	if len(ids) != 2 {
		t.Fatalf("captured %d request ids, want 2", len(ids))
	}
	if ids[0] == ids[1] {
		t.Fatalf("two clients minted the same request id %q", ids[0])
	}
}

func TestClientRejectsUnsequencedEnvelope(t *testing.T) {
	// A message frame with neither an id field nor an envelope sequence
	// cannot be positioned: delivering it would leave the cursor behind it,
	// so a reconnect would replay it. The stream contract is sequenced run
	// events; anything else is malformed.
	envelope, err := protocol.NewEnvelope(protocol.TypeRunStatusUpdated, protocol.EnvelopeID("wire-noseq"), protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = "wire"
	data, err := envelope.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	c := defectServer(t, fmt.Sprintf("data: %s\n\n", data))
	stream := openDefectSession(t, c).Events(context.Background())
	_, err = stream.Next()
	var malformed *MalformedFrameError
	if !errors.As(err, &malformed) {
		t.Fatalf("error %v (%T), want MalformedFrameError", err, err)
	}
	if !strings.Contains(malformed.Error(), "no sequence") {
		t.Fatalf("malformed detail %q, want the sequence requirement", malformed.Error())
	}
}

func TestClientRejectsZeroSequence(t *testing.T) {
	// Sequences start at one: a present-but-zero sequence is schema-invalid
	// and would leave the cursor disabled, admitting further defects.
	envelope, err := protocol.NewEnvelope(protocol.TypeRunStatusUpdated, protocol.EnvelopeID("wire-zero"), protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	envelope.Sequence = &zero
	envelope.SessionID = "wire"
	data, err := envelope.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	c := defectServer(t, fmt.Sprintf("data: %s\n\n", data))
	stream := openDefectSession(t, c).Events(context.Background())
	_, err = stream.Next()
	var malformed *MalformedFrameError
	if !errors.As(err, &malformed) {
		t.Fatalf("error %v (%T), want MalformedFrameError", err, err)
	}
}

func TestClientValidationAppliesToEveryErrorResponse(t *testing.T) {
	// The validation option promises every inbound envelope; the listing and
	// stream-opening paths read error responses directly and must validate
	// them too, not only request exchanges.
	const raw = `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","payload":{"error":{"code":"unknown_session","message":"nope"}}}`
	validate := WithEnvelopeValidation()

	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, raw)
	}, validate)
	if _, err := c.Adapters(context.Background()); err == nil || !strings.Contains(err.Error(), "schema validation") {
		t.Fatalf("adapters listing error: %v", err)
	}

	streaming := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, raw)
	}, validate)
	session := &Session{client: streaming, id: "wire", adapter: "memory"}
	if _, err := session.Events(context.Background()).Next(); err == nil || !strings.Contains(err.Error(), "schema validation") {
		t.Fatalf("stream opening error: %v", err)
	}

	closing := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, raw)
	}, validate)
	closingSession := &Session{client: closing, id: "wire", adapter: "memory"}
	if err := closingSession.Close(context.Background()); err == nil || !strings.Contains(err.Error(), "schema validation") {
		t.Fatalf("close error: %v", err)
	}
}

func TestClientRejectsNon200StreamStatus(t *testing.T) {
	// A 2xx that is not a stream (a proxy's 204) must surface as an error,
	// not be dereferenced as a response with a stream body.
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	stream := session.Events(context.Background())
	_, err := stream.Next()
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("error %v (%T), want ServerError", err, err)
	}
	if serverErr.Status != http.StatusNoContent {
		t.Fatalf("stream status error %+v, want 204", serverErr)
	}
	if _, err := stream.Next(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("repeated Next returned %v, want the sticky error", err)
	}
}

func TestClientRejectsNonEventStreamContentType(t *testing.T) {
	// A 200 with an HTML or JSON body is not an event stream; parsing it as
	// one would masquerade as drops or park forever. The endpoint must
	// declare text/event-stream.
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<html>proxy login page</html>")
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	stream := session.Events(context.Background())
	_, err := stream.Next()
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("error %v (%T), want ServerError", err, err)
	}
	if !strings.Contains(serverErr.Message, "text/event-stream") {
		t.Fatalf("content type error %+v", serverErr)
	}
}

func TestClientMidRunJoinAccepted(t *testing.T) {
	// The stream's first observed envelope may join a run in progress: a
	// subscription opened mid-run legitimately starts at the join position.
	c := defectServer(t, fmt.Sprintf("id: 5\ndata: %s\n\n", validRunEnvelope(t, protocol.TypeRunStatusUpdated, "joined-run", 5)))
	stream := openDefectSession(t, c).Events(context.Background())
	envelope, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Sequence == nil || *envelope.Sequence != 5 {
		t.Fatalf("joined envelope sequence %v, want 5", envelope.Sequence)
	}
}

func TestClientRejectsLateRunStart(t *testing.T) {
	// A live transition to a new run must begin at sequence 1: the stream
	// was attached throughout, so a first envelope later than the run's
	// opening means events were lost.
	joined := validRunEnvelope(t, protocol.TypeRunStatusUpdated, "first-run", 5)
	late := validRunEnvelope(t, protocol.TypeRunStatusUpdated, "second-run", 3)
	c := defectServer(t, fmt.Sprintf("id: 5\ndata: %s\n\nid: 3\ndata: %s\n\n", joined, late))
	stream := openDefectSession(t, c).Events(context.Background())
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := stream.Next()
	var gap *SequenceGapError
	if !errors.As(err, &gap) {
		t.Fatalf("error %v (%T), want SequenceGapError", err, err)
	}
	if gap.Expected != 1 || gap.Observed != 3 {
		t.Fatalf("gap %+v, want expected 1 observed 3", gap)
	}
}

func TestClientValidationRejectsIdlessErrorEnvelope(t *testing.T) {
	// Parses and is typed error.response, but omits the required top-level
	// id: the presence test must not key on the id, or this invalid envelope
	// skips validation entirely.
	const raw = `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","payload":{"error":{"code":"unknown_session","message":"nope"}}}`
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, raw)
	}, WithEnvelopeValidation())
	session := &Session{client: c, id: "wire", adapter: "memory"}
	_, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
		Delivery: protocol.DeliveryAuto,
	})
	if err == nil || !strings.Contains(err.Error(), "schema validation") {
		t.Fatalf("idless error envelope: %v", err)
	}
}

func TestClientRejectsUncorrelatedErrorEnvelope(t *testing.T) {
	response, err := protocol.NewEnvelope(protocol.TypeErrorResponse, protocol.EnvelopeID("err-1"), protocol.ErrorResponse{
		Error: protocol.ProtocolError{Code: "unknown_session", Message: "no such session"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response.InReplyTo = "someone-elses-request"
	body, err := response.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	_, err = session.Submit(context.Background(), protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
		Delivery: protocol.DeliveryAuto,
	})
	if err == nil || !strings.Contains(err.Error(), "correlation") {
		t.Fatalf("uncorrelated error envelope: %v", err)
	}
}

func TestClientValidationRejectsInvalidErrorEnvelope(t *testing.T) {
	// Parses as an envelope, typed error.response, but the payload violates
	// the schema (no error member): dev-mode validation must flag it instead
	// of typing it as this operation's refusal.
	const raw = `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"err-2","payload":{}}`
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, raw)
	}, WithEnvelopeValidation())
	session := &Session{client: c, id: "wire", adapter: "memory"}
	_, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
		Delivery: protocol.DeliveryAuto,
	})
	if err == nil || !strings.Contains(err.Error(), "schema validation") {
		t.Fatalf("invalid error envelope: %v", err)
	}
}

func TestClientRejectsLiveSequenceGap(t *testing.T) {
	// A gap within a run on a live connection means an envelope was lost in
	// flight or never published; the client surfaces it instead of silently
	// advancing its cursor past the hole.
	first := validEventEnvelope(t, protocol.TypeRunStatusUpdated, 1)
	third := validEventEnvelope(t, protocol.TypeRunStatusUpdated, 3)
	c := defectServer(t, fmt.Sprintf("id: 1\ndata: %s\n\nid: 3\ndata: %s\n\n", first, third))
	stream := openDefectSession(t, c).Events(context.Background())
	if _, err := stream.Next(); err != nil {
		t.Fatal(err)
	}
	_, err := stream.Next()
	var gap *SequenceGapError
	if !errors.As(err, &gap) {
		t.Fatalf("error %v (%T), want SequenceGapError", err, err)
	}
	if gap.Expected != 2 || gap.Observed != 3 {
		t.Fatalf("gap %+v, want expected 2 observed 3", gap)
	}
}

// TestClientRunChangedUnderCursor covers the cross-run reconnect: the cursor
// belongs to a run that finished while disconnected, and a newer run is the
// daemon's current one; the replayed suffix cannot continue the stream the
// consumer was reading, so the mismatch is surfaced rather than silently
// mixing two sequence spaces.
func TestClientRunChangedUnderCursor(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	transport := newDroppingTransport(1, 4)
	c := dial(t, server, WithHTTPClient(&http.Client{Transport: transport}))
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "run-change")
	stream := session.Events(ctx)
	first := submitGolden(t, session, "first run")
	initial := readUntil(t, stream, typeStop(protocol.TypeActionPermissionRequested))
	if len(initial) != 4 {
		t.Fatalf("initial burst %d envelopes, want 4", len(initial))
	}

	// The connection is dead. Settle run one, start run two, and push it
	// past its permission gate so its journal holds sequence 5 onward.
	settleRun(t, session, initial[len(initial)-1])
	second := submitGolden(t, session, "second run")
	secondEvents := session.EventsAfter(ctx, second, 0)
	secondInitial := readUntil(t, secondEvents, typeStop(protocol.TypeActionPermissionRequested))
	resolveGate(t, session, secondInitial[len(secondInitial)-1])

	// The reconnect's cursor (run one, sequence 4) is applied to run two;
	// run two's suffix at 5 exposes the mismatch.
	_, err := stream.Next()
	var mismatch *ResumeMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("error %v (%T), want ResumeMismatchError", err, err)
	}
	if mismatch.ExpectedRunID != first || mismatch.ObservedRunID != second {
		t.Fatalf("mismatch runs %q -> %q, want %q -> %q", mismatch.ExpectedRunID, mismatch.ObservedRunID, first, second)
	}
}

// --- request validation ---

func TestClientSubmitScopeMismatch(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "scope")
	_, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "other-session",
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
		Delivery:  protocol.DeliveryAuto,
	})
	if err == nil || !strings.Contains(err.Error(), "other-session") {
		t.Fatalf("scope mismatch error: %v", err)
	}
}

func TestClientUnknownSessionStream(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	stream := (&Session{client: c, id: "ghost", adapter: "memory"}).Events(ctx)
	_, err := stream.Next()
	code, ok := ErrorCode(err)
	if !ok || code != "unknown_session" {
		t.Fatalf("unknown session stream error: %v (code %q, %v)", err, code, ok)
	}
}

func TestClientOpenUnknownAdapter(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, err := c.Open(ctx, "ghost", "session")
	code, ok := ErrorCode(err)
	if !ok || code != "unknown_adapter" {
		t.Fatalf("unknown adapter open error: %v (code %q, %v)", err, code, ok)
	}
}

func TestClientValidationRejectsInvalidEnvelope(t *testing.T) {
	// An envelope missing every required member but the type.
	c := defectServer(t, "data: {\"type\":\"run.started\"}\n\n", WithEnvelopeValidation())
	session := openDefectSession(t, c)
	stream := session.Events(context.Background())
	if _, err := stream.Next(); err == nil || !strings.Contains(err.Error(), "schema validation") {
		t.Fatalf("validation error: %v", err)
	}
}

// requestStub serves one canned response for every request, for daemon
// misbehavior the real server never exhibits on the request surface.
func requestStub(t *testing.T, respond func(w http.ResponseWriter, r *http.Request), opts ...Option) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	return New(server.URL, opts...)
}

func TestClientRejectsUncorrelatedResponse(t *testing.T) {
	response, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID("stale-response"), protocol.SessionOpenResponse{
		SessionID: "wire", Status: protocol.SessionIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	response.InReplyTo = "someone-elses-request"
	body, err := response.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	_, err = c.Open(context.Background(), "memory", "wire")
	if err == nil || !strings.Contains(err.Error(), "correlation") {
		t.Fatalf("uncorrelated open error: %v", err)
	}
}

func TestClientRejectsNon204Close(t *testing.T) {
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "<html>proxy page</html>")
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	err := session.Close(context.Background())
	if err == nil || !strings.Contains(err.Error(), "204") {
		t.Fatalf("non-204 close error: %v", err)
	}
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("error %v (%T), want ServerError", err, err)
	}
	if code, ok := ErrorCode(err); ok {
		t.Fatalf("plain-body close error reports code %q, %v; want none", code, ok)
	}
}

func TestClientErrorCodeAbsentForPlainFailures(t *testing.T) {
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream melted")
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	_, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
		Delivery: protocol.DeliveryAuto,
	})
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("error %v (%T), want ServerError", err, err)
	}
	if serverErr.Status != http.StatusBadGateway || serverErr.Code != "" || serverErr.Message != "upstream melted" {
		t.Fatalf("plain failure error %+v", serverErr)
	}
	if code, ok := ErrorCode(err); ok {
		t.Fatalf("ErrorCode on an untyped failure: %q, %v; want absent", code, ok)
	}
}

// A per-submit run control crosses the daemon wire end to end: the admitted
// model is echoed on the admission and on run.started, and a model outside the
// endpoint's catalog comes back as a typed refusal naming the id it could not
// serve rather than as a generic invalid submission (decision 0005).
func TestClientDrivesRunControls(t *testing.T) {
	server := newDaemon(t, memoryRegistry(64))
	c := dial(t, server)
	session := openMemorySession(t, c, "controls")
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	stream := session.Events(ctx)

	admission, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("pick a model")}},
		Delivery: protocol.DeliveryAuto,
		ModelID:  protocol.ControlValue(base.ModelSecondary),
	})
	if err != nil {
		t.Fatalf("admitted model refused: %v", err)
	}
	if admission.ModelID != base.ModelSecondary {
		t.Fatalf("admission model %q, want %q", admission.ModelID, base.ModelSecondary)
	}
	started := readUntil(t, stream, typeStop(protocol.TypeRunStarted))
	var payload protocol.RunStartedPayload
	if err := started[len(started)-1].DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.ModelID != base.ModelSecondary {
		t.Fatalf("run.started model %q, want %q", payload.ModelID, base.ModelSecondary)
	}
	// The application is per_run, so the model the next control-free
	// submission would use has not moved.
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentModelID != "" {
		t.Fatalf("per_run selection moved the session default to %q", state.CurrentModelID)
	}

	_, err = session.Submit(ctx, protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("pick another")}},
		Delivery: protocol.DeliveryAuto,
		ModelID:  protocol.ControlValue("model-the-catalog-lacks"),
	})
	var serverErr *ServerError
	if !errors.As(err, &serverErr) || serverErr.Code != "model_not_found" {
		t.Fatalf("unknown model: got %v, want a typed model_not_found refusal", err)
	}
	if serverErr.Details["model_id"] != "model-the-catalog-lacks" {
		t.Fatalf("refusal details %+v, want the requested id", serverErr.Details)
	}
}

// A queued submission reaches the client as the reservation it is, and the
// state the client reads describes both nonterminal runs: a caller that saw
// only active_run_id would think the session had one run's work outstanding
// when it has two.
func TestClientQueuedSubmission(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	c := dial(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, c, "queue-client")
	first := submitGolden(t, session, "parks at the gate")
	reservation, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("after you")}},
		Delivery: protocol.DeliveryQueue,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Admission != protocol.AdmissionQueued || reservation.EffectiveDelivery != protocol.EffectiveDeliveryQueue {
		t.Fatalf("reservation = %+v", reservation)
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 2 || state.ActiveRuns[0].RunID != first || state.ActiveRuns[1].RunID != reservation.RunID {
		t.Fatalf("active_runs = %+v", state.ActiveRuns)
	}
	if state.ActiveRuns[1].QueuePosition == nil || *state.ActiveRuns[1].QueuePosition != 1 {
		t.Fatalf("queue position = %+v", state.ActiveRuns[1])
	}
	if state.ActiveRunID != first {
		t.Fatalf("active_run_id = %q, want the started run %q", state.ActiveRunID, first)
	}

	// The endpoint's disclosed bound reaches the client too, so a caller can
	// tell a queue it may use from one it may not.
	capabilities, err := c.Capabilities(ctx, "memory")
	if err != nil {
		t.Fatal(err)
	}
	limits := capabilities.Descriptor.Limits
	if limits == nil || limits.MaxQueuedRunsPerSession == nil || *limits.MaxQueuedRunsPerSession != 1 {
		t.Fatalf("limits = %+v", limits)
	}
}
