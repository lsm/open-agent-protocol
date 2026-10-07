package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestTheOAPPathBuildsAProtocolEnvelopeAndTheLegacyPathAFrame(t *testing.T) {
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
	legacy := &frame{Type: "agent_start", ID: newULID(), Version: 1}
	if legacy.Version == nil {
		t.Fatal("the legacy frame lost its untyped version, which the V1 wire needs")
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || !json.Valid(encoded) {
		t.Fatalf("the legacy frame no longer marshals: %q", encoded)
	}
}

func TestTheEnvelopeTheOAPPathWritesIsTheOneTheProtocolPackageDecodes(t *testing.T) {
	envelope := oapFrame(oapProvider, "inference.create.request", map[string]any{"model_ref": "fixture/other:test"})
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded protocol.Envelope
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("the protocol package cannot read back what the SDK writes: %v", err)
	}
	if decoded.Type != envelope.Type || decoded.ID != envelope.ID || decoded.Profile != envelope.Profile {
		t.Fatalf("the round trip lost the envelope: wrote %+v, read %+v", envelope, decoded)
	}
	if decoded.CapabilityRevision != "" {
		t.Fatalf("a request carries the capability revision %q before the transport stamps it", decoded.CapabilityRevision)
	}
}

func readFrame(t *testing.T, line string) *frame {
	t.Helper()
	f, err := newFrameReader(strings.NewReader(line + "\n")).next()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAFrameOnTheOAPWireBecomesTheEnvelopeTheProtocolPackageDescribes(t *testing.T) {
	f := readFrame(t, `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"env-1","in_reply_to":"env-0","session_id":"s1","run_id":"run-1","capability_revision":"rev-1","sequence":7,"timestamp_ms":42,"turn_id":"turn-1","tool_call_id":"call-1","extensions":{"x.y":{"z":1}},"inference_id":"inf-1","payload":{"capability_revision":"rev-1"}}`)
	envelope, err := asEnvelope(f)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Type != "capabilities.response" || envelope.ID != "env-1" || envelope.InReplyTo != "env-0" || envelope.Version != "0.1" {
		t.Fatalf("the conversion lost the envelope's own members: %+v", envelope)
	}
	if envelope.SessionID != "s1" || envelope.RunID != "run-1" || envelope.CapabilityRevision != "rev-1" {
		t.Fatalf("the conversion lost the addressing members: %+v", envelope)
	}
	if envelope.Sequence == nil || *envelope.Sequence != 7 || envelope.TimestampMS == nil || *envelope.TimestampMS != 42 {
		t.Fatalf("the conversion read the sequence as %v and the time as %v, want 7 and 42", envelope.Sequence, envelope.TimestampMS)
	}
	if envelope.TurnID != "turn-1" || envelope.ToolCallID != "call-1" || string(envelope.Extensions["x.y"]) != `{"z":1}` || string(envelope.Unknown["inference_id"]) != `"inf-1"` {
		t.Fatalf("the conversion lost a member the hand-copied one dropped: %+v", envelope)
	}
	if payload := envelopePayload(envelope); payload.str("capability_revision") != "rev-1" {
		t.Fatalf("the payload is not readable through the envelope: %+v", payload)
	}
}

func TestTheConversionSubstitutesTheOAPVersionForALegacyNumberAndKeepsTheOAPOne(t *testing.T) {
	legacy, _ := asEnvelope(readFrame(t, `{"protocol":"open-agent-protocol","profile":"open-agent-protocol.agent-control-core","type":"ready","version":1}`))
	if legacy.Version != oapVersion || legacy.Type != "ready" {
		t.Fatalf("a legacy frame carrying the number 1 became %+v, want the OAP version %q: the conversion runs on the OAP path, where a numeric version cannot be the wire's", legacy, oapVersion)
	}
	oap, _ := asEnvelope(readFrame(t, `{"protocol":"open-agent-protocol","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","version":"0.1"}`))
	if oap.Version != oapVersion {
		t.Fatalf("the conversion rewrote the OAP version to %q, want %q", oap.Version, oapVersion)
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
	stream := &ProviderStream{oap: true, transport: tr, sub: tr.subscribeStream("s"), oapInferenceID: "inf-1"}
	_ = stream.Close()
	written.mu.Lock()
	line := bytes.TrimSpace(written.lines.Bytes())
	written.mu.Unlock()
	var sent protocol.Envelope
	if err := json.Unmarshal(line, &sent); err != nil {
		t.Fatalf("sent %q: %v", line, err)
	}
	if sent.Type != "inference.cancel.request" || sent.Profile != oapProvider || sent.Version != oapVersion || sent.ID == "" || string(sent.Unknown["inference_id"]) != `"inf-1"` {
		t.Fatalf("sent %s", line)
	}
	if payload := envelopePayload(sent); payload.str("reason") != "caller_closed" {
		t.Fatalf("the cancel carried %s", sent.Payload)
	}
}

func TestAnEnvelopeTheProtocolPackageCannotReadIsAnErrorNotAnEmptyEnvelope(t *testing.T) {
	f := readFrame(t, `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"env-1","in_reply_to":"env-0","sequence":-1,"payload":{}}`)
	if _, err := asEnvelope(f); err == nil {
		t.Fatal("a negative sequence decoded")
	}
	tr := &transport{logger: discardLogger, stdin: &capturedWrites{}, streams: map[string][]*subscription{}, sessions: map[string][]*subscription{}, correlates: map[string]*subscription{}, inferences: map[string]*subscription{}, done: make(chan struct{}), exited: make(chan struct{})}
	sub := tr.subscribeStream("s")
	request := oapFrame(oapAgent, "capabilities.request", map[string]any{})
	request.ID = "env-0"
	sub.correlate("env-0")
	go tr.dispatch(f)
	started := time.Now()
	if _, err := oapRequest(context.Background(), tr, sub, 5*time.Second, request); err == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("a reply the protocol package cannot read answered %v after %v", err, time.Since(started))
	}
}
