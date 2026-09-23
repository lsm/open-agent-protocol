package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

var (
	buildOnce sync.Once
	binary    string
	buildErr  error
	buildDir  string
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("OAP_CONFORMANCE_HELPER"); mode != "" {
		os.Exit(runHelperEndpoint(mode))
	}
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func oapBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		goTool := os.Getenv("OAP_GO")
		if goTool == "" {
			goTool = "go"
		}
		resolved, err := exec.LookPath(goTool)
		if err != nil {
			buildErr = fmt.Errorf("no go toolchain on PATH: %w", err)
			return
		}
		buildDir, buildErr = os.MkdirTemp("", "oap-conformance")
		if buildErr != nil {
			return
		}
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			buildErr = err
			return
		}
		binary = filepath.Join(buildDir, "oap")
		build := exec.Command(resolved, "build", "-o", binary, "./go/cmd/oap")
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("build oap: %v: %s", err, output)
			return
		}
	})
	if buildErr != nil {
		if strings.Contains(buildErr.Error(), "no go toolchain") {
			t.Skip(buildErr.Error())
		}
		t.Fatal(buildErr)
	}
	return binary
}

func TestRunnerAcceptsTheReferenceEndpoint(t *testing.T) {
	oap := oapBinary(t)
	report, err := Run(context.Background(), Options{
		Command: []string{oap, "endpoint", "--adapter", "memory"},
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range report.Checks {
		if !check.Passed {
			t.Errorf("the reference endpoint failed %q: %s", check.Name, check.Detail)
		}
	}
	for _, diagnostic := range report.Diagnostics {
		t.Errorf("the reference exchange is not a valid trace: %s: %s", diagnostic.Code, diagnostic.Message)
	}
	if !report.Passed {
		t.Fatal("the reference endpoint is reported non-conformant")
	}
	seen := map[string]bool{}
	for _, check := range report.Checks {
		seen[check.Name] = true
	}
	for _, name := range []string{
		"session.model.switch changes the session default",
		"session.model.switch publishes canonical state",
		"session.model.switch refuses a missing model with its id",
		"the switched model is used by the next run",
	} {
		if !seen[name] {
			t.Errorf("conformance did not exercise %q", name)
		}
	}
}

func TestRunnerRejectsAnEndpointThatSettlesTwice(t *testing.T) {
	report, err := Run(context.Background(), Options{
		Command: helperCommand(t, "double-settle"),
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Passed {
		t.Fatal("an endpoint that settled one run twice was reported conformant")
	}
	var sawDuplicate bool
	for _, diagnostic := range report.Diagnostics {
		if diagnostic.Code == "duplicate_run_terminal" {
			sawDuplicate = true
		}
	}
	if !sawDuplicate {
		t.Fatalf("expected duplicate_run_terminal from the validator, got %+v", report.Diagnostics)
	}
}

func helperCommand(t *testing.T, mode string) []string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	os.Setenv("OAP_CONFORMANCE_HELPER", mode)
	t.Cleanup(func() { os.Unsetenv("OAP_CONFORMANCE_HELPER") })
	return []string{self}
}

func runHelperEndpoint(mode string) int {
	const revision = "helper-v1"
	if mode == "closes-stdout" {

		os.Stdout.Close()
		time.Sleep(time.Hour)
		return 0
	}
	if mode == "mute" {

		time.Sleep(time.Hour)
		return 0
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	ids := 0
	emit := func(typ protocol.EnvelopeType, payload any, decorate func(*protocol.Envelope)) {
		ids++
		envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(fmt.Sprintf("helper-%d", ids)), payload)
		if err != nil {
			return
		}
		if decorate != nil {
			decorate(&envelope)
		}
		data, err := json.Marshal(envelope)
		if err != nil {
			return
		}
		out.Write(append(data, '\n'))
		out.Flush()
	}

	reader := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		text, err := reader.ReadString('\n')
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			var shape struct {
				Protocol string `json:"protocol"`
				Control  string `json:"control"`
				ID       string `json:"id"`
			}
			if json.Unmarshal([]byte(trimmed), &shape) != nil {
				fmt.Fprintln(os.Stderr, "helper: malformed line")
				return 2
			}
			if shape.Protocol == "" && shape.Control != "" {
				if mode == "mute-controls" {

					continue
				}

				frame, _ := json.Marshal(ControlFrame{
					Control: "replay.error", ID: shape.ID, Code: "unsupported_control",
					Message: "this helper serves no controls",
				})
				out.Write(append(frame, '\n'))
				out.Flush()
				continue
			}
			var request protocol.Envelope
			if json.Unmarshal([]byte(trimmed), &request) != nil || request.Type == "" {
				fmt.Fprintln(os.Stderr, "helper: malformed line")
				return 2
			}
			handleHelperRequest(request, revision, mode, emit)
		}
		if err != nil {
			return 0
		}
	}
}

func handleHelperRequest(request protocol.Envelope, revision, mode string, emit func(protocol.EnvelopeType, any, func(*protocol.Envelope))) {
	reply := func(e *protocol.Envelope) {
		e.InReplyTo = request.ID
		e.SessionID = request.SessionID
		e.CapabilityRevision = request.CapabilityRevision
	}
	switch request.Type {
	case protocol.TypeProtocolInitializeRequest:
		emit(protocol.TypeProtocolInitializeResponse, protocol.InitializeResponse{
			ProtocolVersion: protocol.Version, Profile: protocol.Profile,
			Endpoint: protocol.EndpointDescriptor{ID: "helper.double-settle", Name: "Double-settling helper endpoint"},
		}, func(e *protocol.Envelope) { reply(e); e.CapabilityRevision = revision })
	case protocol.TypeRunCancelRequest:

		emit(protocol.TypeErrorResponse, protocol.ErrorResponse{
			Error: protocol.ProtocolError{Code: "unsupported_feature", Message: "this helper cannot cancel"},
		}, reply)
	case protocol.TypeCapabilitiesRequest:
		emit(protocol.TypeCapabilitiesResponse, protocol.CapabilityDescriptor{
			Endpoint: protocol.EndpointDescriptor{ID: "helper.double-settle", Name: "Double-settling helper endpoint"},
			Features: map[string]protocol.FeatureSupport{
				"session.open":                  {Level: protocol.SupportNative},
				"session.message.submit":        {Level: protocol.SupportNative},
				"session.message.delivery.auto": {Level: protocol.SupportNative},
				"session.state":                 {Level: protocol.SupportNative},
			},
		}, func(e *protocol.Envelope) { reply(e); e.CapabilityRevision = revision })
	case protocol.TypeSessionOpenRequest:
		var open protocol.SessionOpenRequest
		_ = request.DecodePayload(&open)
		emit(protocol.TypeSessionOpenResponse, protocol.SessionState{
			SessionID: open.SessionID, Status: protocol.SessionIdle, UpdatedAtMS: 1,
		}, func(e *protocol.Envelope) { reply(e); e.SessionID = open.SessionID })
	case protocol.TypeSessionMessageSubmitRequest:
		var submit protocol.MessageSubmitRequest
		_ = request.DecodePayload(&submit)
		run := func(e *protocol.Envelope) {
			e.SessionID = submit.SessionID
			e.RunID = "helper-run"
		}
		seq := func(n uint64) func(*protocol.Envelope) {
			return func(e *protocol.Envelope) { run(e); e.Sequence = &n }
		}
		acknowledge := func() {
			emit(protocol.TypeSessionMessageSubmitResponse, protocol.MessageSubmitResponse{
				SessionID: submit.SessionID, Accepted: true, SubmissionID: "helper-sub",
				RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart,
				Admission: protocol.AdmissionStarted, RunID: "helper-run", Status: protocol.RunRunning,
			}, func(e *protocol.Envelope) { reply(e); e.RunID = "helper-run" })
		}
		started := func() {
			emit(protocol.TypeRunStarted, protocol.RunStartedPayload{
				SessionID: submit.SessionID, RunID: "helper-run", Status: protocol.RunRunning,
			}, seq(1))
		}
		completed := func() {
			emit(protocol.TypeRunCompleted, protocol.RunCompletedPayload{
				SessionID: submit.SessionID, RunID: "helper-run", StopReason: "end_turn",
				FinalResponse: protocol.Message{Role: protocol.RoleAssistant, Content: protocol.TextContent("done")},
			}, seq(2))
		}

		if mode == "early-events" {

			started()
			completed()
			acknowledge()
			return
		}
		acknowledge()
		started()
		completed()

		emit(protocol.TypeRunFailed, protocol.RunFailedPayload{
			SessionID: submit.SessionID, RunID: "helper-run",
			Error: protocol.ProtocolError{Code: "internal_error", Message: "the other settlement path also settled this run"},
		}, seq(3))
	case protocol.TypeSessionStateRequest:
		var state protocol.SessionStateRequest
		_ = request.DecodePayload(&state)
		emit(protocol.TypeSessionStateResponse, protocol.SessionState{
			SessionID: state.SessionID, Status: protocol.SessionIdle, UpdatedAtMS: 2,
		}, reply)
	default:

		emit(protocol.TypeErrorResponse, protocol.ErrorResponse{
			Error: protocol.ProtocolError{Code: "unsupported_request", Message: "this helper serves no " + string(request.Type)},
		}, reply)
	}
}

func TestReplayRefusesACursorItCannotHonour(t *testing.T) {
	oap := oapBinary(t)
	client, err := Spawn(context.Background(), oap, []string{"endpoint", "--adapter", "memory"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseInput()

	open, err := protocol.NewEnvelope(protocol.TypeSessionOpenRequest, "open-1", protocol.SessionOpenRequest{SessionID: "replay"})
	if err != nil {
		t.Fatal(err)
	}
	open.SessionID = "replay"
	if err := client.Send(open); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Response(open.ID); err != nil {
		t.Fatal(err)
	}

	after := uint64(0)
	frame := ControlFrame{Control: "replay", ID: "replay-1", SessionID: "replay", RunID: "no-such-run", After: &after}
	if err := client.SendControl(frame); err != nil {
		t.Fatal(err)
	}
	answer, err := client.Control(frame.ID)
	if err != nil {
		t.Fatal(err)
	}
	if answer.Control == "replay.accepted" {
		t.Fatal("the endpoint accepted a replay for a run it does not have")
	}
	if answer.Control != "replay.error" && answer.Control != "replay.gap" {
		t.Fatalf("unexpected control %q: %+v", answer.Control, answer)
	}
	if answer.Control == "replay.error" && answer.Code == "" {
		t.Fatal("a replay.error carries no code, so the host is told nothing it can act on")
	}
}

func TestIdleEndpointStopsOnSignal(t *testing.T) {
	oap := oapBinary(t)
	cmd := exec.Command(oap, "endpoint", "--adapter", "memory")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	request, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "ready-1", protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReaderSize(stdout, 1<<20).ReadString('\n'); err != nil {
		t.Fatalf("the endpoint never answered, so it was never ready: %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("an idle endpoint interrupted with SIGINT exited %v, want a clean stop", err)
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("an idle endpoint ignored SIGINT; only SIGKILL would stop it, which skips the session sweep")
	}
}

func TestRunnerAcceptsAnEndpointThatStreamsBeforeAcknowledging(t *testing.T) {
	report, err := Run(context.Background(), Options{
		Command: helperCommand(t, "early-events"),
		Stderr:  io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range report.Diagnostics {
		t.Errorf("an endpoint streaming before its acknowledgement was judged invalid: %s: %s",
			diagnostic.Code, diagnostic.Message)
	}
	for _, check := range report.Checks {
		if !check.Passed && check.Name == "the exchange validates as an OAP trace" {
			t.Fatalf("the assembled trace was rejected: %s", check.Detail)
		}
	}
}

func TestClientKillsAnEndpointThatNeitherSpeaksNorExits(t *testing.T) {
	const deadline = 300 * time.Millisecond
	client, err := SpawnWithDeadline(context.Background(), helperCommand(t, "mute")[0], nil, io.Discard, deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Response("never-answered"); err == nil {
		t.Fatal("waiting on a mute endpoint returned no error")
	}
	if client.cmd.ProcessState == nil {
		t.Fatal("the endpoint was left running after the runner gave up on it")
	}

	start := time.Now()
	if _, err := client.Response("never-answered-either"); err == nil {
		t.Fatal("a second wait on a dead endpoint returned no error")
	}
	if elapsed := time.Since(start); elapsed > deadline/2 {
		t.Fatalf("a second wait took %s, so it paid the deadline again", elapsed)
	}
}

func TestClientDoesNotWaitForeverOnAnEndpointThatClosesStdout(t *testing.T) {
	const deadline = 300 * time.Millisecond
	client, err := SpawnWithDeadline(context.Background(), helperCommand(t, "closes-stdout")[0], nil, io.Discard, deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		_, waitErr := client.Wait()
		done <- waitErr
	}()
	select {
	case waitErr := <-done:
		if waitErr == nil {
			t.Fatal("waiting on an endpoint that never exited reported success")
		}
		if !strings.Contains(waitErr.Error(), "not exited") {
			t.Fatalf("unexpected error %v", waitErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Wait never returned: the runner is waiting on a binary that closed stdout and stayed alive")
	}
	if client.cmd.ProcessState == nil {
		t.Fatal("the endpoint was left running after the runner gave up on it")
	}
}

func TestUnansweredControlDoesNotCascade(t *testing.T) {
	report, err := Run(context.Background(), Options{
		Command:      helperCommand(t, "mute-controls"),
		Stderr:       io.Discard,
		LineDeadline: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	var replay Check
	for _, check := range report.Checks {
		if strings.Contains(check.Name, "cursor replay") {
			replay = check
		}
		if strings.Contains(check.Detail, "file already closed") {
			t.Fatalf("check %q reports a closed pipe, so the runner killed the endpoint over a control it was never owed", check.Name)
		}
	}
	if replay.Name == "" || replay.Passed {
		t.Fatalf("the replay check should fail for an endpoint that answers no control: %+v", replay)
	}
	if !strings.Contains(replay.Detail, "unsupported_control") {
		t.Fatalf("the failure should say what the endpoint owed: %q", replay.Detail)
	}

	var state Check
	for _, check := range report.Checks {
		if strings.Contains(check.Name, "session.state.request is answered") {
			state = check
		}
	}
	if state.Name == "" || !state.Passed {
		t.Fatalf("the checks after an unanswered control must still run: %+v", state)
	}
}
