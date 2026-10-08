package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const routeQueueSize = 1024

const stderrTailBytes = 8 << 10

type routeKind int

const (
	routeStream routeKind = iota
	routeSession
)

func (k routeKind) String() string {
	if k == routeStream {
		return "stream"
	}
	return "session"
}

type transport struct {
	agentRevision string
	agentEndpoint string
	agentFeatures map[string]bool
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	logger        *slog.Logger
	shutdownGrace time.Duration

	writeMu sync.Mutex

	mu         sync.Mutex
	streams    map[string][]*subscription
	sessions   map[string][]*subscription
	correlates map[string]*subscription
	inferences map[string]*subscription
	authFlows  map[string]*subscription
	stderr     *tailBuffer

	done chan struct{}

	exited chan struct{}

	readErr atomic.Pointer[error]
	waitErr atomic.Pointer[error]

	closeOnce sync.Once
	closeErr  error
	closing   atomic.Bool
}

func startTransport(ctx context.Context, command string, opts *Options) (*transport, error) {
	cmd := exec.Command(command, opts.args()...)
	cmd.Dir = opts.Dir
	if opts.Env != nil {
		cmd.Env = opts.Env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, transportErrorf(err, "cannot open runtime stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, transportErrorf(err, "cannot open runtime stdout: %v", err)
	}
	stderrTail := newTailBuffer(stderrTailBytes)
	cmd.Stderr = stderrTail

	t := &transport{
		cmd:           cmd,
		stdin:         stdin,
		logger:        opts.logger(),
		shutdownGrace: opts.shutdownGraceOrDefault(),
		streams:       make(map[string][]*subscription),
		sessions:      make(map[string][]*subscription),
		correlates:    make(map[string]*subscription),
		inferences:    make(map[string]*subscription),
		authFlows:     make(map[string]*subscription),
		stderr:        stderrTail,
		done:          make(chan struct{}),
		exited:        make(chan struct{}),
	}

	if err := cmd.Start(); err != nil {
		return nil, transportErrorf(err, "cannot start runtime %q: %v", command, err)
	}
	t.logger.Debug("oap sdk: runtime started", "command", command, "args", opts.args(), "pid", cmd.Process.Pid)

	handshake := make(chan *inbound, 1)
	go t.readLoop(stdout, handshake)
	if err := t.sendEnvelope(oapFrame(oapAgent, "protocol.initialize.request", map[string]any{
		"participant":       map[string]any{"id": sdkParticipant, "name": "OAP Go SDK"},
		"protocol_versions": []string{"0.1"},
		"profiles":          []string{"open-agent-protocol.agent-control-core"},
	})); err != nil {
		_ = t.close()
		return nil, err
	}

	if err := t.awaitHandshake(ctx, handshake, opts); err != nil {
		_ = t.close()
		return nil, err
	}
	request := oapFrame(oapAgent, "capabilities.request", map[string]any{})
	sub := t.subscribeStream(string(request.ID))
	response, err := oapRequest(ctx, t, sub, opts.handshakeTimeout(), request)
	sub.close()
	if err != nil || response.Type != "capabilities.response" || response.CapabilityRevision == "" {
		_ = t.close()
		if err != nil {
			return nil, err
		}
		return nil, &ProtocolError{Code: CodeMalformedResponse, Message: "OAP capabilities response omitted capability_revision"}
	}
	t.agentRevision = response.CapabilityRevision
	described := envelopePayload(response)
	t.agentEndpoint = described.obj("endpoint").str("id")
	t.agentFeatures = map[string]bool{}
	for feature, support := range described.obj("features") {
		if entry, ok := support.(map[string]any); ok && entry["level"] == "unavailable" {
			continue
		}
		t.agentFeatures[feature] = true
	}
	return t, nil
}

func (t *transport) awaitHandshake(ctx context.Context, handshake <-chan *inbound, opts *Options) error {
	timer := time.NewTimer(opts.handshakeTimeout())
	defer timer.Stop()

	select {
	case in, ok := <-handshake:
		if !ok {
			return t.terminalError("handshake")
		}
		if in.broken != nil {
			return in.broken
		}
		switch in.kind() {
		case "protocol.initialize.response":
			expected := opts.protocolVersion()
			actual := in.body().str("protocol_version")
			if actual != expected {
				return fmt.Errorf("%w: expected %q, got %q", ErrProtocolVersion, expected, actual)
			}
			t.logger.Debug("oap: initialize complete", "protocol_version", actual)
			return nil
		case "error", "error.response":
			payload := in.body()
			if nested := payload.obj("error"); nested != nil {
				payload = nested
			}
			return &ProtocolError{
				Code:    payload.str("code", "error_code"),
				Message: payload.strOrDefault("runtime rejected the handshake", "message", "reason"),
			}
		default:
			return transportErrorf(nil, "unexpected handshake frame type %q", in.kind())
		}
	case <-timer.C:
		return transportErrorf(nil, "timed out waiting for the runtime handshake after %s", opts.handshakeTimeout())
	case <-ctx.Done():
		return abortError(ctx.Err(), "handshake")
	case <-t.done:
		return t.terminalError("handshake")
	}
}

func (t *transport) readLoop(stdout io.ReadCloser, handshake chan<- *inbound) {
	reader := newFrameReader(stdout)
	delivered := false

	defer func() {
		if !delivered {
			close(handshake)
		}
		close(t.done)
		t.wakeAll()

		err := t.cmd.Wait()
		t.waitErr.Store(&err)
		close(t.exited)
	}()

	for {
		in, err := reader.nextInbound()
		if errors.Is(err, errMalformedFrame) {
			t.logger.Warn("oap sdk: discarding malformed frame from runtime")
			continue
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) && !isPipeClosed(err) {
				t.readErr.Store(&err)
				t.logger.Error("oap sdk: runtime read failed", "error", err)
			}
			return
		}
		if !delivered {
			delivered = true
			handshake <- in
			close(handshake)
			continue
		}
		t.dispatch(in)
	}
}

