package hermes

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
)

func TestTheNativeListAsksOneGatewayForItsStoredSessionsTitledByTitleOrPreview(t *testing.T) {
	f := newFake()
	f.queue(methodSessionList, reply{result: map[string]any{"sessions": []any{
		map[string]any{"id": "stored-a", "title": "Named", "preview": "ignored", "started_at": 1774717612.4789, "message_count": 3, "source": "cli"},
		7,
		map[string]any{"title": "no id"},
		map[string]any{"id": "stored-b", "title": "", "preview": "first words", "started_at": 1774717000},
		map[string]any{"id": "stored-c", "preview": "past the limit"},
	}}})
	var launches atomic.Int32
	listed, err := processBackedAdapter(t, f, &launches).NativeList(context.Background(), base.NativeListRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0] != (base.NativeListing{NativeID: "stored-a", Title: "Named", UpdatedAtMS: 1774717612478}) || listed[1] != (base.NativeListing{NativeID: "stored-b", Title: "first words", UpdatedAtMS: 1774717000000}) {
		t.Fatalf("listed %+v", listed)
	}
	if launches.Load() != 1 || len(f.calls) != 1 || f.calls[0].params != (sessionListParams{Limit: 2}) {
		t.Fatalf("%d launches, calls %+v", launches.Load(), f.calls)
	}
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		t.Fatal("the listing closed the gateway it keeps for the next one")
	}
}

func TestTheListingsShareOneGatewayAndStartAnotherOnlyWhenItBreaks(t *testing.T) {
	sessions := reply{result: map[string]any{"sessions": []any{map[string]any{"id": "stored-a"}}}}
	var gateways []*fakeClient
	add := func(replies ...reply) *fakeClient {
		gateway := newFake()
		for _, r := range replies {
			gateway.queue(methodSessionList, r)
		}
		gateways = append(gateways, gateway)
		return gateway
	}
	kept := add(sessions, sessions, reply{err: &rpc.RemoteError{Object: rpc.ErrorObject{Code: -32000, Message: "busy"}}}, reply{err: errors.New("pipe closed")})
	var launches atomic.Int32
	implementation, err := New(Config{Model: "hermes-test", Clock: &testClock{}, IDs: &testIDs{}, ProcessFactory: ProcessFactoryFunc(func(context.Context, rpc.ProcessConfig) (ProcessBridge, error) {
		at := int(launches.Add(1)) - 1
		return fakeBridge{client: gateways[min(at, len(gateways)-1)]}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	list := func() error {
		_, err := implementation.NativeList(context.Background(), base.NativeListRequest{Limit: 1})
		return err
	}
	closed := func(gateway *fakeClient) bool {
		gateway.mu.Lock()
		defer gateway.mu.Unlock()
		return gateway.closed
	}
	if list() != nil || list() != nil || launches.Load() != 1 || closed(kept) {
		t.Fatalf("two listings launched %d gateways", launches.Load())
	}
	if list() == nil || launches.Load() != 1 || closed(kept) {
		t.Fatalf("an error the gateway answered dropped it: %d launches", launches.Load())
	}
	fresh := add(sessions)
	if err := list(); err != nil || launches.Load() != 2 || !closed(kept) {
		t.Fatalf("a broken reused gateway answered %v after %d launches", err, launches.Load())
	}
	_ = fresh.Close()
	third := add(reply{err: errors.New("died at once")}, sessions)
	if list() == nil || launches.Load() != 3 || len(fresh.calls) != 1 || !closed(third) {
		t.Fatalf("an exited gateway was used again, or one that broke at once was retried: %d launches", launches.Load())
	}
	spare := add(sessions)
	lister := implementation.config.Factory.(processClientFactory).lister
	lister.mu.Lock()
	err = list()
	lister.mu.Unlock()
	if err != nil || launches.Load() != 4 || !closed(spare) {
		t.Fatalf("a listing while the kept gateway was busy did not run on its own: %v, %d launches", err, launches.Load())
	}
}

func TestTheNativeListFailsWhenTheGatewayFailsAndListsNothingForAFactoryThatCannotList(t *testing.T) {
	f := newFake()
	f.queue(methodSessionList, reply{err: errors.New("gateway gone")})
	var launches atomic.Int32
	if _, err := processBackedAdapter(t, f, &launches).NativeList(context.Background(), base.NativeListRequest{Limit: 5}); err == nil {
		t.Fatal("a failed session.list answered no error")
	}
	scripted, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return nil, "", errors.New("unused") })})
	if err != nil {
		t.Fatal(err)
	}
	if listed, err := scripted.NativeList(context.Background(), base.NativeListRequest{Limit: 5}); err != nil || listed != nil {
		t.Fatalf("a factory without a list answered %+v, %v", listed, err)
	}
}

