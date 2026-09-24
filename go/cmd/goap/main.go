package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/provider"
	"github.com/lsm/open-agent-protocol/go/validation"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "goap:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usage(stderr)
	}
	switch args[0] {
	case "hub":
		return runHub(ctx, args[1:], stdin, stdout, stderr)
	case "serve":
		return runServeRole(ctx, args[1:], stdin, stdout, stderr)
	case "endpoint":
		return runEndpoint(ctx, "endpoint", "adapter", args[1:], stdin, stdout, stderr)
	case "conformance":
		return runConformance(ctx, args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "fixtures":
		return runFixtures(args[1:], stdout)
	case "demo":
		return runDemo(ctx, args[1:], stdout)
	case "check":
		return runCheck(ctx, args[1:], stdout)
	case "providers":
		return runProviders(args[1:], stdout, stderr)
	default:
		return usage(stderr)
	}
}

func usage(w io.Writer) error {
	fmt.Fprintln(w, "usage: goap <hub|serve|endpoint|conformance|validate|fixtures|demo|check|providers> [arguments]")
	return errors.New("invalid command")
}

func repositoryRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func runValidate(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "human", "output format: human or json")

	modeFlag := fs.String("mode", string(validation.ModeStrict), "validation mode: strict (the bundle as published) or tolerant (the extension rules: unknown fields, enum values, and envelope types are accepted)")

	provider := fs.Bool("provider", false, "validate against the model-provider-core profile instead of agent-control-core")

	var packs repeatedFlag
	fs.Var(&packs, "pack", "load an extension pack from a directory containing pack.json; repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *provider && len(packs) > 0 {
		return errors.New("extension packs are an agent-control-core mechanism and do not apply to the provider profile")
	}
	if *format != "human" && *format != "json" {
		return fmt.Errorf("unsupported output format %q", *format)
	}
	mode, err := validation.ParseMode(*modeFlag)
	if err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("validate requires at least one file")
	}
	loaded, err := validation.LoadPacks(packs)
	if err != nil {
		return err
	}
	type traceValidator interface {
		Validate(io.Reader, string) validation.Result
	}
	var validator traceValidator
	if *provider {
		validator, err = validation.NewProviderValidatorWith(mode)
	} else {
		validator, err = validation.NewWith(validation.Options{Mode: mode, Packs: loaded})
	}
	if err != nil {
		return err
	}
	type report struct {
		File        string                  `json:"file"`
		Valid       bool                    `json:"valid"`
		Diagnostics []validation.Diagnostic `json:"diagnostics"`
	}
	reports := make([]report, 0, fs.NArg())
	valid := true
	for _, filename := range fs.Args() {
		file, openErr := os.Open(filename)
		if openErr != nil {
			return openErr
		}
		result := validator.Validate(file, filename)
		if closeErr := file.Close(); closeErr != nil {
			return closeErr
		}
		reports = append(reports, report{File: filename, Valid: result.Valid(), Diagnostics: result.Diagnostics})
		valid = valid && result.Valid()
	}
	if *format == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(reports); err != nil {
			return err
		}
	} else {
		for _, report := range reports {
			if report.Valid {
				fmt.Fprintf(stdout, "PASS %s\n", report.File)
				continue
			}
			fmt.Fprintf(stdout, "FAIL %s\n", report.File)
			for _, diagnostic := range report.Diagnostics {
				fmt.Fprintf(stdout, "  %s\n", diagnostic.Error())
			}
		}
	}
	if !valid {
		return errors.New("validation failed")
	}
	return nil
}

type repeatedFlag []string

func (f *repeatedFlag) String() string     { return strings.Join(*f, ",") }
func (f *repeatedFlag) Set(v string) error { *f = append(*f, v); return nil }

func runFixtures(args []string, stdout io.Writer) error {
	if len(args) > 1 {
		return errors.New("fixtures accepts at most one manifest path")
	}
	manifest := filepath.Join(repositoryRoot(), "fixtures", "manifest.json")
	if len(args) == 1 {
		manifest = args[0]
	}
	validator, err := validation.New()
	if err != nil {
		return err
	}
	outcomes, err := validator.ValidateManifest(manifest)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "PASS fixtures: %d\n", len(outcomes))
	return nil
}

