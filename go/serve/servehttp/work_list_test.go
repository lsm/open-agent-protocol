package servehttp

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func workPieces(t *testing.T, url string) int {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	var listed struct {
		Groups []struct {
			Work []json.RawMessage `json:"work"`
		} `json:"groups"`
	}
	if response.StatusCode != http.StatusOK || json.Unmarshal(body, &listed) != nil {
		t.Fatalf("GET %s answered %d %s", url, response.StatusCode, body)
	}
	pieces := 0
	for _, group := range listed.Groups {
		pieces += len(group.Work)
	}
	return pieces
}

func TestTheWorkListRouteHandsItsSearchTermToTheList(t *testing.T) {
	server := newMemoryServer(t, 64)
	if response, body := post(t, server, "/adapters/memory/work", "application/json", []byte(`{"message":"apple pie"}`)); response.StatusCode != http.StatusOK {
		t.Fatalf("start answered %d %s", response.StatusCode, body)
	}
	if pieces := workPieces(t, server.URL+"/work"); pieces != 1 {
		t.Fatalf("the plain list held %d pieces", pieces)
	}
	if pieces := workPieces(t, server.URL+"/work?search=apple"); pieces != 0 {
		t.Fatalf("a search over adapters whose lists cannot search answered %d pieces", pieces)
	}
}
