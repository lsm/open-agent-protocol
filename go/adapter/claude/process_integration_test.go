package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	claudeMockSecret = "fixture-claude-key"

	claudeLoopbackModel = "claude-sonnet-4-5"
)

func verifiedClaudeCLI(t *testing.T) string {
	t.Helper()
	binary := adaptertest.VerifiedBinary(t, "OAP_CLAUDE_BIN", "OAP_CLAUDE_SHA256", "the pinned claude binary (2.1.280)")
	return binary
}

func newPinnedClaude(t *testing.T, environment []string, workDir string, tools ToolPosture, args ...string) *Adapter {
	t.Helper()
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	implementation, err := New(Config{
		Executable:       verifiedClaudeCLI(t),
		Args:             args,
		Environment:      environment,
		WorkingDirectory: workDir,
		Model:            claudeLoopbackModel,
		Tools:            tools,
		ExitTimeout:      15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func TestClaudeProcessSmoke(t *testing.T) {
	if os.Getenv("OAP_CLAUDE_SMOKE") != "1" {
		t.Skip("set OAP_CLAUDE_SMOKE=1 with absolute OAP_CLAUDE_BIN (pinned claude 2.1.280 binary) to run; optionally set OAP_CLAUDE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	root := t.TempDir()
	implementation := newPinnedClaude(t, claudeEnvironment(t, root, ""), filepath.Join(root, "work"), UnrestrictedTools())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "claude-smoke-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatalf("open pinned CLI (spawn + initialize exchange): %v", err)
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
	if state.SessionID != "claude-smoke-session" || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}

	if err := session.Close(ctx); err != nil {
		t.Fatalf("close claude smoke session: %v", err)
	}
	closed = true
}

func TestClaudeProcessAgainstMessagesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Claude Code process integration in short mode")
	}
	if os.Getenv("OAP_CLAUDE_INTEGRATION") != "1" {
		t.Skip("set OAP_CLAUDE_INTEGRATION=1 with absolute OAP_CLAUDE_BIN (pinned claude 2.1.280 binary) to run; optionally set OAP_CLAUDE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	mock := providertest.New(t, providertest.Config{AnthropicKey: claudeMockSecret})
	mock.Enqueue(providertest.AnthropicMessages, providertest.Success)
	root := t.TempDir()
	implementation := newPinnedClaude(t, claudeEnvironment(t, root, mock.AnthropicBaseURL()), filepath.Join(root, "work"), UnrestrictedTools())

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "claude-process-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatalf("open pinned CLI (spawn + initialize exchange): %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close(context.Background())
		}
	}()
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "claude-process-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if admission.Admission != protocol.AdmissionStarted || admission.RunID == "" {
		t.Fatalf("admission=%+v", admission)
	}
	events := adaptertest.Drain(t, stream, 60*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	var streamed strings.Builder
	for _, event := range events {
		if event.Type != protocol.TypeContentDelta {
			continue
		}
		var payload protocol.ContentDeltaPayload
		if err := event.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Part.Type == protocol.ContentText {
			streamed.WriteString(payload.Part.Text)
		}
	}
	if streamed.Len() == 0 {
		t.Fatal("run produced no streamed content deltas")
	}
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s", events[len(events)-1].Type)
	}
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if text, ok := completed.FinalResponse.Content.Text(); !ok || !strings.Contains(text, providertest.FixtureText) {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}

	requests := mock.RequestsFor(providertest.AnthropicMessages)
	if len(requests) == 0 {
		t.Fatal("loopback provider received no messages request")
	}
	first := requests[0]
	if first.Path != providertest.MessagesPath || first.Model != claudeLoopbackModel {
		t.Fatalf("messages request: %+v", first)
	}
	if first.Header.Get("x-api-key") != claudeMockSecret {
		t.Fatal("unexpected mock authentication")
	}
	if first.Header.Get("anthropic-version") == "" {
		t.Fatal("messages request carried no anthropic-version header")
	}
	if strings.HasPrefix(first.Header.Get("Authorization"), "Bearer ") {
		t.Fatal("messages request carried a bearer token")
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle {
		t.Fatalf("post-run state=%+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close claude integration session: %v", err)
	}
	closed = true
}

func TestClaudeProcessReadOnlyReviewCompletesWithoutAGate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Claude Code process integration in short mode")
	}
	if os.Getenv("OAP_CLAUDE_INTEGRATION") != "1" {
		t.Skip("set OAP_CLAUDE_INTEGRATION=1 with absolute OAP_CLAUDE_BIN (pinned claude 2.1.280 binary) to run; optionally set OAP_CLAUDE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	const reviewed = "a line the review has to read"
	if err := os.WriteFile(filepath.Join(work, "CHANGES.md"), []byte(reviewed+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	written := filepath.Join(work, "written-by-an-unlisted-tool")
	readArguments, err := json.Marshal(map[string]string{"file_path": filepath.Join(work, "CHANGES.md")})
	if err != nil {
		t.Fatal(err)
	}
	bashArguments, err := json.Marshal(map[string]string{"command": "touch " + written, "description": "write a file"})
	if err != nil {
		t.Fatal(err)
	}

	mock := providertest.New(t, providertest.Config{AnthropicKey: claudeMockSecret})
	mock.EnqueueToolCall(providertest.AnthropicMessages, providertest.ToolCall{ID: "toolu_review_read", Name: "Read", Arguments: string(readArguments)})
	mock.EnqueueToolCall(providertest.AnthropicMessages, providertest.ToolCall{ID: "toolu_review_bash", Name: "Bash", Arguments: string(bashArguments)})
	mock.Enqueue(providertest.AnthropicMessages, providertest.Success)
	const systemPrompt = "You review changes by reading files and never modify them."
	implementation := newPinnedClaude(t, claudeEnvironment(t, root, mock.AnthropicBaseURL()), work,
		AllowTools("Read", "Grep", "Glob"), "--append-system-prompt", systemPrompt, "--no-session-persistence")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "claude-review-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatalf("open pinned CLI (spawn + initialize exchange): %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close(context.Background())
		}
	}()
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "claude-review-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Review CHANGES.md.")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []protocol.Envelope
	for settled := false; !settled; {
		event := adaptertest.Next(t, stream, 60*time.Second)
		if event.Type == protocol.TypeUserInputRequested {
			t.Fatalf("a permission gate opened on an allowlisted review: %s", event.Payload)
		}
		events = append(events, event)
		settled = event.Type == protocol.TypeRunCompleted || event.Type == protocol.TypeRunFailed || event.Type == protocol.TypeRunCancelled
	}
	if trailing := adaptertest.Drain(t, stream, 10*time.Second); len(trailing) != 0 {
		t.Fatalf("events after the terminal: %v", trailing)
	}
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)

	var read, bash *protocol.ActionCallPayload
	for _, event := range events {
		switch event.Type {
		case protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed:
			var payload protocol.ActionCallPayload
			if err := event.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			switch {
			case payload.Name == "Read" && event.Type == protocol.TypeActionCallCompleted:
				read = &payload
			case payload.Name == "Bash" && event.Type == protocol.TypeActionCallFailed:
				bash = &payload
			default:
				t.Fatalf("unexpected %s for %s", event.Type, payload.Name)
			}
		}
	}
	if read == nil || !strings.Contains(string(read.Result), reviewed) {
		t.Fatalf("the allowlisted Read did not complete with the file's content: %+v", read)
	}
	if bash == nil || bash.Error == nil {
		t.Fatalf("the unlisted Bash was not refused: %+v", bash)
	}
	if _, err := os.Stat(written); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unlisted Bash ran: stat %s: %v", written, err)
	}

	terminal := events[len(events)-1]
	if terminal.Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s", terminal.Type)
	}
	var completed protocol.RunCompletedPayload
	if err := terminal.DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if text, ok := completed.FinalResponse.Content.Text(); !ok || !strings.Contains(text, providertest.FixtureText) {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}
	if completed.Usage == nil || completed.Usage.InputTokens == 0 || completed.Usage.OutputTokens == 0 {
		t.Fatalf("run.completed carries no usage: %+v", completed.Usage)
	}

	requests := mock.RequestsFor(providertest.AnthropicMessages)
	if len(requests) != 3 {
		t.Fatalf("loopback provider saw %d messages requests, want the Read turn, the Bash turn and the answer", len(requests))
	}
	for _, request := range requests {
		var body struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		var offered []string
		for _, tool := range body.Tools {
			offered = append(offered, tool.Name)
		}
		slices.Sort(offered)
		if !slices.Equal(offered, []string{"Glob", "Grep", "Read"}) {
			t.Fatalf("the provider was offered %v, want only the allowlist", offered)
		}
		if !strings.Contains(string(request.Body), systemPrompt) {
			t.Fatal("the appended system prompt did not reach the provider")
		}
	}

	if err := session.Close(ctx); err != nil {
		t.Fatalf("close claude review session: %v", err)
	}
	closed = true
}

