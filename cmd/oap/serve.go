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
	"github.com/lsm/open-agent-protocol/serve/servestdio"
)

// runServe starts the single-user local daemon over the adapter registry and
// blocks until SIGINT/SIGTERM. Restarting the daemon kills every session: run
// child processes are per-session and no adapter here is wired for native
// cross-restart resume.
func runServe(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "adapter registry JSON path (default: built-in memory adapter)")
	addr := fs.String("addr", servehttp.DefaultAddr, "listen address")
	overStdio := fs.Bool("stdio", false, "serve newline-delimited JSON on stdin/stdout instead of listening on a port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("serve accepts no positional arguments")
	}
	// Exclusivity is decided on what the operator actually wrote, not on the
	// value: --addr carries a default, so comparing against it would refuse
	// every plain --stdio invocation and silently accept the one case that is
	// genuinely ambiguous.
	addrSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			addrSet = true
		}
	})
	if *overStdio && addrSet {
		return errors.New("serve --stdio takes no listen address; --addr and --stdio are mutually exclusive")
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
	if *overStdio {
		return serveStdio(signals, hub, registry, stdin, stdout, stderr)
	}
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

// serveStdio serves one host over the process's own stdin and stdout, the
// spawn-a-binary embedding: the parent process launched this one, and that
// launch is the authorization, so there is no port, no TLS and no Host
// allowlist to establish.
//
// Nothing but protocol lines may reach stdout. The banner the HTTP path
// writes there — the address a caller needs — has no counterpart here, since
// the caller already holds both pipes; what is left is the adapter list, and
// that goes to stderr with the rest of the diagnostics.
//
// The session sweep runs on every exit, including the malformed-line exit.
// The frontend's own teardown settles the work it admitted, but the sessions
// on the hub outlive it, and skipping the sweep on the error path would
// orphan the child agent processes a session holds — which is exactly the
// path a host hits when its encoder is broken.
func serveStdio(ctx context.Context, hub *serve.Hub, registry *serve.Registry, stdin io.Reader, stdout, stderr io.Writer) error {
	frontend, err := servestdio.New(hub, servestdio.Options{
		Logger: log.New(stderr, "oap: ", 0),
	})
	if err != nil {
		return err
	}
	if stdin == nil {
		return errors.New("serve --stdio needs stdin")
	}
	fmt.Fprintf(stderr, "oap: serving adapters over stdio: %s (exit kills all sessions)\n", strings.Join(registry.Names(), ", "))
	runErr := frontend.Run(ctx, stdin, stdout)

	sessionShutdown, cancelSessions := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelSessions()
	hub.CloseSessions(sessionShutdown)

	// A malformed line is the host's framing defect and the one outcome that
	// must not look like a clean end: it is returned so the process exits
	// non-zero, and the dispatcher prints it as the single bounded
	// diagnostic. Every other end — stdin EOF, a signal — is a normal one.
	if runErr != nil {
		return runErr
	}
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