func runProviders(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "zai-cn" {
		fmt.Fprintln(stderr, "usage: goap providers zai-cn [--format=human|json]")
		return errors.New("invalid providers command")
	}
	fs := flag.NewFlagSet("providers zai-cn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "human", "output format: human or json")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*format != "human" && *format != "json") {
		return errors.New("providers zai-cn accepts only --format=human|json")
	}
	presets := provider.ZAIChinaCodingPlan()
	if *format == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(presets)
	}
	for _, preset := range presets {
		fmt.Fprintf(stdout, "%s\t%s\t%s%s\t%s\t%s\n", preset.ID, preset.Wire, preset.BaseURL, preset.Path, preset.Model, preset.EvidenceClass)
	}
	return nil
}

func runDemo(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return errors.New("demo accepts no arguments")
	}
	if err := goldenDemo(ctx, stdout); err != nil {
		return err
	}
	if err := cancellationDemo(ctx, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "PASS demo")
	return nil
}

func runCheck(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return errors.New("check accepts no arguments")
	}
	if _, err := validation.CompileSchemas(); err != nil {
		return fmt.Errorf("schemas: %w", err)
	}
	fmt.Fprintln(stdout, "PASS schemas")
	if err := checkHarnesses(stdout); err != nil {
		return fmt.Errorf("harnesses: %w", err)
	}
	if err := runFixtures(nil, stdout); err != nil {
		return fmt.Errorf("fixtures: %w", err)
	}
	if err := runDemo(ctx, nil, stdout); err != nil {
		return fmt.Errorf("demo: %w", err)
	}
	fmt.Fprintln(stdout, "PASS check")
	return nil
}

type demoClock struct {
	mu sync.Mutex
	n  int64
}

func (c *demoClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return time.UnixMilli(c.n)
}

type demoIDs struct {
	mu sync.Mutex
	n  int
}

func (g *demoIDs) NewID(kind string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("%s-%02d", kind, g.n)
}

func openDemo(ctx context.Context, capacity int) (adapter.Descriptor, adapter.Session, error) {
	implementation := adapter.NewMemory(adapter.Config{Clock: &demoClock{}, IDs: &demoIDs{}, JournalCapacity: capacity})
	descriptor, err := implementation.Probe(ctx)
	if err != nil {
		return adapter.Descriptor{}, nil, err
	}
	session, err := implementation.Open(ctx, adapter.OpenRequest{SessionID: "demo-session", Participant: protocol.Participant{ID: "user"}})
	return descriptor, session, err
}

func goldenDemo(ctx context.Context, stdout io.Writer) error {
	descriptor, session, err := openDemo(ctx, 64)
	if err != nil {
		return err
	}
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{SessionID: "demo-session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run the deterministic demo")}}})
	if err != nil {
		return err
	}
	initial, err := drainStream(stream)
	if err != nil {
		return err
	}
	permission, err := payloadAt[protocol.PermissionRequestedPayload](initial, protocol.TypeActionPermissionRequested)
	if err != nil {
		return err
	}
	if err := session.Resolve(ctx, adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, SessionID: "demo-session", RunID: admission.RunID, RequestedBy: "reference.memory", RespondedBy: "user", ChoiceID: "approve", Granted: true}}); err != nil {
		return err
	}
	middle, err := drainStream(stream)
	if err != nil {
		return err
	}
	input, err := payloadAt[protocol.UserInputRequestedPayload](middle, protocol.TypeUserInputRequested)
	if err != nil {
		return err
	}
	if err := session.Resolve(ctx, adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: input.InteractionID, SessionID: "demo-session", RunID: admission.RunID, RequestedBy: "reference.memory", RespondedBy: "user", Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}}); err != nil {
		return err
	}
	final, err := drainToClose(stream)
	if err != nil {
		return err
	}
	events := append(append(initial, middle...), final...)
	if err := validateEventLifecycle(events, admission.RunID); err != nil {
		return err
	}
	if err := validateDemoTrace(admission, descriptor, events, false); err != nil {
		return err
	}
	recovery, replay, err := session.Resume(ctx, adapter.ResumeRequest{RunID: admission.RunID, AfterSequence: 9})
	if err != nil {
		return err
	}
	replayed, err := drainToClose(replay)
	if err != nil {
		return err
	}
	if recovery.ReplayGap != nil || len(replayed) != 3 || recovery.ReplayedFrom != 10 || recovery.ReplayedThrough != 12 {
		return fmt.Errorf("unexpected retained replay: recovery=%+v events=%d", recovery, len(replayed))
	}
	state, err := session.State(ctx)
	if err != nil {
		return err
	}
	if state.Status != protocol.SessionIdle || state.ActiveRunID != "" {
		return fmt.Errorf("unexpected reconciled state: %+v", state)
	}
	fmt.Fprintf(stdout, "PASS golden: %d events, replay %d-%d, capability %s\n", len(events), recovery.ReplayedFrom, recovery.ReplayedThrough, descriptor.CapabilityRevision)
	return nil
}