func (t *transport) dispatch(in *inbound) {
	kind, replyTo, sessionID, inferenceID := in.kind(), in.replyTo(), in.session(), in.inference()
	t.logger.Debug("oap sdk: frame received",
		"type", kind, "session_id", sessionID,
		"sequence", in.sequence(), "in_reply_to", replyTo)

	t.mu.Lock()
	var target *subscription
	if replyTo != "" {
		target = t.correlates[replyTo]
		if target != nil && kind == "inference.create.response" && inferenceID != "" {
			t.inferences[inferenceID] = target
		}
		if target != nil && kind == "auth.login.start.response" {
			if flowID := in.flow(); flowID != "" {
				t.authFlows[flowID] = target
			}
		}
	}
	if target == nil && (kind == "auth.login.event" || kind == "auth.login.completed") {
		target = t.authFlows[in.flow()]
	}
	if target == nil && inferenceID != "" {
		target = t.inferences[inferenceID]
	}
	if target == nil && sessionID != "" {
		if subs := t.sessions[sessionID]; len(subs) > 0 {
			target = subs[0]
		}
	}

	t.mu.Unlock()

	if target == nil {
		t.logger.Debug("oap sdk: dropping unroutable frame", "type", kind,
			"session_id", sessionID, "in_reply_to", replyTo)
		return
	}
	target.deliver(in)
}

func (t *transport) sendEnvelope(env protocol.Envelope) error {
	if env.Profile == oapAgent && env.Type != protocol.TypeProtocolInitializeRequest && env.Type != protocol.TypeCapabilitiesRequest {
		env.CapabilityRevision = t.agentRevision
	}
	return t.write(mustMarshal(env), env.Type, "", string(env.SessionID), 0)
}

func (t *transport) sendProviderEnvelope(env protocol.ProviderEnvelope) error {
	return t.write(mustMarshal(env), env.Type, "", "", 0)
}

