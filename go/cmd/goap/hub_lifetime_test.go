package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const stdioSubscriptionCeiling = 64

func (c *child) answer(id int64) responseShape {
	c.t.Helper()
	if shape, ok := c.held[id]; ok {
		delete(c.held, id)
		return shape
	}
	for {
		line, ok := c.next()
		if !ok {
			c.t.Fatalf("stdout ended before the response to %d; stderr:\n%s", id, c.stderr)
		}
		shape := c.decode(line)
		if shape.Event != "" {
			c.heldSignals = append(c.heldSignals, line)
			continue
		}
		if shape.ID == id {
			return shape
		}
		c.held[shape.ID] = shape
	}
}

func cancelARunOnItsSubscription(t *testing.T, c *child, submit, subscription int64) protocol.RunID {
	t.Helper()
	c.send(`{"id":%d,"op":"submit","session_id":"lifetime","request":%s}`, submit,
		envelopeJSON(t, fmt.Sprintf("lifetime-submit-%d", submit), protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
			SessionID: "lifetime", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
		}, "lifetime", ""))
	c.response(submit)
	var run protocol.RunID
	for {
		var signal signalShape
		if err := json.Unmarshal([]byte(c.signal()), &signal); err != nil {
			t.Fatal(err)
		}
		if signal.Event != "envelope" || signal.ID != subscription {
			t.Fatalf("signal %q on subscription %d, want an envelope on %d", signal.Event, signal.ID, subscription)
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(signal.Envelope, &envelope); err != nil {
			t.Fatal(err)
		}
		if run == "" {
			run = envelope.RunID
			if *envelope.Sequence != 1 {
				t.Fatalf("run %s began at #%d, want #1", run, *envelope.Sequence)
			}
			c.send(`{"id":%d,"op":"cancel","session_id":"lifetime","request":%s}`, submit*100,
				envelopeJSON(t, fmt.Sprintf("lifetime-cancel-%d", submit), protocol.TypeRunCancelRequest,
					protocol.RunCancelRequest{SessionID: "lifetime", RunID: run}, "lifetime", string(run)))
		}
		if envelope.Type == protocol.TypeRunCancelled {
			c.response(submit * 100)
			return run
		}
	}
}

func driveSubscriptionLifetime(t *testing.T, c *child) {
	t.Helper()
	c.send(`{"id":1,"op":"open","adapter":"memory","request":%s}`,
		envelopeJSON(t, "lifetime-open", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{SessionID: "lifetime"}, "", ""))
	c.response(1)
	c.send(`{"id":2,"op":"events","session_id":"lifetime"}`)
	c.response(2)

	first := cancelARunOnItsSubscription(t, c, 3, 2)
	second := cancelARunOnItsSubscription(t, c, 4, 2)
	if first == second {
		t.Fatalf("both runs were %s", first)
	}

	c.send(`{"id":5,"op":"unsubscribe","subscription":2}`)
	if ack := c.response(5); string(ack.Result) != "null" {
		t.Fatalf("unsubscribe answered %s, want null", ack.Result)
	}

	for id := int64(1000); id < 1000+stdioSubscriptionCeiling; id++ {
		c.send(`{"id":%d,"op":"events","session_id":"lifetime"}`, id)
		c.response(id)
	}
	c.send(`{"id":6,"op":"events","session_id":"lifetime"}`)
	if refused := c.answer(6); refused.OK || refused.Error.Code != "busy" {
		t.Fatalf("a subscription past the ceiling answered %s", refused.raw)
	}
	c.send(`{"id":7,"op":"unsubscribe","subscription":1000}`)
	c.response(7)
	c.send(`{"id":8,"op":"events","session_id":"lifetime"}`)
	c.response(8)
}

func TestHubStdioHoldsASubscriptionAcrossRunsUntilItsHostEndsIt(t *testing.T) {
	binaries := map[string]string{"goap": oapBinary(t)}
	for name, variable := range map[string]string{"oapx": "OAP_OAPX_BIN", "rust": "OAP_RUST_HUB_BIN"} {
		binary := os.Getenv(variable)
		if binary == "" {
			continue
		}
		if !filepath.IsAbs(binary) {
			t.Fatalf("%s must be absolute, got %q", variable, binary)
		}
		binaries[name] = binary
	}
	for name, binary := range binaries {
		t.Run(name, func(t *testing.T) {
			driveSubscriptionLifetime(t, spawnBinary(t, binary, "--session-history="))
		})
	}
}
