package opencode

import (
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
)

func TestTwoAdaptersMintDistinctNativeMessageIDs(t *testing.T) {
	var minted []string
	for range 2 {
		adapter, err := New(Config{Endpoint: "http://127.0.0.1:1"})
		if err != nil {
			t.Fatal(err)
		}
		id := adapter.ids.NewID("opencode-message")
		if !native.MessageID(id).Valid() || !strings.HasPrefix(id, "msg_oap") || !strings.HasSuffix(id, "0000000000000001") {
			t.Fatalf("minted %q", id)
		}
		minted = append(minted, id)
	}
	if minted[0] == minted[1] {
		t.Fatalf("two adapters both minted %q, which a long-lived server refuses the second time", minted[0])
	}
}
