package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const portScenariosPath = "testdata/port-scenarios.json"

type scenarioOp struct {
	Op         string          `json:"op"`
	Delivery   string          `json:"delivery,omitempty"`
	Text       string          `json:"text,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
	Submission int             `json:"submission,omitempty"`
	Count      int             `json:"count,omitempty"`
	Message    string          `json:"message,omitempty"`
}

type scenarioClient struct {
	Gated            bool   `json:"gated,omitempty"`
	ActiveError      string `json:"active_error,omitempty"`
	HistoryError     string `json:"history_error,omitempty"`
	ForeignAdmission bool   `json:"foreign_admission,omitempty"`
}

type portScenario struct {
	Name       string                           `json:"name"`
	Client     scenarioClient                   `json:"client"`
	Script     []scenarioOp                     `json:"script"`
	Admissions []protocol.MessageSubmitResponse `json:"admissions"`
	Runs       [][]protocol.Envelope            `json:"runs"`
}

func runPortScenario(t *testing.T, scenario portScenario) portScenario {
	t.Helper()
	client := newFakeClient()
	client.promoted = true
	client.foreignAdmission = scenario.Client.ForeignAdmission
	if scenario.Client.ActiveError != "" {
		client.activeErr = errors.New(scenario.Client.ActiveError)
	}
	if scenario.Client.HistoryError != "" {
		client.historyErr = errors.New(scenario.Client.HistoryError)
	}
	var gate chan struct{}
	if scenario.Client.Gated {
		gate = make(chan struct{})
		client.idleGate = gate
	}
	session, _ := openTest(t, client, 64)
	var admissions []protocol.MessageSubmitResponse
	var streams []base.EventStream
	var runs [][]protocol.Envelope
	for index, op := range scenario.Script {
		switch op.Op {
		case "submit":
			delivery := protocol.RequestedDeliveryMode(op.Delivery)
			admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: delivery, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(op.Text)}}})
			if err != nil {
				t.Fatalf("%s op %d: %v", scenario.Name, index, err)
			}
			admissions = append(admissions, admission)
			streams = append(streams, stream)
			runs = append(runs, []protocol.Envelope{})
		case "event":
			event, err := native.DecodeEvent(op.Raw)
			if err != nil {
				t.Fatalf("%s op %d: %v", scenario.Name, index, err)
			}
			client.events <- event
		case "await":
			for range op.Count {
				runs[op.Submission] = append(runs[op.Submission], adaptertest.Next(t, streams[op.Submission], 2*time.Second))
			}
		case "cancel":
			if _, err := session.Cancel(context.Background(), admissions[op.Submission].RunID); err != nil {
				t.Fatalf("%s op %d: %v", scenario.Name, index, err)
			}
		case "idle":
			close(gate)
		case "fail":
			client.subscription.fail(errors.New(op.Message))
		default:
			t.Fatalf("%s op %d: unknown op %q", scenario.Name, index, op.Op)
		}
	}
	for index, stream := range streams {
		runs[index] = append(runs[index], adaptertest.Drain(t, stream, 2*time.Second)...)
	}
	scenario.Admissions, scenario.Runs = admissions, runs
	return scenario
}

func TestPortScenariosAreWhatTheGoSessionEmits(t *testing.T) {
	path := filepath.FromSlash(portScenariosPath)
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var scenarios []portScenario
	if err := json.Unmarshal(stored, &scenarios); err != nil {
		t.Fatal(err)
	}
	for index := range scenarios {
		scenarios[index] = runPortScenario(t, scenarios[index])
	}
	got, err := json.MarshalIndent(scenarios, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if os.Getenv("OAP_UPDATE_OPENCODE_PORT_GOLDENS") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		stored = got
	}
	if !bytes.Equal(stored, got) {
		t.Fatalf("%s drifted from the Go session\nwant: %s\ngot:  %s", portScenariosPath, stored, got)
	}
}