func TestStartedAtReadsSecondsAsMillisecondsSaturatingAnIntegerAndRefusingAnUnboundedFloat(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want int64
	}{
		{"", 0},
		{"null", 0},
		{`"1"`, 0},
		{"12", 12000},
		{"1.5", 1500},
		{"-1.5", -1500},
		{"9223372036854775807", math.MaxInt64},
		{"-9223372036854775807", math.MinInt64},
		{"1e15", 0},
		{"1e3", 1000000},
	} {
		if got := startedMillis([]byte(c.raw)); got != c.want {
			t.Fatalf("startedMillis(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestTheKeptGatewayReleasesEachBarrierItsClientQueues(t *testing.T) {
	f := newFake()
	f.queue(methodSessionList, reply{result: map[string]any{"sessions": []any{}}})
	barrier := make(chan struct{})
	f.in <- rpc.InboundMessage{Barrier: barrier}
	var launches atomic.Int32
	if _, err := processBackedAdapter(t, f, &launches).NativeList(context.Background(), base.NativeListRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-barrier:
	case <-time.After(time.Second):
		t.Fatal("the kept gateway's barrier was never released")
	}
	_ = f.Close()
}

type cancelShutClient struct{ *fakeClient }

func (c cancelShutClient) Call(ctx context.Context, method string, params, result any) error {
	if err := ctx.Err(); err != nil {
		_ = c.fakeClient.Close()
		return err
	}
	return c.fakeClient.Call(ctx, method, params, result)
}

type cancelShutBridge struct{ client cancelShutClient }

func (b cancelShutBridge) ClientHandle() Client        { return b.client }
func (b cancelShutBridge) Done() <-chan struct{}       { return b.client.done }
func (b cancelShutBridge) WaitError() error            { return nil }
func (b cancelShutBridge) Close(context.Context) error { return b.client.Close() }

func TestAListingWhoseCallerGivesUpLeavesTheKeptGatewayRunning(t *testing.T) {
	kept := newFake()
	for range 3 {
		kept.queue(methodSessionList, reply{result: map[string]any{"sessions": []any{}}})
	}
	var launches atomic.Int32
	implementation, err := New(Config{Model: "hermes-test", Clock: &testClock{}, IDs: &testIDs{}, ProcessFactory: ProcessFactoryFunc(func(context.Context, rpc.ProcessConfig) (ProcessBridge, error) {
		launches.Add(1)
		return cancelShutBridge{client: cancelShutClient{kept}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{context.Background(), gone, context.Background()} {
		if _, err := implementation.NativeList(ctx, base.NativeListRequest{Limit: 1}); err != nil {
			t.Fatal(err)
		}
	}
	kept.mu.Lock()
	closed := kept.closed
	kept.mu.Unlock()
	if closed || launches.Load() != 1 {
		t.Fatalf("a caller that had given up shut the kept gateway down: closed=%v, %d launches", closed, launches.Load())
	}
}

func TestAKeptGatewayThatNeverGetsReadyGivesUpAtTheListingsDeadline(t *testing.T) {
	implementation, err := New(Config{Model: "hermes-test", Clock: &testClock{}, IDs: &testIDs{}, ProcessFactory: ProcessFactoryFunc(func(ctx context.Context, _ rpc.ProcessConfig) (ProcessBridge, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})})
	if err != nil {
		t.Fatal(err)
	}
	answered := make(chan error, 2)
	go func() {
		for range 2 {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			_, err := implementation.NativeList(ctx, base.NativeListRequest{Limit: 1})
			cancel()
			answered <- err
		}
	}()
	for range 2 {
		select {
		case err := <-answered:
			if err == nil {
				t.Fatal("a gateway that never got ready listed")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a launch that never got ready held the kept gateway past the listing's deadline")
		}
	}
}

func TestClosingTheAdapterStopsItsKeptGateway(t *testing.T) {
	f := newFake()
	f.queue(methodSessionList, reply{result: map[string]any{"sessions": []any{}}})
	var launches atomic.Int32
	implementation := processBackedAdapter(t, f, &launches)
	if _, err := implementation.NativeList(context.Background(), base.NativeListRequest{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if err := implementation.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if !closed {
		t.Fatal("closing the adapter left its listing gateway running")
	}
	if err := implementation.Close(context.Background()); err != nil {
		t.Fatalf("a second close: %v", err)
	}
}
