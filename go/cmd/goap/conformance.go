package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/lsm/open-agent-protocol/go/conformance"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/validation"
)

func runConformance(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("conformance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	command := fs.String("command", "", "endpoint command to drive (default: this binary's own reference endpoint)")
	adapterName := fs.String("adapter", "memory", "adapter for the built-in reference endpoint when --command is not given")
	sessionID := fs.String("session", "conformance", "session id to open")
	format := fs.String("format", "text", "text or json")
	verbose := fs.Bool("verbose", false, "print the endpoint's stderr")
	model := fs.String("model", "", "model id for the scripted submission (default: the endpoint's own catalog, else none)")
	traceOut := fs.String("trace-out", "", "write the assembled trace to this file, so a diagnostic can be read against the envelope it anchors to")
	timeout := fs.Duration("timeout", conformance.DefaultLineDeadline, "how long to wait for each line the endpoint writes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("conformance accepts no positional arguments")
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("unknown format %q", *format)
	}

	argv, err := endpointCommand(*command, *adapterName)
	if err != nil {
		return err
	}
	childStderr := io.Discard
	if *verbose {
		childStderr = stderr
	}

	report, err := conformance.Run(ctx, conformance.Options{
		Command:      argv,
		SessionID:    protocol.SessionID(*sessionID),
		Stderr:       childStderr,
		LineDeadline: *timeout,
		Model:        *model,
	})
	if err != nil {
		return err
	}

	if *format == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "endpoint: %s\n", strings.Join(argv, " "))
		for _, check := range report.Checks {
			mark := "PASS"
			if !check.Passed {
				mark = "FAIL"
			}
			fmt.Fprintf(stdout, "%s %s\n", mark, check.Name)
			if check.Detail != "" {
				fmt.Fprintf(stdout, "     %s\n", check.Detail)
			}
		}
		for _, diagnostic := range report.Diagnostics {

			fmt.Fprintf(stdout, "     %s: %s\n", diagnostic.Code, diagnostic.Message)
			fmt.Fprintf(stdout, "       at %s\n", diagnosticAnchor(diagnostic))
		}
		if *traceOut == "" && len(report.Diagnostics) > 0 {
			fmt.Fprintf(stdout, "     (run again with --trace-out=trace.json to read the assembled trace)\n")
		}
	}
	if *traceOut != "" {
		encoded, err := json.MarshalIndent(report.Trace, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(*traceOut, append(encoded, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "trace: %s (%d envelopes)\n", *traceOut, len(report.Trace))
	}
	if !report.Passed {
		return errors.New("endpoint is not conformant")
	}
	return nil
}

func diagnosticAnchor(d validation.Diagnostic) string {
	parts := []string{fmt.Sprintf("envelope %d", d.Index)}
	if d.Type != "" {
		parts = append(parts, d.Type)
	}
	if d.EnvelopeID != "" {
		parts = append(parts, "id "+d.EnvelopeID)
	}
	if d.Pointer != "" {
		parts = append(parts, d.Pointer)
	}
	return strings.Join(parts, " · ")
}

func endpointCommand(command, adapterName string) ([]string, error) {
	if command != "" {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return nil, errors.New("--command is empty")
		}
		return fields, nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating this binary to run its own endpoint: %w", err)
	}
	return []string{self, "serve", "agent", "--backend", adapterName}, nil
}
