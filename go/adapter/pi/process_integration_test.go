package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const piMockSecret = "fixture-pi-key"

func TestPiProcessSmoke(t *testing.T) {
	if os.Getenv("OAP_PI_SMOKE") != "1" {
		t.Skipf("set OAP_PI_SMOKE=1 and absolute OAP_PI_BIN pointing to Pi %s to run; optionally set OAP_PI_SHA256 for exact-artifact evidence", PinnedVersion)
	}
	binary := verifiedPiBinary(t)
	root := t.TempDir()
	implementation := newPinnedPi(t, binary, root, piEnvironment(t, root, ""), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "pi-smoke-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close(context.Background())
		}
	}()
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID != "pi-smoke-session" || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close Pi smoke session: %v", err)
	}
	closed = true
}

func TestPiProcessAgainstResponsesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Pi process integration in short mode")
	}
	if os.Getenv("OAP_PI_INTEGRATION") != "1" {
		t.Skipf("set OAP_PI_INTEGRATION=1 and absolute OAP_PI_BIN pointing to Pi %s to run; optionally set OAP_PI_SHA256 for exact-artifact evidence", PinnedVersion)
	}
	binary := verifiedPiBinary(t)
	mock := providertest.New(t, providertest.Config{OpenAIKey: piMockSecret})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)

	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	models := map[string]any{"providers": map[string]any{"oap-loopback": map[string]any{
		"baseUrl": mock.OpenAIBaseURL(), "api": "openai-responses", "apiKey": "$OAP_PI_MOCK_KEY",
		"models": []map[string]any{{"id": "fixture-model", "name": "OAP loopback fixture"}},
	}}}
	data, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"--provider", "oap-loopback", "--model", "fixture-model"}
	implementation := newPinnedPi(t, binary, root, piEnvironment(t, root, piMockSecret), args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "pi-process-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close(context.Background())
		}
	}()
	admission, stream, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "pi-process-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, 30*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s", events[len(events)-1].Type)
	}
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	parts, ok := completed.FinalResponse.Content.Parts()
	if !ok || len(parts) != 1 || parts[0].Text != providertest.FixtureText {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}
	requests := mock.RequestsFor(providertest.OpenAIResponses)
	if len(requests) != 1 || requests[0].Path != providertest.ResponsesPath || requests[0].Model != "fixture-model" {
		t.Fatalf("Responses requests=%d: %+v", len(requests), requests)
	}
	if requests[0].Header.Get("Authorization") != "Bearer "+piMockSecret {
		t.Fatal("unexpected mock authorization")
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close Pi integration session: %v", err)
	}
	closed = true
}