func claudeEnvironment(t *testing.T, root, loopbackBaseURL string) []string {
	t.Helper()
	home := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	tmpDir := filepath.Join(root, "tmp")
	for _, directory := range []string{home, configDir, tmpDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"HOME=" + home,
		"CLAUDE_CONFIG_DIR=" + configDir,
		"TMPDIR=" + tmpDir,
		"LANG=C.UTF-8",
		"NO_COLOR=1",
		"DISABLE_TELEMETRY=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	if loopbackBaseURL != "" {
		environment = append(environment,
			"ANTHROPIC_BASE_URL="+loopbackBaseURL,
			"ANTHROPIC_API_KEY="+claudeMockSecret,
		)
	}
	return environment
}

func TestClaudeProcessEnvironmentIsAllowlisted(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ambient-must-not-leak")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-must-not-leak")
	environment := claudeEnvironment(t, t.TempDir(), "")
	for _, entry := range environment {
		for _, secret := range []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_BASE_URL="} {
			if strings.HasPrefix(entry, secret) {
				t.Fatalf("credential-free environment carries %s", secret)
			}
		}
	}
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "ambient-must-not-leak") {
		t.Fatal("ambient credential leaked into the child environment")
	}
	loopback := claudeEnvironment(t, t.TempDir(), "http://127.0.0.1:1")
	hasKey := false
	for _, entry := range loopback {
		if entry == "ANTHROPIC_API_KEY="+claudeMockSecret {
			hasKey = true
		}
	}
	if !hasKey {
		t.Fatal("loopback environment lost the test-owned key")
	}
}