func (t *transport) write(encoded []byte, frameType any, streamID, sessionID string, sequence int64) error {
	line := make([]byte, 0, len(encoded)+1)
	line = append(line, encoded...)
	line = append(line, '\n')

	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	select {
	case <-t.done:
		return t.terminalError("send")
	default:
	}
	if t.closing.Load() {
		return t.terminalError("send")
	}

	t.logger.Debug("oap sdk: frame sent",
		"type", frameType, "stream_id", streamID, "session_id", sessionID, "sequence", sequence)
	if _, err := t.stdin.Write(line); err != nil {
		if terminal := t.terminalErrorIfDown(); terminal != nil {
			return terminal
		}
		return transportErrorf(err, "cannot write %s frame to the runtime: %v", frameType, err)
	}
	return nil
}

func (t *transport) sendEnvelopeBestEffort(env protocol.Envelope) {
	if err := t.sendEnvelope(env); err != nil {
		t.logger.Debug("oap sdk: best-effort envelope not sent", "type", env.Type, "error", err)
	}
}

func (t *transport) sendProviderEnvelopeBestEffort(env protocol.ProviderEnvelope) {
	if err := t.sendProviderEnvelope(env); err != nil {
		t.logger.Debug("oap sdk: best-effort envelope not sent", "type", env.Type, "error", err)
	}
}

func (t *transport) subscribeStream(id string) *subscription { return t.subscribe(routeStream, id) }

func (t *transport) subscribeSession(id string) *subscription { return t.subscribe(routeSession, id) }

func (t *transport) subscribe(kind routeKind, id string) *subscription {
	sub := &subscription{
		transport: t,
		kind:      kind,
		id:        id,
		queue:     make(chan *inbound, routeQueueSize),
		wake:      make(chan struct{}, 1),
	}
	t.mu.Lock()
	if kind == routeStream {
		t.streams[id] = append(t.streams[id], sub)
	} else {
		t.sessions[id] = append(t.sessions[id], sub)
	}
	t.mu.Unlock()
	return sub
}

