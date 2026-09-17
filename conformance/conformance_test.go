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
	"testing"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The runner is only worth anything if it separates a conformant endpoint
// from one that is not. Testing it against the reference endpoint alone would
// prove it says yes; the second test is the one that proves it can say no,
// against the failure an endpoint is most likely to actually have.

var (
	buildOnce sync.Once
	binary    string
	buildErr  error
	buildDir  string
)

func TestMain(m *testing.M) {
	if os.Getenv("OAP_CONFORMANCE_HELPER") != "" {
		os.Exit(runHelperEndpoint())
	}
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// oapBinary builds ./cmd/oap once. A toolchain that is not on PATH skips
// rather than fails, matching the other binary-driving suites.
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
		root, err := filepath.Abs("..")
		if err != nil {
			buildErr = err
			return
		}
		binary = filepath.Join(buildDir, "oap")
		build := exec.Command(resolved, "build", "-o", binary, "./cmd/oap")
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

// TestRunnerAcceptsTheReferenceEndpoint drives the real binary, which is what
// makes the reference endpoint the known-good target the binding claims it is.
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
}

// TestRunnerRejectsAnEndpointThatSettlesTwice is the negative half, and it
// uses the failure the two settlement-path fixtures describe: an endpoint
// whose normal completion and whose error path both believe they own the run,
// so the run gets two different terminals.
//
// The runner must reject it, and must reject it through the validator rather
// than through a rule of its own — the diagnostic is asserted for that reason.
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

// runHelperEndpoint is a deliberately non-conformant endpoint, re-executed
// from this test binary the way the adapter rpc suites re-execute theirs. It
// speaks just enough of the binding to be driven, and gets exactly one thing
// wrong.
func runHelperEndpoint() int {
	const revision = "helper-v1"
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
			var request protocol.Envelope
			if json.Unmarshal([]byte(trimmed), &request) != nil || request.Type == "" {
				fmt.Fprintln(os.Stderr, "helper: malformed line")
				return 2
			}
			handleHelperRequest(request, revision, emit)
		}
		if err != nil {
			return 0
		}
	}
}

func handleHelperRequest(request protocol.Envelope, revision string, emit func(protocol.EnvelopeType, any, func(*protocol.Envelope))) {
	reply := func(e *protocol.Envelope) {
		e.InReplyTo = request.ID
		e.SessionID = request.SessionID
		e.CapabilityRevision = request.CapabilityRevision
	}
	switch request.Type {
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
		emit(protocol.TypeSessionMessageSubmitResponse, protocol.MessageSubmitResponse{
			SessionID: submit.SessionID, Accepted: true, SubmissionID: "helper-sub",
			RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart,
			Admission: protocol.AdmissionStarted, RunID: "helper-run", Status: protocol.RunRunning,
		}, func(e *protocol.Envelope) { reply(e); e.RunID = "helper-run" })
		run := func(e *protocol.Envelope) {
			e.SessionID = submit.SessionID
			e.RunID = "helper-run"
		}
		seq := func(n uint64) func(*protocol.Envelope) {
			return func(e *protocol.Envelope) { run(e); e.Sequence = &n }
		}
		emit(protocol.TypeRunStarted, protocol.RunStartedPayload{
			SessionID: submit.SessionID, RunID: "helper-run", Status: protocol.RunRunning,
		}, seq(1))
		emit(protocol.TypeRunCompleted, protocol.RunCompletedPayload{
			SessionID: submit.SessionID, RunID: "helper-run", StopReason: "end_turn",
			FinalResponse: protocol.Message{Role: protocol.RoleAssistant, Content: protocol.TextContent("done")},
		}, seq(2))
		// The defect: the error path settles the same run the completion path
		// just settled. Two settlement paths, two different terminals.
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
	}
}
