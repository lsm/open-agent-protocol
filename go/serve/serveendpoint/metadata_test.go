package serveendpoint

import (
	"context"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type recordingAdapter struct {
	*base.Memory
	opens []base.OpenRequest
}

func (a *recordingAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	a.opens = append(a.opens, request)
	return a.Memory.Open(ctx, request)
}

func TestAnOpensMetadataReachesTheAdapter(t *testing.T) {
	adapter := &recordingAdapter{Memory: base.NewMemory(base.Config{})}
	registry := serve.NewRegistry()
	if err := registry.Register("recording", adapter); err != nil {
		t.Fatal(err)
	}
	server, err := New(serve.New(registry, serve.Options{}), Options{Adapter: "recording", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	frame := func(id, payload string) string {
		return `{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.request","id":"` + id + `","payload":` + payload + `}`
	}
	input := frame("o1", `{"session_id":"a","metadata":{"label":"nightly","depth":2}}`) + "\n" + frame("o2", `{"session_id":"b"}`) + "\n" + frame("o3", `{"session_id":"c","metadata":{"n":1e999}}`) + "\n"
	out := &syncBuffer{}
	if err := server.Run(context.Background(), strings.NewReader(input), out); err != nil {
		t.Fatal(err)
	}
	if len(adapter.opens) != 2 {
		t.Fatalf("adapter saw %d opens, want 2: %s", len(adapter.opens), out.String())
	}
	if got := adapter.opens[0].Metadata; got["label"] != "nightly" || got["depth"] != float64(2) || len(got) != 2 {
		t.Fatalf("first open's metadata = %#v, want label nightly and depth 2", got)
	}
	if got := adapter.opens[1].Metadata; got != nil {
		t.Fatalf("second open's metadata = %#v, want none", got)
	}
	if !strings.Contains(out.String(), `"invalid_payload"`) || !strings.Contains(out.String(), `metadata \"n\"`) {
		t.Fatalf("an unrepresentable metadata value was not refused invalid_payload: %s", out.String())
	}
}