func (t *transport) unsubscribe(sub *subscription) {
	t.mu.Lock()
	defer t.mu.Unlock()

	table := t.sessions
	if sub.kind == routeStream {
		table = t.streams
	}
	subs := table[sub.id]
	for i, candidate := range subs {
		if candidate == sub {
			subs = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(subs) == 0 {
		delete(table, sub.id)
	} else {
		table[sub.id] = subs
	}
	for messageID, owner := range t.correlates {
		if owner == sub {
			delete(t.correlates, messageID)
		}
	}
	for inferenceID, owner := range t.inferences {
		if owner == sub {
			delete(t.inferences, inferenceID)
		}
	}
	for flowID, owner := range t.authFlows {
		if owner == sub {
			delete(t.authFlows, flowID)
		}
	}
}

func (t *transport) wakeAll() {
	t.mu.Lock()
	subs := make([]*subscription, 0, len(t.streams)+len(t.sessions))
	for _, list := range t.streams {
		subs = append(subs, list...)
	}
	for _, list := range t.sessions {
		subs = append(subs, list...)
	}
	t.mu.Unlock()

	for _, sub := range subs {
		sub.signal()
	}
}

func (t *transport) terminalError(operation string) error {
	<-t.done
	if err := t.readErr.Load(); err != nil && *err != nil {
		return transportErrorf(*err, "%s failed: runtime stream error: %v", operation, *err)
	}
	if t.closing.Load() {
		return transportErrorf(ErrClosed, "%s failed: %v", operation, ErrClosed)
	}

	select {
	case <-t.exited:
	case <-time.After(time.Second):
	}
	detail := "runtime process exited"
	if err := t.waitErr.Load(); err != nil && *err != nil {
		detail = "runtime process exited: " + (*err).Error()
	}
	if tail := strings.TrimSpace(t.stderr.String()); tail != "" {
		detail += ": " + lastLine(tail)
	}
	return transportErrorf(ErrClosed, "%s failed: %s", operation, detail)
}

func (t *transport) terminalErrorIfDown() error {
	select {
	case <-t.done:
		return t.terminalError("send")
	default:
		return nil
	}
}

func (t *transport) close() error {
	t.closeOnce.Do(func() {
		t.closing.Store(true)
		t.logger.Debug("oap sdk: closing transport")

		closeErr := t.stdin.Close()

		grace := t.shutdownGrace
		if grace <= 0 {
			grace = defaultShutdownGrace
		}
		timer := time.NewTimer(grace)
		defer timer.Stop()

		killed := false
		select {
		case <-t.exited:
		case <-timer.C:
			t.logger.Debug("oap sdk: runtime did not exit in time, killing")
			if t.cmd.Process != nil {
				killed = true
				_ = t.cmd.Process.Kill()
			}
			<-t.exited
		}

		if err := t.waitErr.Load(); err != nil && *err != nil && !killed {
			t.closeErr = transportErrorf(*err, "runtime exited with an error: %v", *err)
			return
		}
		if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.closeErr = transportErrorf(closeErr, "cannot close runtime stdin: %v", closeErr)
		}
	})
	return t.closeErr
}

type subscription struct {
	transport *transport
	kind      routeKind
	id        string
	queue     chan *inbound
	wake      chan struct{}
	overflow  atomic.Bool
	closed    atomic.Bool
}

func (s *subscription) correlate(messageID string) {
	if messageID == "" {
		return
	}
	s.transport.mu.Lock()
	s.transport.correlates[messageID] = s
	s.transport.mu.Unlock()
}

func (s *subscription) uncorrelate(messageID string) {
	if messageID == "" {
		return
	}
	s.transport.mu.Lock()
	if s.transport.correlates[messageID] == s {
		delete(s.transport.correlates, messageID)
	}
	s.transport.mu.Unlock()
}

func (s *subscription) deliver(in *inbound) {
	if s.closed.Load() {
		return
	}
	select {
	case s.queue <- in:
		s.signal()
	default:
		s.overflow.Store(true)
		s.signal()
		s.transport.logger.Error("oap sdk: route queue overflowed, frame dropped",
			"route", s.kind.String(), "id", s.id, "type", in.kind())
	}
}

func (s *subscription) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *subscription) next(ctx context.Context, timeout time.Duration, operation string) (*inbound, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case f := <-s.queue:
			return f, nil
		default:
		}
		if s.overflow.Load() {
			return nil, &StreamError{
				Kind:      KindTransportError,
				Message:   fmt.Sprintf("%s failed: runtime produced frames faster than they could be consumed and some were dropped", operation),
				StreamID:  s.streamID(),
				SessionID: s.sessionID(),
			}
		}

		select {
		case f := <-s.queue:
			return f, nil
		case <-s.wake:
			continue
		case <-ctx.Done():
			return nil, abortError(ctx.Err(), operation)
		case <-timer.C:
			return nil, &StreamError{
				Kind:      KindTransportError,
				Message:   fmt.Sprintf("timed out waiting for %s after %s", operation, timeout),
				StreamID:  s.streamID(),
				SessionID: s.sessionID(),
			}
		case <-s.transport.done:

			select {
			case f := <-s.queue:
				return f, nil
			default:
			}
			return nil, s.transport.terminalError(operation)
		}
	}
}

func (s *subscription) close() {
	if s.closed.Swap(true) {
		return
	}
	s.transport.unsubscribe(s)
}

func (s *subscription) streamID() string {
	if s.kind == routeStream {
		return s.id
	}
	return ""
}

func (s *subscription) sessionID() string {
	if s.kind == routeSession {
		return s.id
	}
	return ""
}

func isPipeClosed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "file already closed")
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

type tailBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
}

func newTailBuffer(size int) *tailBuffer { return &tailBuffer{size: size} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.size {
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-b.size:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
