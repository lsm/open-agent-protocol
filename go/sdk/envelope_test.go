package sdk

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestTheOAPPathBuildsAProtocolEnvelope(t *testing.T) {
	envelope := oapFrame(oapAgent, "session.open.request", map[string]any{"session_id": "s1"})
	if envelope.Protocol != oapProtocol || envelope.Version != oapVersion || envelope.Profile != oapAgent {
		t.Fatalf("the OAP path built %+v, which is not the envelope the protocol package describes", envelope)
	}
	if envelope.Type != protocol.TypeSessionOpenRequest {
		t.Fatalf("the OAP path built the type %q, so it is naming the wire in its own vocabulary", envelope.Type)
	}
	if envelope.ID == "" || len(envelope.Payload) == 0 {
		t.Fatalf("the OAP path built an envelope with no id or payload: %+v", envelope)
	}
}

func TestTheEnvelopesTheOAPPathWritesAreTheOnesTheProtocolPackageDecodes(t *testing.T) {
	agent := oapFrame(oapAgent, "session.open.request", map[string]any{"session_id": "s1"})
	decoded, err := protocol.ParseEnvelope(mustMarshal(agent))
	if err != nil {
		t.Fatalf("the protocol package cannot read back the agent envelope the SDK writes: %v", err)
	}
	if decoded.Type != agent.Type || decoded.ID != agent.ID || decoded.Profile != oapAgent || decoded.CapabilityRevision != "" {
		t.Fatalf("the agent round trip lost the envelope: wrote %+v, read %+v", agent, decoded)
	}
	provider := providerFrame("inference.create.request", map[string]any{"model_ref": "fixture/other:test"})
	read, err := protocol.ParseProviderEnvelope(mustMarshal(provider))
	if err != nil {
		t.Fatalf("the protocol package cannot read back the provider envelope the SDK writes: %v", err)
	}
	if read.Type != provider.Type || read.ID != provider.ID || read.Profile != protocol.ProviderProfile || read.Version != oapVersion {
		t.Fatalf("the provider round trip lost the envelope: wrote %+v, read %+v", provider, read)
	}
}

func readLine(t *testing.T, line string) *inbound {
	t.Helper()
	in, err := newFrameReader(strings.NewReader(line + "\n")).nextInbound()
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestAnAgentLineOnTheOAPWireIsReadAsTheProtocolPackagesEnvelope(t *testing.T) {
	in := readLine(t, `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"env-1","in_reply_to":"env-0","session_id":"s1","run_id":"run-1","capability_revision":"rev-1","sequence":7,"timestamp_ms":42,"turn_id":"turn-1","tool_call_id":"call-1","extensions":{"x.y":{"z":1}},"future":true,"payload":{"capability_revision":"rev-1"}}`)
	if in.agent == nil || in.provider != nil || in.broken != nil {
		t.Fatalf("an agent line was read as %+v", in)
	}
	envelope := in.agent
	if envelope.Type != "capabilities.response" || envelope.ID != "env-1" || envelope.InReplyTo != "env-0" || envelope.Version != "0.1" {
		t.Fatalf("the read lost the envelope's own members: %+v", envelope)
	}
	if envelope.SessionID != "s1" || envelope.RunID != "run-1" || envelope.CapabilityRevision != "rev-1" {
		t.Fatalf("the read lost the addressing members: %+v", envelope)
	}
	if envelope.Sequence == nil || *envelope.Sequence != 7 || envelope.TimestampMS == nil || *envelope.TimestampMS != 42 {
		t.Fatalf("the read took the sequence as %v and the time as %v, want 7 and 42", envelope.Sequence, envelope.TimestampMS)
	}
	if envelope.TurnID != "turn-1" || envelope.ToolCallID != "call-1" || string(envelope.Extensions["x.y"]) != `{"z":1}` || string(envelope.Unknown["future"]) != "true" {
		t.Fatalf("the read lost a member: %+v", envelope)
	}
	if in.kind() != "capabilities.response" || in.replyTo() != "env-0" || in.session() != "s1" || in.run() != "run-1" || in.sequence() != 7 || in.body().str("capability_revision") != "rev-1" {
		t.Fatalf("the routing read disagrees with the envelope: %+v", in)
	}
}

func TestAProviderLineOnTheOAPWireIsReadAsTheProtocolPackagesProviderEnvelope(t *testing.T) {
	in := readLine(t, `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.delta","id":"e2","inference_id":"inf-1","sequence":3,"payload":{"part_index":0,"delta":"hi"}}`)
	if in.provider == nil || in.agent != nil || in.broken != nil {
		t.Fatalf("a provider line was read as %+v", in)
	}
	if in.provider.InferenceID != "inf-1" || in.provider.Sequence == nil || *in.provider.Sequence != 3 || in.provider.Type != "inference.part.delta" {
		t.Fatalf("the read lost the provider members: %+v", in.provider)
	}
	if in.inference() != "inf-1" || in.sequence() != 3 || in.body().str("delta") != "hi" || in.session() != "" {
		t.Fatalf("the routing read disagrees with the envelope: %+v", in)
	}
}

func TestAnOAPLineTheProtocolPackageCannotReadIsBrokenButStillRoutable(t *testing.T) {
	in := readLine(t, `{"protocol":"open-agent-protocol","profile":"open-agent-protocol.agent-control-core","type":"ready","in_reply_to":"env-0","version":1,"payload":{}}`)
	if in.broken == nil || in.agent != nil {
		t.Fatalf("a numeric version on the OAP wire was read as %+v, want a broken line", in)
	}
	if in.kind() != "ready" || in.replyTo() != "env-0" {
		t.Fatalf("a broken line lost its routing: %+v", in.header)
	}
	if !errors.Is(in.failure(""), in.broken) {
		t.Fatalf("a broken line reports %v, not why it broke", in.failure(""))
	}
	if _, err := newFrameReader(strings.NewReader("[1]\n")).nextInbound(); !errors.Is(err, errMalformedFrame) {
		t.Fatalf("a line that is not an object answered %v", err)
	}
}

type capturedWrites struct {
	mu    sync.Mutex
	lines bytes.Buffer
}

func (c *capturedWrites) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lines.Write(p)
}

