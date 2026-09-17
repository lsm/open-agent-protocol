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
	"syscall"

	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/serve/serveendpoint"
)

func runEndpoint(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("endpoint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "adapter registry JSON path (default: built-in memory adapter)")
	adapterName := fs.String("adapter", "memory", "the single adapter this endpoint exposes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("endpoint accepts no positional arguments")
	}
	if stdin == nil {
		return errors.New("endpoint needs stdin")
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

	hub := serve.New(registry, serve.Options{Logger: log.New(stderr, "oap: ", 0)})
	endpoint, err := serveendpoint.New(hub, serveendpoint.Options{
		Adapter: *adapterName,
		Logger:  log.New(stderr, "oap: ", 0),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "oap: endpoint over adapter %q (exit closes the session)\n", *adapterName)

	runErr := endpoint.Run(signals, stdin, stdout)

	sweep, cancelSweep := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelSweep()
	hub.CloseSessions(sweep)

	if runErr != nil {
		return runErr
	}
	fmt.Fprintln(stderr, "oap: stopped")
	return nil
}
