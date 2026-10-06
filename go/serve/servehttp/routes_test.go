package servehttp

import (
	"net/http"
	"strings"
	"testing"
)

func TestTheRouteTableIsComplete(t *testing.T) {
	server := newMemoryServer(t, 8)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/adapters"},
		{http.MethodGet, "/adapters/memory/capabilities"},
		{http.MethodPost, "/adapters/memory/sessions"},
		{http.MethodGet, "/sessions"},
		{http.MethodGet, "/sessions/history"},
		{http.MethodGet, "/sessions/absent/state"},
		{http.MethodGet, "/sessions/absent/tools"},
		{http.MethodGet, "/sessions/absent/models"},
		{http.MethodPost, "/sessions/absent/submit"},
		{http.MethodPost, "/sessions/absent/resolve"},
		{http.MethodPost, "/sessions/absent/cancel"},
		{http.MethodPost, "/sessions/absent/settings"},
		{http.MethodPost, "/sessions/absent/close"},
		{http.MethodGet, "/sessions/absent/events"},
	}
	send := func(t *testing.T, method, path string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(method, server.URL+path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response
	}
	for _, route := range routes {
		response := send(t, route.method, route.path)
		if strings.HasPrefix(response.Header.Get("Content-Type"), "text/plain") {
			t.Fatalf("%s %s answered the mux's %s, so no route serves it", route.method, route.path, response.Status)
		}
		wrong := http.MethodPost
		if route.method == http.MethodPost {
			wrong = http.MethodGet
		}
		refused := send(t, wrong, route.path)
		if refused.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(refused.Header.Get("Allow"), route.method) {
			t.Fatalf("%s %s answered %s with Allow %q, want 405 naming %s", wrong, route.path, refused.Status, refused.Header.Get("Allow"), route.method)
		}
	}
	if unrouted := send(t, http.MethodGet, "/sessions/absent/nothing"); unrouted.StatusCode != http.StatusNotFound || !strings.HasPrefix(unrouted.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("an unrouted path answered %s %q, want the mux's plain 404", unrouted.Status, unrouted.Header.Get("Content-Type"))
	}
}