func cancellationDemo(ctx context.Context, stdout io.Writer) error {
	descriptor, session, err := openDemo(ctx, 2)
	if err != nil {
		return err
	}
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{SessionID: "demo-session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("cancel")}}})
	if err != nil {
		return err
	}
	initial, err := drainStream(stream)
	if err != nil {
		return err
	}
	ack, err := session.Cancel(ctx, admission.RunID)
	if err != nil {
		return err
	}
	if !ack.Accepted || ack.Status != protocol.RunCancelling {
		return fmt.Errorf("unexpected cancellation acknowledgement: %+v", ack)
	}
	settlement, err := drainToClose(stream)
	if err != nil {
		return err
	}
	events := append(initial, settlement...)
	if err := validateEventLifecycle(events, admission.RunID); err != nil {
		return err
	}
	if err := validateDemoTrace(admission, descriptor, events, ack.Accepted); err != nil {
		return err
	}
	if events[len(events)-1].Type != protocol.TypeRunCancelled {
		return fmt.Errorf("cancellation did not settle as run.cancelled")
	}
	recovery, replay, gapErr := session.Resume(ctx, adapter.ResumeRequest{RunID: admission.RunID, AfterSequence: 0})
	var gap *adapter.ReplayGap
	if !errors.As(gapErr, &gap) || recovery.ReplayGap == nil {
		return fmt.Errorf("expected explicit replay gap, got recovery=%+v err=%v", recovery, gapErr)
	}
	if replayed, err := drainToClose(replay); err != nil || len(replayed) != 0 {
		return fmt.Errorf("gap replay returned events=%d err=%v", len(replayed), err)
	}
	fmt.Fprintf(stdout, "PASS cancellation: intent acknowledged, terminal %s, replay gap %d-%d\n", events[len(events)-1].Type, gap.OldestAvailable, gap.LatestAvailable)
	return nil
}

func drainStream(stream adapter.EventStream) ([]protocol.Envelope, error) {
	var events []protocol.Envelope
	for {
		select {
		case result, ok := <-stream:
			if !ok {
				return events, nil
			}
			if result.Error != nil {
				return nil, result.Error
			}
			events = append(events, result.Envelope)
		default:
			return events, nil
		}
	}
}

func drainToClose(stream adapter.EventStream) ([]protocol.Envelope, error) {
	var events []protocol.Envelope
	for result := range stream {
		if result.Error != nil {
			return nil, result.Error
		}
		events = append(events, result.Envelope)
	}
	return events, nil
}

func payloadAt[T any](events []protocol.Envelope, typ protocol.EnvelopeType) (T, error) {
	var payload T
	for _, event := range events {
		if event.Type != typ {
			continue
		}
		if err := event.DecodePayload(&payload); err != nil {
			return payload, err
		}
		return payload, nil
	}
	return payload, fmt.Errorf("event %s not found", typ)
}

func validateEventLifecycle(events []protocol.Envelope, runID protocol.RunID) error {
	if len(events) == 0 {
		return errors.New("adapter emitted no events")
	}
	terminals := 0
	for index, event := range events {
		want := uint64(index + 1)
		if event.RunID != runID || event.Sequence == nil || *event.Sequence != want {
			return fmt.Errorf("event %d has invalid run scope or sequence", index)
		}
		if event.Type == protocol.TypeRunCompleted || event.Type == protocol.TypeRunFailed || event.Type == protocol.TypeRunCancelled {
			terminals++
			if index != len(events)-1 {
				return errors.New("terminal event was not final")
			}
		}
	}
	if terminals != 1 {
		return fmt.Errorf("adapter emitted %d terminal events", terminals)
	}
	return nil
}

func validateDemoTrace(admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope, cancelled bool) error {
	var trace []byte
	var err error
	if cancelled {
		trace, err = adaptertest.ProtocolTraceWithCancellation(admission, descriptor, events)
	} else {
		trace, err = adaptertest.ProtocolTrace(admission, descriptor, events)
	}
	if err != nil {
		return err
	}
	if result := validation.MustNew().ValidateBytes(trace, "oap-demo"); !result.Valid() {
		return fmt.Errorf("demo trace failed OAP validation: %v", result.Diagnostics)
	}
	return nil
}
