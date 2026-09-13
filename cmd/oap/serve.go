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
	"time"

	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/serve/servehttp"
)

// runServe starts the single-user local daemon over the adapter registry and
// blocks until SIGINT/SIGTERM. Restarting the daemon kills every session: run
// child processes are per-session and no adapter here is wired for native
// cross-restart resume.
func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "adapter registry JSON path (default: built-in memory adapter)")
	addr := fs.String("addr", servehttp.DefaultAddr, "listen address")
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

	hub := serve.New(registry, serve.Options{
		Logger: log.New(stderr, "oap: ", 0),
	})
	daemon, err := servehttp.New(hub, servehttp.Options{
		HostAllowlist: loopbackHosts(*addr),
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           daemon.Handler(),
		BaseContext:       func(net.Listener) context.Context { return signals },
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
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
		// The accept loop died on its own: still settle any sessions that
		// were opened before returning the error.
		settle, cancelSettle := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
		defer cancelSettle()
		hub.CloseSessions(settle)
		return err
	case <-signals.Done():
	}

	// The HTTP sweep and the session sweep get independent budgets: a
	// stalled SSE client can consume the whole HTTP window inside its write,
	// and handing the session sweep an already-expired context would skip
	// adapter Close entirely, orphaning child agent processes.
	fmt.Fprintln(stderr, "oap: shutting down")
	httpShutdown, cancelHTTP := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelHTTP()
	if err := httpServer.Shutdown(httpShutdown); err != nil {
		fmt.Fprintf(stderr, "oap: http shutdown: %v\n", err)
	}
	sessionShutdown, cancelSessions := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelSessions()
	hub.CloseSessions(sessionShutdown)
	fmt.Fprintln(stderr, "oap: stopped")
	return nil
}

// loopbackHosts returns the Host-header names a loopback bind serves, or nil
// when the operator bound a non-loopback or wildcard address and thereby
// opted out of the single-user trust model.
func loopbackHosts(addr string) []string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return []string{"localhost", "127.0.0.1", "::1"}
	default:
		// The empty host binds every interface, and a Host allowlist is
		// worthless against non-browser clients who choose their own Host;
		// only an explicit loopback bind keeps the restriction meaningful.
		return nil
	}
}
