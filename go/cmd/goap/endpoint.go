package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lsm/open-agent-protocol/go/serve"
	"github.com/lsm/open-agent-protocol/go/serve/serveendpoint"
)

func runServeRole(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "usage: goap serve agent [--backend B] [--config F] [--stdio]; the multi-session daemon is goap hub")
		return errors.New("serve needs a role")
	}
	switch args[0] {
	case "agent":
		return runEndpoint(ctx, "serve agent", "backend", args[1:], stdin, stdout, stderr)
	case "provider", "agent,provider", "provider,agent":
		return fmt.Errorf("unavailable: goap does not carry serve %s", args[0])
	default:
		fmt.Fprintln(stderr, "usage: goap serve agent [--backend B] [--config F] [--stdio]; the multi-session daemon is goap hub")
		return fmt.Errorf("unknown serve role %q", args[0])
	}
}

func runEndpoint(ctx context.Context, verb, backendFlag string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "adapter registry JSON path (default: built-in memory adapter)")
	adapterName := fs.String(backendFlag, "memory", "the single adapter this endpoint exposes")
	fs.Bool("stdio", true, "carry raw envelopes on stdin/stdout, the only transport this verb has")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%s accepts no positional arguments", verb)
	}
	if stdin == nil {
		return fmt.Errorf("%s needs stdin", verb)
	}

	var registry *serve.Registry
	var err error
	if *configPath == "" {
		registry, err = serve.DefaultRegistry()
	} else {
		registry, err = serve.LoadRegistry(*configPath, os.LookupEnv)
	}
	if err != nil {
		return err
	}

	signals, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := serve.New(registry, serve.Options{Logger: log.New(stderr, "goap: ", 0)})
	endpoint, err := serveendpoint.New(hub, serveendpoint.Options{
		Adapter: *adapterName,
		Logger:  log.New(stderr, "goap: ", 0),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "goap: endpoint over adapter %q (exit closes the session)\n", *adapterName)

	runErr := endpoint.Run(signals, stdin, stdout)

	sweep, cancelSweep := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelSweep()
	hub.CloseSessions(sweep)

	if runErr != nil {
		return runErr
	}
	fmt.Fprintln(stderr, "goap: stopped")
	return nil
}
