package servehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestAReopenDirectoryConflictIsABadRequest(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("broken", base.NewMemory(base.Config{})); err != nil {
		t.Fatal(err)
	}
	registry.SetWorkingDirectory("broken", t.TempDir())
	store, err := binding.File(filepath.Join(t.TempDir(), "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{Bindings: store})
	entry, _, err := hub.Open(context.Background(), "broken", base.OpenRequest{SessionID: "kept"})
	if err != nil {
		t.Fatal(err)
	}
	if err := entry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	registry.SetWorkingDirectory("broken", t.TempDir())
	server, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	status, body := postOpenTo(t, httpServer, protocol.SessionOpenRequest{SessionID: "kept", Reopen: true})
	if status != http.StatusBadRequest {
		t.Fatalf("reopen returned %d: %s", status, body)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	var failure protocol.ErrorResponse
	if err := envelope.DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "invalid_request" {
		t.Fatalf("reopen refused %q", failure.Error.Code)
	}
}
