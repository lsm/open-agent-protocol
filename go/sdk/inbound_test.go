package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func wiredTransport(written *capturedWrites) *transport {
	return &transport{
		logger: discardLogger, stdin: written,
		streams: map[string][]*subscription{}, sessions: map[string][]*subscription{}, correlates: map[string]*subscription{},
		inferences: map[string]*subscription{}, authFlows: map[string]*subscription{}, done: make(chan struct{}), exited: make(chan struct{}),
	}
}

func waitSent(written *capturedWrites, count int) ([]map[string]any, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		written.mu.Lock()
		raw := bytes.Split(bytes.TrimSpace(written.lines.Bytes()), []byte("\n"))
		written.mu.Unlock()
		if len(raw) >= count && len(raw[0]) > 0 {
			var lines []map[string]any
			for _, line := range raw {
				var decoded map[string]any
				if err := json.Unmarshal(line, &decoded); err != nil {
					return nil, fmt.Errorf("sent %q: %w", line, err)
				}
				lines = append(lines, decoded)
			}
			return lines, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the SDK wrote %d lines, want %d", len(raw), count)
		}
		time.Sleep(time.Millisecond)
	}
}

func sentLines(t *testing.T, written *capturedWrites, count int) []map[string]any {
	t.Helper()
	lines, err := waitSent(written, count)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func answerOnceSent(t *testing.T, tr *transport, written *capturedWrites, answer func(request map[string]any) []string) {
	go func() {
		sent, err := waitSent(written, 1)
		if err != nil {
			t.Error(err)
			return
		}
		for _, line := range answer(sent[0]) {
			in, err := decodeInbound([]byte(line), false)
			if err != nil {
				t.Error(err)
				return
			}
			tr.dispatch(in)
		}
	}()
}

func deliverLine(t *testing.T, tr *transport, line string) {
	t.Helper()
	in, err := decodeInbound([]byte(line), false)
	if err != nil {
		t.Fatal(err)
	}
	tr.dispatch(in)
}

func TestAnInferenceTheRuntimeAcceptedIsCancelledByTheIDItsAcceptanceNamed(t *testing.T) {
	written := &capturedWrites{}
	tr := wiredTransport(written)
	stream, err := (&ProviderService{transport: tr, timeout: 2 * time.Second}).oapStream(context.Background(), CompletionRequest{ModelRef: "fixture/other:test@m", Messages: []Message{{Role: RoleUser, Parts: []ContentPart{{Type: PartText, Text: "hi"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	request := sentLines(t, written, 1)[0]
	deliverLine(t, tr, `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.create.response","id":"r1","in_reply_to":"`+request["id"].(string)+`","inference_id":"inf-9","payload":{"accepted":true}}`)
	deliverLine(t, tr, `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.started","id":"r2","inference_id":"inf-9","sequence":1,"payload":{"model_ref":"fixture/other:test@m"}}`)
	if !stream.Next() {
		t.Fatalf("the stream ended before it started: %v", stream.Err())
	}
	_ = stream.Close()
	cancel := sentLines(t, written, 2)[1]
	if cancel["type"] != "inference.cancel.request" || cancel["inference_id"] != "inf-9" || cancel["profile"] != protocol.ProviderProfile {
		t.Fatalf("closing early sent %v, want a cancel naming inf-9", cancel)
	}
}

func TestAProviderRequestAnsweredOutsideTheProviderProfileIsAProtocolError(t *testing.T) {
	written := &capturedWrites{}
	tr := wiredTransport(written)
	request := providerFrame("provider.models.list.request", map[string]any{})
	sub := tr.subscribeStream(string(request.ID))
	answerOnceSent(t, tr, written, func(map[string]any) []string {
		return []string{`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"provider.models.list.response","id":"r1","in_reply_to":"` + string(request.ID) + `","payload":{"models":[]}}`}
	})
	_, err := providerRequest(context.Background(), tr, sub, 2*time.Second, request)
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || !strings.Contains(protocolErr.Message, "outside the provider profile") {
		t.Fatalf("an agent-profile answer to a provider request answered %v", err)
	}
}

func TestAnAuthFlowEventOutOfSequenceEndsTheLoginAsAProtocolViolation(t *testing.T) {
	written := &capturedWrites{}
	tr := wiredTransport(written)
	answerOnceSent(t, tr, written, func(start map[string]any) []string {
		return []string{
			`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.response","id":"r1","in_reply_to":"` + start["id"].(string) + `","payload":{"flow_id":"F1"}}`,
			`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.event","id":"r2","sequence":2,"payload":{"flow_id":"F1","provider_id":"P1","kind":"progress","message":"working"}}`,
		}
	})
	err := (&AuthService{transport: tr, timeout: 2 * time.Second}).oapLogin(context.Background(), "P1", LoginHandlers{})
	var authErr *AuthError
	if !errors.As(err, &authErr) || authErr.Code != "protocol_violation" || authErr.Message != "auth flow sequence gap" {
		t.Fatalf("a flow that opened at sequence 2 answered %v", err)
	}
}

func TestAHandshakeAnswerTheProtocolPackageCannotReadFailsAsMalformed(t *testing.T) {
	tr := wiredTransport(&capturedWrites{})
	handshake := make(chan *inbound, 1)
	in, err := decodeInbound([]byte(`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"r1","sequence":-1,"payload":{"protocol_version":"0.1"}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	handshake <- in
	err = tr.awaitHandshake(context.Background(), handshake, &Options{})
	if err == nil || !strings.Contains(err.Error(), "malformed OAP envelope protocol.initialize.response") {
		t.Fatalf("an unreadable handshake answer gave %v", err)
	}
}

func TestAnAgentRequestAnsweredOutsideTheAgentProfileIsAProtocolError(t *testing.T) {
	written := &capturedWrites{}
	tr := wiredTransport(written)
	request := oapFrame(oapAgent, "auth.providers.request", map[string]any{})
	sub := tr.subscribeStream(string(request.ID))
	answerOnceSent(t, tr, written, func(map[string]any) []string {
		return []string{`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"auth.providers.response","id":"r1","in_reply_to":"` + string(request.ID) + `","payload":{"providers":[]}}`}
	})
	_, err := oapRequest(context.Background(), tr, sub, 2*time.Second, request)
	var protocolErr *ProtocolError
	if !errors.As(err, &protocolErr) || !strings.Contains(protocolErr.Message, "outside the agent profile") {
		t.Fatalf("a provider-profile answer to an agent request answered %v", err)
	}
}

func TestAnOAPLineOutsideBothProfilesIsBrokenAndStillRoutable(t *testing.T) {
	for name, line := range map[string]string{
		"a misspelled profile": `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider","type":"inference.part.delta","id":"e1","in_reply_to":"r0","inference_id":"inf-1","sequence":1,"payload":{}}`,
		"no profile":           `{"protocol":"open-agent-protocol","version":"0.1","type":"inference.part.delta","id":"e1","in_reply_to":"r0","inference_id":"inf-1","sequence":1,"payload":{}}`,
		"another protocol":     `{"protocol":"other","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.delta","id":"e1","in_reply_to":"r0","inference_id":"inf-1","sequence":1,"payload":{}}`,
		"another version":      `{"protocol":"open-agent-protocol","version":"0.2","profile":"open-agent-protocol.model-provider-core","type":"inference.part.delta","id":"e1","in_reply_to":"r0","inference_id":"inf-1","sequence":1,"payload":{}}`,
	} {
		in := readLine(t, line)
		if in.broken == nil || in.agent != nil || in.provider != nil {
			t.Fatalf("%s was read as %+v, want a broken line", name, in)
		}
		if in.inference() != "inf-1" || in.replyTo() != "r0" || !strings.Contains(in.broken.Error(), "not an OAP 0.1 profile") {
			t.Fatalf("%s lost its routing or its reason: %+v, %v", name, in.header, in.broken)
		}
	}
}

func TestAnAuthFlowEventTheProtocolPackageCannotReadFailsTheLoginNamingWhy(t *testing.T) {
	written := &capturedWrites{}
	tr := wiredTransport(written)
	answerOnceSent(t, tr, written, func(start map[string]any) []string {
		return []string{
			`{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.response","id":"r1","in_reply_to":"` + start["id"].(string) + `","payload":{"flow_id":"F1"}}`,
			`{"protocol":"open-agent-protocol","version":1,"profile":"open-agent-protocol.agent-control-core","type":"auth.login.event","id":"r2","sequence":1,"payload":{"flow_id":"F1","provider_id":"P1","kind":"progress","message":"working"}}`,
		}
	})
	started := time.Now()
	err := (&AuthService{transport: tr, timeout: 5 * time.Second}).oapLogin(context.Background(), "P1", LoginHandlers{})
	if err == nil || !strings.Contains(err.Error(), "malformed OAP envelope auth.login.event") || time.Since(started) > 2*time.Second {
		t.Fatalf("an unreadable flow event answered %v after %v, want the malformed envelope at once", err, time.Since(started))
	}
}
