package rpc

import (
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

func FuzzAFrameIsEitherWithinTheLimitOrRefusedAndNeverAHalfMessage(f *testing.F) {
	for _, seed := range codexSeeds(f) {
		f.Add(4096, seed)
		f.Add(1, seed)
		f.Add(0, seed)
	}
	for _, seed := range []string{"{}\n", "\n", "{\"jsonrpc\":\"2.0\"}\n", "{\"jsonrpc\":\"2.0\"", "no newline", "\r\n", "{}\n{}\n"} {
		f.Add(8, seed)
	}
	f.Fuzz(func(t *testing.T, limit int, body string) {
		decoder := NewDecoder(strings.NewReader(body), limit)
		message, err := decoder.Decode()
		if err != nil {
			if message.Method != "" || message.ID != (RequestID{}) || message.Result != nil || message.Params != nil || message.Error != nil {
				t.Fatalf("a refused frame returned the message %+v: %q", message, body)
			}
			return
		}
		if message.Method == "" && message.ID == (RequestID{}) && message.Result == nil && message.Params == nil && message.Error == nil {
			t.Fatalf("a frame with no request, result or error in it was admitted as a message: %q", body)
		}
	})
}

func FuzzTheWalkRefusesADuplicateKeyInAnyFrameOfAMessage(f *testing.F) {
	for _, seed := range codexSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{`{"jsonrpc":"2.0","method":"a","method":"b"}`, `{"a":1,"a":2}`, `{}`, `[]`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if _, err := ParseMessage([]byte(raw)); err == nil && rejectDuplicateKeys([]byte(raw)) != nil {
			t.Fatalf("the message parse admitted bytes the walk refuses as duplicated: %q", raw)
		}
	})
}

func codexSeeds(f *testing.F) []string {
	{
		bodies, err := fuzzseed.Corpus("codex-app-server", fuzzseed.DefaultLimit)
		if err != nil {
			{
				f.Fatal(err)
			}
		}
		if len(bodies) == 0 {
			{
				f.Fatalf("the catalog's corpus for %s is empty, so this target starts from its own literals only", "codex-app-server")
			}
		}
		seeds := make([]string, 0, len(bodies))
		for _, body := range bodies {
			{
				seeds = append(seeds, string(body))
			}
		}
		return seeds
	}
}
