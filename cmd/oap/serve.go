package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/lsm/open-agent-protocol/internal/serve"
)

// runServe starts the single-user local daemon over the adapter registry and
// blocks until SIGINT/SIGTERM. Restarting the daemon kills every session: run
// child processes are per-session and no adapter here is wired for native
// cross-restart resume.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "adapter registry JSON path (default: built-in memory adapter)")
	addr := fs.String("addr", serve.DefaultAddr, "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("serve accepts no positional arguments")
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

	// The signal context is also the base context of every request, so
	// long-lived SSE streams terminate the moment shutdown begins.
	signals, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	daemon, err := serve.New(registry, serve.Options{Logger: log.New(stderr, "oap: ", 0)})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:     daemon.Handler(),
		BaseContext: func(net.Listener) context.Context { return signals },
	}
	listener, listenErr := net.Listen("tcp", *addr)
	if listenErr != nil {
		return listenErr
	}
	fmt.Fprintf(stdout, "listening on http://%s\n", listener.Addr())
	fmt.Fprintf(stderr, "oap: serving adapters: %s (restart kills all sessions)\n", strings.Join(registry.Names(), ", "))

	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()

	select {
	case err := <-serveDone:
		return err
	case <-signals.Done():
	}

	// A fresh context bounds shutdown: the request contexts are already
	// canceled through the signal context, and closing each adapter session
	// settles child processes rather than orphaning them.
	fmt.Fprintln(stderr, "oap: shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdown); err != nil {
		fmt.Fprintf(stderr, "oap: http shutdown: %v\n", err)
	}
	daemon.CloseSessions(shutdown)
	fmt.Fprintln(stderr, "oap: stopped")
	return nil
}
