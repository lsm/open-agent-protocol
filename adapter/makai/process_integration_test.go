package makai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/internal/providertest"
	"github.com/lsm/open-agent-protocol/protocol"
)

func TestPinnedMakaiProcessAgainstResponsesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping pinned Makai process integration in short mode")
	}
	if os.Getenv("OAP_MAKAI_INTEGRATION") != "1" {
		t.Skip("set OAP_MAKAI_INTEGRATION=1, OAP_MAKAI_BIN, and OAP_MAKAI_COMMIT to run; optionally set OAP_MAKAI_SHA256 to bind the exact release artifact")
	}
	binary := os.Getenv("OAP_MAKAI_BIN")
	if binary == "" {
		t.Fatal("OAP_MAKAI_BIN is required when OAP_MAKAI_INTEGRATION=1")
	}
	if os.Getenv("OAP_MAKAI_COMMIT") != PinnedCommit {
		t.Fatalf("OAP_MAKAI_COMMIT must equal pinned commit %s", PinnedCommit)
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("OAP_MAKAI_BIN is not an executable file: %v", err)
	}
	if expected := os.Getenv("OAP_MAKAI_SHA256"); expected != "" {
		if len(expected) != sha256.Size*2 {
			t.Fatal("OAP_MAKAI_SHA256 must be exactly 64 hexadecimal characters")
		}
		expectedDigest, err := hex.DecodeString(expected)
		if err != nil {
			t.Fatal("OAP_MAKAI_SHA256 must be exactly 64 hexadecimal characters")
		}
		file, err := os.Open(binary)
		if err != nil {
			t.Fatalf("open OAP_MAKAI_BIN for digest verification: %v", err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("hash OAP_MAKAI_BIN: %v", copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close OAP_MAKAI_BIN after hashing: %v", closeErr)
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expectedDigest)) {
			t.Fatal("OAP_MAKAI_BIN SHA-256 does not match OAP_MAKAI_SHA256")
		}
	}
	mock := providertest.New(t, providertest.Config{OpenAIKey: "fixture-makai-key"})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	environment := []string{
		"HOME=" + t.TempDir(),
		"OPENAI_API_KEY=fixture-makai-key",
		"OPENAI_BASE_URL=" + mock.OpenAIBaseURL(),
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	implementation, err := New(Config{Executable: binary, Environment: environment, WorkingDirectory: t.TempDir(), AgentConfig: json.RawMessage(`{"model_ref":"openai/openai-responses@fixture-model","tools":[]}`), ShutdownTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{SessionID: "makai-process-session", Participant: protocol.Participant{ID: "integration-user"}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{SessionID: "makai-process-session", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("openai/openai-responses@fixture-model"), Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}}})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, 30*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s payload=%s events=%+v", events[len(events)-1].Type, events[len(events)-1].Payload, events)
	}
	requests := mock.RequestsFor(providertest.OpenAIResponses)
	if len(requests) != 1 || requests[0].Path != providertest.ResponsesPath || requests[0].Model != "fixture-model" {
		t.Fatalf("Responses requests=%d: %+v", len(requests), requests)
	}
	if requests[0].Header.Get("Authorization") != "Bearer fixture-makai-key" {
		t.Fatal("unexpected mock authorization")
	}
}
