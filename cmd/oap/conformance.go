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

	"github.com/lsm/open-agent-protocol/conformance"
	"github.com/lsm/open-agent-protocol/protocol"
)

// runConformance drives an endpoint binary through the stdio binding and
// reports whether it conformed.
//
// The command is spawned as a process rather than linked in, which is the
// whole point: an implementer's endpoint is a binary in whatever language
// they wrote it, and a runner only Go implementers can use would be half a
// deliverable. With no --command it re-executes this binary as its own
// reference endpoint, so the runner always has a known-good target.
func runConformance(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("conformance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	command := fs.String("command", "", "endpoint command to drive (default: this binary's own reference endpoint)")
	adapterName := fs.String("adapter", "memory", "adapter for the built-in reference endpoint when --command is not given")
	sessionID := fs.String("session", "conformance", "session id to open")
	format := fs.String("format", "text", "text or json")
	verbose := fs.Bool("verbose", false, "print the endpoint's stderr")
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
		Command:   argv,
		SessionID: protocol.SessionID(*sessionID),
		Stderr:    childStderr,
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
		}
	}
	if !report.Passed {
		return errors.New("endpoint is not conformant")
	}
	return nil
}

// endpointCommand resolves the argv to spawn. Splitting on spaces is enough
// for the shapes this is used with and keeps the flag readable; a command
// needing more than that can be wrapped in a script.
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
	return []string{self, "endpoint", "--adapter", adapterName}, nil
}
