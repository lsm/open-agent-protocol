package mockprovider

import (
	"net/http"
	"strings"
	"testing"
)

func post(t *testing.T, url, body string) string {
	t.Helper()
	resp, err := http.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	held := make([]byte, 0, 1024)
	chunk := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(chunk)
		held = append(held, chunk[:n]...)
		if err != nil {
			break
		}
	}
	return string(held)
}

func TestTheMockServesItsFramesToAnHTTPCaller(t *testing.T) {
	mock := New(TextTurn("over http")...)
	defer mock.Close()
	got := post(t, mock.URL(), "{}")
	if !strings.Contains(got, `"content":"over http"`) {
		t.Errorf("the body = %q, want the text frame", got)
	}
	if !strings.HasSuffix(got, "data: [DONE]\n\n") {
		t.Errorf("the body = %q, want the done sentinel last and unmarshalled", got)
	}
}

func TestTheMockRefusesWithTheMessageItWasGiven(t *testing.T) {
	mock := New()
	defer mock.Close()
	mock.FailWith = http.StatusTooManyRequests
	mock.FailMessage = "rate limited"
	got := post(t, mock.URL(), "{}")
	if !strings.Contains(got, `"message":"rate limited"`) {
		t.Errorf("the body = %q, want the refusal carrying its message", got)
	}
}

func TestTheMockRecordsTheRouteAndTheBodyItWasSent(t *testing.T) {
	mock := New(TextTurn("x")...)
	defer mock.Close()
	post(t, mock.URL(), `{"model":"gpt-4o"}`)
	if mock.RequestCount() != 1 {
		t.Fatalf("the mock saw %d requests, want 1", mock.RequestCount())
	}
	if mock.Paths[0] != "/v1/chat/completions" {
		t.Errorf("the path = %q, want the completions route", mock.Paths[0])
	}
	if mock.LastBody() != `{"model":"gpt-4o"}` {
		t.Errorf("the recorded body = %q", mock.LastBody())
	}
}

func TestTwoRunsOfOneFixtureSendIdenticalBytes(t *testing.T) {
	const request = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	first := New(TextTurn("same")...)
	defer first.Close()
	second := New(TextTurn("same")...)
	defer second.Close()

	post(t, first.URL(), request)
	post(t, second.URL(), request)

	if first.LastBody() != second.LastBody() {
		t.Errorf("the two runs were sent %q and %q, want identical bytes", first.LastBody(), second.LastBody())
	}
	if first.LastBody() != request {
		t.Errorf("the body = %q, want what was sent", first.LastBody())
	}
	if first.RequestCount() != 1 || second.RequestCount() != 1 {
		t.Errorf("the mocks saw %d and %d requests, want one each", first.RequestCount(), second.RequestCount())
	}
}

func TestATextFixtureSurvivesQuotesAndNewlines(t *testing.T) {
	const awkward = "he said \"stop\"\nand \\ left"
	mock := New(TextTurn(awkward)...)
	defer mock.Close()
	got := post(t, mock.URL(), "{}")
	if !strings.Contains(got, `he said \"stop\"\nand \\ left`) {
		t.Errorf("the body = %q, want the text escaped rather than spliced raw", got)
	}
}

func TestEachTurnIsMadeOfTheFramesItsShapeNeeds(t *testing.T) {
	if got := len(TextTurn("t")); got != 3 {
		t.Errorf("a text turn is %d frames, want 3: the delta, the finish, the sentinel", got)
	}
	if got := len(ToolTurn("call_1", "read", "{}")); got != 4 {
		t.Errorf("a tool turn is %d frames, want 4: the call opens, the arguments arrive, the finish, the sentinel", got)
	}
	if got := len(UsageTurn(1, 1)); got != 3 {
		t.Errorf("a usage turn is %d frames, want 3", got)
	}
}