func TestPiProcessCompactsPastItsThresholdInsideTheRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Pi process integration in short mode")
	}
	if os.Getenv("OAP_PI_INTEGRATION") != "1" {
		t.Skipf("set OAP_PI_INTEGRATION=1 and absolute OAP_PI_BIN pointing to Pi %s to run; optionally set OAP_PI_SHA256 for exact-artifact evidence", PinnedVersion)
	}
	binary := verifiedPiBinary(t)
	mock := providertest.New(t, providertest.Config{OpenAIKey: piMockSecret})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]any{
		"models.json": map[string]any{"providers": map[string]any{"oap-loopback": map[string]any{
			"baseUrl": mock.OpenAIBaseURL(), "api": "openai-responses", "apiKey": "$OAP_PI_MOCK_KEY",
			"models": []map[string]any{{"id": "fixture-model", "name": "OAP loopback fixture", "contextWindow": 4000, "maxTokens": 100}},
		}}},
		"settings.json": map[string]any{"compaction": map[string]any{"reserveTokens": 3990, "keepRecentTokens": 1}},
	}
	for name, content := range files {
		data, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(agentDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	implementation := newPinnedPi(t, binary, root, piEnvironment(t, root, piMockSecret), []string{"--provider", "oap-loopback", "--model", "fixture-model"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{SessionID: "pi-threshold-session", Participant: protocol.Participant{ID: "integration-user"}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	admission, stream, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "pi-threshold-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, 30*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	var started protocol.RunCompactionStartedPayload
	var ended protocol.RunCompactionEndedPayload
	for _, event := range events {
		switch event.Type {
		case protocol.TypeRunCompactionStarted:
			if err := event.DecodePayload(&started); err != nil {
				t.Fatal(err)
			}
		case protocol.TypeRunCompactionEnded:
			if err := event.DecodePayload(&ended); err != nil {
				t.Fatal(err)
			}
		}
	}
	if started.Reason != protocol.CompactionThreshold || ended.CompactionID != started.CompactionID || ended.Outcome != protocol.CompactionCompleted || ended.Summary == nil {
		t.Fatalf("compaction started=%+v ended=%+v, want one completed threshold compaction with a summary", started, ended)
	}
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s", events[len(events)-1].Type)
	}
	if requests := mock.RequestsFor(providertest.OpenAIResponses); len(requests) != 2 {
		t.Fatalf("Responses requests=%d, want the turn and its summary", len(requests))
	}
}

func TestPiProcessCompactsOnRequestAndOnCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Pi process integration in short mode")
	}
	if os.Getenv("OAP_PI_INTEGRATION") != "1" {
		t.Skipf("set OAP_PI_INTEGRATION=1 and absolute OAP_PI_BIN pointing to Pi %s to run; optionally set OAP_PI_SHA256 for exact-artifact evidence", PinnedVersion)
	}
	binary := verifiedPiBinary(t)
	for _, tc := range []struct {
		name    string
		summary providertest.Case
		cancel  bool
		want    protocol.EnvelopeType
		outcome protocol.CompactionOutcome
	}{
		{name: "requested", summary: providertest.Success, want: protocol.TypeRunCompleted, outcome: protocol.CompactionCompleted},
		{name: "cancelled", summary: providertest.Slow, cancel: true, want: protocol.TypeRunCancelled, outcome: protocol.CompactionCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := providertest.New(t, providertest.Config{OpenAIKey: piMockSecret})
			mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
			mock.Enqueue(providertest.OpenAIResponses, tc.summary)
			root := t.TempDir()
			agentDir := filepath.Join(root, "agent")
			if err := os.MkdirAll(agentDir, 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string]any{
				"models.json": map[string]any{"providers": map[string]any{"oap-loopback": map[string]any{
					"baseUrl": mock.OpenAIBaseURL(), "api": "openai-responses", "apiKey": "$OAP_PI_MOCK_KEY",
					"models": []map[string]any{{"id": "fixture-model", "name": "OAP loopback fixture"}},
				}}},
				"settings.json": map[string]any{"compaction": map[string]any{"keepRecentTokens": 1}},
			}
			for name, content := range files {
				data, err := json.Marshal(content)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(agentDir, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			implementation := newPinnedPi(t, binary, root, piEnvironment(t, root, piMockSecret), []string{"--provider", "oap-loopback", "--model", "fixture-model"})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			session, err := implementation.Open(ctx, base.OpenRequest{SessionID: "pi-compact-session", Participant: protocol.Participant{ID: "integration-user"}})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close(context.Background())
			_, turn, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
				SessionID: "pi-compact-session", Delivery: protocol.DeliveryAuto,
				Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			adaptertest.Drain(t, turn, 30*time.Second)
			focus := "keep the fixture"
			admission, stream, err := session.(base.Compactor).Compact(ctx, base.CompactRequest{Request: protocol.SessionCompactRequest{SessionID: "pi-compact-session", Focus: &focus}, EnvelopeID: "compact"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.cancel {
				time.Sleep(300 * time.Millisecond)
				if _, err := session.Cancel(ctx, admission.RunID); err != nil {
					t.Fatal(err)
				}
			}
			events := adaptertest.Drain(t, stream, 30*time.Second)
			var ended protocol.RunCompactionEndedPayload
			for _, event := range events {
				if event.Type == protocol.TypeRunCompactionEnded {
					if err := event.DecodePayload(&ended); err != nil {
						t.Fatal(err)
					}
				}
			}
			if events[len(events)-1].Type != tc.want || ended.Outcome != tc.outcome {
				t.Fatalf("events=%v outcome=%s, want %s ending %s", eventTypes(events), ended.Outcome, tc.outcome, tc.want)
			}
			if requests := mock.RequestsFor(providertest.OpenAIResponses); len(requests) != 2 {
				t.Fatalf("Responses requests=%d, want the turn and the summary", len(requests))
			}
		})
	}
}

func TestPiProcessChangesItsLevelAndCompactionBetweenRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Pi process integration in short mode")
	}
	if os.Getenv("OAP_PI_INTEGRATION") != "1" {
		t.Skipf("set OAP_PI_INTEGRATION=1 and absolute OAP_PI_BIN pointing to Pi %s to run; optionally set OAP_PI_SHA256 for exact-artifact evidence", PinnedVersion)
	}
	binary := verifiedPiBinary(t)
	mock := providertest.New(t, providertest.Config{OpenAIKey: piMockSecret})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	models, err := json.Marshal(map[string]any{"providers": map[string]any{"oap-loopback": map[string]any{
		"baseUrl": mock.OpenAIBaseURL(), "api": "openai-responses", "apiKey": "$OAP_PI_MOCK_KEY",
		"models": []map[string]any{{"id": "fixture-model", "name": "OAP loopback fixture", "reasoning": true}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), models, 0o600); err != nil {
		t.Fatal(err)
	}
	implementation := newPinnedPi(t, binary, root, piEnvironment(t, root, piMockSecret), []string{"--provider", "oap-loopback", "--model", "fixture-model"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{SessionID: "pi-live-session", Participant: protocol.Participant{ID: "integration-user"}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	updater := session.(base.SettingsUpdater)
	response, state, err := updater.UpdateSettings(ctx, protocol.SessionSettingsUpdateRequest{SessionID: "pi-live-session", ReasoningLevel: protocol.ReasoningHigh, CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}})
	if err != nil {
		t.Fatal(err)
	}
	if response.ReasoningLevel != protocol.ReasoningHigh || state.ReasoningLevel != protocol.ReasoningHigh || state.CompactionPolicy == nil || state.CompactionPolicy.Kind != protocol.CompactionOff {
		t.Fatalf("response %+v and state %q %+v, want high and off", response, state.ReasoningLevel, state.CompactionPolicy)
	}
	var refusal *base.UnsupportedControlError
	if _, _, err := updater.UpdateSettings(ctx, protocol.SessionSettingsUpdateRequest{SessionID: "pi-live-session", ReasoningLevel: protocol.ReasoningMax}); !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionReasoning {
		t.Fatalf("max answered %v, want a level Pi clamps refused", err)
	}
	polled, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if polled.ReasoningLevel != protocol.ReasoningHigh {
		t.Fatalf("after the refusal Pi runs at %q, want high restored", polled.ReasoningLevel)
	}
	_, turn, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "pi-live-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.Drain(t, turn, 30*time.Second)
	requests := mock.RequestsFor(providertest.OpenAIResponses)
	if len(requests) != 1 {
		t.Fatalf("Responses requests=%d, want one", len(requests))
	}
	var body struct {
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(requests[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Reasoning.Effort != "high" {
		t.Fatalf("the next run asked for effort %q, want high", body.Reasoning.Effort)
	}
}

func verifiedPiBinary(t *testing.T) string {
	t.Helper()
	binary := adaptertest.VerifiedBinary(t, "OAP_PI_BIN", "OAP_PI_SHA256", "a Pi "+PinnedVersion+" executable")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--version")
	command.Env = piEnvironment(t, t.TempDir(), "")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("verify Pi version: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != strings.TrimPrefix(PinnedVersion, "v") {
		t.Fatalf("Pi --version=%q, want %q", got, strings.TrimPrefix(PinnedVersion, "v"))
	}
	return binary
}

func newPinnedPi(t *testing.T, binary, root string, environment, args []string) *Adapter {
	t.Helper()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	implementation, err := New(Config{
		Executable: binary, Args: args, Environment: environment,
		WorkingDirectory: workspace, ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func piEnvironment(t *testing.T, root, secret string) []string {
	t.Helper()
	agentDir := filepath.Join(root, "agent")
	sessionDir := filepath.Join(root, "sessions")
	tmpDir := filepath.Join(root, "tmp")
	for _, directory := range []string{agentDir, sessionDir, tmpDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"HOME=" + root,
		"PI_CODING_AGENT_DIR=" + agentDir,
		"PI_CODING_AGENT_SESSION_DIR=" + sessionDir,
		"PI_TELEMETRY=0",
		"TMPDIR=" + tmpDir,
		"NO_COLOR=1",
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	if secret != "" {
		environment = append(environment, fmt.Sprintf("OAP_PI_MOCK_KEY=%s", secret))
	}
	return environment
}
