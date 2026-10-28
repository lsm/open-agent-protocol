package rpc

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
)

func peer(t *testing.T) (*Client, *Codec, func()) {
	t.Helper()
	a, b := net.Pipe()
	c := NewClient(a, 0, 2)
	return c, NewCodec(b, b, 0), func() { _ = b.Close(); _ = c.Close() }
}

func TestCallKeepsNotificationsAndRefusesReverseRequests(t *testing.T) {
	c, wire, closePeer := peer(t)
	defer closePeer()
	server := make(chan error, 1)
	go func() {
		body, err := wire.Read()
		if err != nil {
			server <- err
			return
		}
		m, err := native.Parse(body)
		if err != nil {
			server <- err
			return
		}
		if err = wire.Write([]byte(`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"ses","event":{"type":"session.idle","data":{}}}}`)); err != nil {
			server <- err
			return
		}
		if err = wire.Write([]byte(`{"jsonrpc":"2.0","id":"reverse-1","method":"gitHubToken.getToken","params":{}}`)); err != nil {
			server <- err
			return
		}
		body, err = wire.Read()
		if err != nil {
			server <- err
			return
		}
		refused, err := native.Parse(body)
		if err != nil {
			server <- err
			return
		}
		if refused.Kind != native.Failure || refused.Code != -32601 {
			server <- ErrFrame
			return
		}
		body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"messageId": "m1"}})
		server <- wire.Write(body)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var result struct {
		MessageID string `json:"messageId"`
	}
	if err := c.Call(ctx, "session.send", map[string]any{"prompt": "synthetic"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.MessageID != "m1" {
		t.Fatalf("reply %+v", result)
	}
	select {
	case note := <-c.Notifications():
		if note.Method != "session.event" {
			t.Fatal(note)
		}
	case <-ctx.Done():
		t.Fatal("notification lost")
	}
	if err := <-server; err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCallsMatchTheirOwnReplies(t *testing.T) {
	c, wire, closePeer := peer(t)
	defer closePeer()
	server := make(chan error, 1)
	go func() {
		var messages []native.Message
		for range 2 {
			body, err := wire.Read()
			if err != nil {
				server <- err
				return
			}
			m, err := native.Parse(body)
			if err != nil {
				server <- err
				return
			}
			messages = append(messages, m)
		}
		for i := 1; i >= 0; i-- {
			m := messages[i]
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": m.Params})
			if err := wire.Write(body); err != nil {
				server <- err
				return
			}
		}
		server <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func() {
			var got string
			err := c.Call(ctx, "synthetic", name, &got)
			if err == nil && got != name {
				err = ErrFrame
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-server; err != nil {
		t.Fatal(err)
	}
}

func TestCancellationUnblocksAStalledWrite(t *testing.T) {
	c, _, closePeer := peer(t)
	defer closePeer()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := c.Call(ctx, "ping", struct{}{}, nil); err != context.DeadlineExceeded {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("client remained open")
	}
}

func TestReplyArrivingBeforeEOFRemainsReadable(t *testing.T) {
	for range 40 {
		func() {
			a, b := net.Pipe()
			c := NewClient(a, 0, 2)
			defer c.Close()
			go func() {
				defer b.Close()
				wire := NewCodec(b, b, 0)
				body, err := wire.Read()
				if err != nil {
					return
				}
				m, err := native.Parse(body)
				if err != nil {
					return
				}
				body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": "last reply"})
				_ = wire.Write(body)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var got string
			if err := c.Call(ctx, "synthetic", nil, &got); err != nil || got != "last reply" {
				t.Fatalf("last reply %q, error %v", got, err)
			}
		}()
	}
}

func TestMalformedInputAndBackpressureCloseTheClient(t *testing.T) {
	for _, bodies := range [][]string{{`{"jsonrpc":"1.0","method":"x"}`}, {`{"jsonrpc":"2.0","id":999,"result":{}}`}, {`{"jsonrpc":"2.0","method":"one"}`, `{"jsonrpc":"2.0","method":"two"}`, `{"jsonrpc":"2.0","method":"three"}`}} {
		func() {
			c, wire, closePeer := peer(t)
			defer closePeer()
			go func() {
				for _, body := range bodies {
					if wire.Write([]byte(body)) != nil {
						return
					}
				}
			}()
			select {
			case <-c.Done():
				if c.Err() == nil {
					t.Fatal("missing failure")
				}
			case <-time.After(time.Second):
				t.Fatal("client accepted invalid input or dropped a notification")
			}
		}()
	}
}
