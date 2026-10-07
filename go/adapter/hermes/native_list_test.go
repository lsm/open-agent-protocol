package hermes

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
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
	if !closed {
		t.Fatal("the listing left its gateway running")
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
