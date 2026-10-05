package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type hmReplayClient struct {
	t        *testing.T
	request  json.RawMessage
	response *rpc.Message
	calls    []string
	done     chan struct{}
}

func (c *hmReplayClient) Call(_ context.Context, method string, params any, result any) error {
	c.calls = append(c.calls, method)
	var recorded struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(c.request, &recorded); err != nil {
		return err
	}
	sent, err := json.Marshal(params)
	if err != nil {
		return err
	}
	var got, want any
	if err := json.Unmarshal(sent, &got); err != nil {
		return err
	}
	if err := json.Unmarshal(recorded.Params, &want); err != nil {
		return err
	}
	if method != recorded.Method || !reflect.DeepEqual(got, want) {
		c.t.Fatalf("native call %s %s, want the recorded %s %s", method, sent, recorded.Method, recorded.Params)
	}
	if c.response.Error != nil {
		return &rpc.RemoteError{ID: c.response.ID, Object: *c.response.Error}
	}
	return json.Unmarshal(c.response.Result, result)
}

func (c *hmReplayClient) Respond(context.Context, *rpc.IncomingRequest, any) error { return nil }
func (c *hmReplayClient) RespondError(context.Context, *rpc.IncomingRequest, int64, string) error {
	return nil
}
func (c *hmReplayClient) Inbound() <-chan rpc.InboundMessage { return nil }
func (c *hmReplayClient) Done() <-chan struct{}              { return c.done }
func (c *hmReplayClient) ReadDone() <-chan struct{}          { return c.done }
func (c *hmReplayClient) Err() error                         { return nil }
func (c *hmReplayClient) Close() error                       { return nil }

type hmReplayFactory struct{ client *hmReplayClient }

func (f hmReplayFactory) Start(context.Context) (Client, string, error) {
	return nil, "", errors.New("a reopen corpus case never creates a session")
}

func (f hmReplayFactory) Reopen(ctx context.Context, stored string) (Client, native.SessionResumeResult, error) {
	resumed, err := resumeStored(ctx, f.client, stored)
	if err != nil {
		return nil, resumed, err
	}
	return f.client, resumed, nil
}

func runHermesReopenCorpus(t *testing.T, expectedFile string, labels []string, frames []hmFrame, decoded []hmDecodedFrame) {
	t.Helper()
	if len(frames) != 3 || decoded[0].Event == nil || decoded[0].Event.Type != native.EventGatewayReady || decoded[2].Message == nil {
		t.Fatalf("a reopen case is gateway.ready, session.resume and its answer; got %d frames", len(frames))
	}
	request := hmWireBytes(t, frames[1].Raw, "session.resume", 2)
	var params struct {
		Params native.SessionResumeParams `json:"params"`
	}
	if err := json.Unmarshal(request, &params); err != nil {
		t.Fatal(err)
	}
	client := &hmReplayClient{t: t, request: request, response: decoded[2].Message, done: make(chan struct{})}
	reopened, resumed, err := reopenBinding(context.Background(), hmReplayFactory{client: client}, params.Params.SessionID)
	for _, label := range labels {
		switch label {
		case "bound-session-reopen":
			if err != nil || reopened == nil || resumed.Resumed != params.Params.SessionID {
				t.Fatalf("reopen of the recorded binding: %v", err)
			}
			projected := &Session{state: protocol.SessionState{SessionID: "session", Status: protocol.SessionIdle}}
			projected.restoreState(resumed)
			expected := hmLoadJSON[protocol.SessionState](t, expectedFile)
			if !reflect.DeepEqual(projected.state, expected) {
				t.Fatalf("state=%+v want=%+v", projected.state, expected)
			}
		case "reopen-auto-continue-refused":
			var refusal *base.UnsupportedControlError
			if !pendingAutoContinue(resumed.AutoContinue) || !errors.As(err, &refusal) || !errors.Is(err, base.ErrUnsupportedInput) {
				t.Fatalf("a recorded auto_continue must refuse the reopen: %v", err)
			}
			expected := hmLoadJSON[struct {
				Feature string `json:"feature"`
				Reason  string `json:"reason"`
			}](t, expectedFile)
			if refusal.Feature != expected.Feature || refusal.Reason != expected.Reason {
				t.Fatalf("refusal=%+v want=%+v", refusal, expected)
			}
		default:
			t.Fatalf("ledger fixture %q has no reopen evidence", label)
		}
	}
	if len(client.calls) != 1 || client.calls[0] != native.MethodSessionResume {
		t.Fatalf("native calls = %v, want exactly the recorded session.resume", client.calls)
	}
}