func (c *capturedWrites) Close() error { return nil }

func TestClosingAnOAPStreamEarlyCancelsItsInferenceByID(t *testing.T) {
	written := &capturedWrites{}
	tr := &transport{
		logger: discardLogger, stdin: written,
		streams: map[string][]*subscription{}, sessions: map[string][]*subscription{}, correlates: map[string]*subscription{},
		inferences: map[string]*subscription{}, done: make(chan struct{}), exited: make(chan struct{}),
	}
	stream := &ProviderStream{transport: tr, sub: tr.subscribeStream("s"), oapInferenceID: "inf-1"}
	_ = stream.Close()
	written.mu.Lock()
	line := bytes.TrimSpace(written.lines.Bytes())
	written.mu.Unlock()
	sent, err := protocol.ParseProviderEnvelope(line)
	if err != nil {
		t.Fatalf("sent %q: %v", line, err)
	}
	if sent.Type != "inference.cancel.request" || sent.Profile != oapProvider || sent.Version != oapVersion || sent.ID == "" || sent.InferenceID != "inf-1" {
		t.Fatalf("sent %s", line)
	}
	if payload := payloadObject(sent.Payload); payload.str("reason") != "caller_closed" {
		t.Fatalf("the cancel carried %s", sent.Payload)
	}
}

func TestAnEnvelopeTheProtocolPackageCannotReadIsAnErrorNotAnEmptyEnvelope(t *testing.T) {
	f := readLine(t, `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"env-1","in_reply_to":"env-0","sequence":-1,"payload":{}}`)
	if f.broken == nil {
		t.Fatal("a negative sequence decoded")
	}
	tr := &transport{logger: discardLogger, stdin: &capturedWrites{}, streams: map[string][]*subscription{}, sessions: map[string][]*subscription{}, correlates: map[string]*subscription{}, inferences: map[string]*subscription{}, done: make(chan struct{}), exited: make(chan struct{})}
	sub := tr.subscribeStream("s")
	request := oapFrame(oapAgent, "capabilities.request", map[string]any{})
	request.ID = "env-0"
	sub.correlate("env-0")
	go tr.dispatch(f)
	started := time.Now()
	if _, err := oapRequest(context.Background(), tr, sub, 5*time.Second, request); err == nil || !strings.Contains(err.Error(), "malformed OAP envelope capabilities.response") || time.Since(started) > 2*time.Second {
		t.Fatalf("a reply the protocol package cannot read answered %v after %v", err, time.Since(started))
	}
}

func TestClosingAnAgentStreamBeforeItSettlesCancelsTheRun(t *testing.T) {
	written := &capturedWrites{}
	tr := &transport{
		logger: discardLogger, stdin: written,
		streams: map[string][]*subscription{}, sessions: map[string][]*subscription{}, correlates: map[string]*subscription{},
		inferences: map[string]*subscription{}, done: make(chan struct{}), exited: make(chan struct{}),
	}
	stream := &AgentStream{state: &oapAgentState{transport: tr, sub: tr.subscribeSession("session-1"), sessionID: "session-1", runID: "run-1"}}
	_ = stream.Close()
	written.mu.Lock()
	line := bytes.TrimSpace(written.lines.Bytes())
	written.mu.Unlock()
	sent, err := protocol.ParseEnvelope(line)
	if err != nil {
		t.Fatalf("sent %q: %v", line, err)
	}
	if sent.Type != "run.cancel.request" || sent.SessionID != "session-1" || sent.RunID != "run-1" {
		t.Fatalf("sent %s", line)
	}
	if payload := payloadObject(sent.Payload); payload.str("reason") != "caller_closed" || payload.str("run_id") != "run-1" {
		t.Fatalf("the cancel carried %s", sent.Payload)
	}
}

func TestClosingASettledAgentStreamSendsNothing(t *testing.T) {
	written := &capturedWrites{}
	tr := &transport{
		logger: discardLogger, stdin: written,
		streams: map[string][]*subscription{}, sessions: map[string][]*subscription{}, correlates: map[string]*subscription{},
		inferences: map[string]*subscription{}, done: make(chan struct{}), exited: make(chan struct{}),
	}
	stream := &AgentStream{state: &oapAgentState{transport: tr, sub: tr.subscribeSession("session-1"), sessionID: "session-1", runID: "run-1", settled: true}}
	_ = stream.Close()
	written.mu.Lock()
	defer written.mu.Unlock()
	if written.lines.Len() != 0 {
		t.Fatalf("a settled run was cancelled: %s", written.lines.Bytes())
	}
}
