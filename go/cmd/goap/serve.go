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

	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
	"github.com/lsm/open-agent-protocol/go/serve/servehttp"
	"github.com/lsm/open-agent-protocol/go/serve/servestdio"
)

func runHub(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "adapter registry JSON path (default: built-in memory adapter)")
	addr := fs.String("addr", servehttp.DefaultAddr, "listen address")
	overStdio := fs.Bool("stdio", false, "serve newline-delimited JSON on stdin/stdout instead of listening on a port")
	bindingsPath := fs.String("session-history", "", "append-only JSONL file recording every session this hub opens, reopens and closes, so a host can reopen what it opened")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("hub accepts no positional arguments")
	}

	addrSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			addrSet = true
		}
	})
	if *overStdio && addrSet {
		return errors.New("hub --stdio takes no listen address; --addr and --stdio are mutually exclusive")
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

	caught := make(chan os.Signal, 4)
	signal.Notify(caught, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(caught)
	signals, stop := context.WithCancel(ctx)
	defer stop()

	options := serve.Options{
		Logger: log.New(stderr, "goap: ", 0),
		Home:   homeDirectory(),
	}
	if *bindingsPath != "" {
		held, err := lockSessionHistory(*bindingsPath)
		if err != nil {
			return err
		}
		defer held.Close()
		store, err := binding.File(*bindingsPath)
		if err != nil {
			return err
		}
		options.Bindings = store
	}
	hub := serve.New(registry, options)
	if *overStdio {
		return serveStdio(signals, stop, caught, hub, registry, stdin, stdout, stderr)
	}
	daemon, err := servehttp.New(hub, servehttp.Options{
		HostAllowlist: loopbackHosts(*addr),
	})
	if err != nil {
		return err
	}
	go relaySignals(signals, stop, caught, daemon, stderr)
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
	fmt.Fprintf(stderr, "goap: serving adapters: %s (restart kills all sessions)\n", strings.Join(registry.Names(), ", "))

	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()

	select {
	case err := <-serveDone:

		daemon.Close()
		settle, cancelSettle := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
		defer cancelSettle()
		hub.CloseSessions(settle)
		return err
	case <-signals.Done():
	case <-daemon.Done():
	}

	fmt.Fprintln(stderr, "goap: shutting down")
	httpShutdown, cancelHTTP := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelHTTP()
	if err := httpServer.Shutdown(httpShutdown); err != nil {
		fmt.Fprintf(stderr, "goap: http shutdown: %v\n", err)
	}

	daemon.Close()
	sessionShutdown, cancelSessions := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelSessions()
	hub.CloseSessions(sessionShutdown)
	fmt.Fprintln(stderr, "goap: stopped")
	return nil
}

type signalTaker interface {
	Signal(ctx context.Context, count int) (protocol.EndpointShutdownResponse, bool)
}

func relaySignals(ctx context.Context, stop context.CancelFunc, caught <-chan os.Signal, taker signalTaker, stderr io.Writer) {
	count := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-caught:
		}
		count++
		if count > 2 {
			stop()
			return
		}
		go applySignal(ctx, taker, count, stderr)
	}
}

func applySignal(ctx context.Context, taker signalTaker, count int, stderr io.Writer) {
	answer, ok := taker.Signal(ctx, count)
	if !ok {
		return
	}
	if count == 1 {
		fmt.Fprintf(stderr, "goap: shutting down gracefully, %d run(s) still active, %d queued run(s) lost; signal again to force\n", len(answer.ActiveRuns), len(answer.Lost))
		return
	}
	fmt.Fprintf(stderr, "goap: forced shutdown, %d run(s) cut off\n", len(answer.ActiveRuns))
}

func serveStdio(ctx context.Context, stop context.CancelFunc, caught <-chan os.Signal, hub *serve.Hub, registry *serve.Registry, stdin io.Reader, stdout, stderr io.Writer) error {
	frontend, err := servestdio.New(hub, servestdio.Options{
		Logger: log.New(stderr, "goap: ", 0),
	})
	if err != nil {
		return err
	}
	go relaySignals(ctx, stop, caught, frontend, stderr)
	if stdin == nil {
		return errors.New("hub --stdio needs stdin")
	}
	fmt.Fprintf(stderr, "goap: serving adapters over stdio: %s (exit kills all sessions)\n", strings.Join(registry.Names(), ", "))
	runErr := frontend.Run(ctx, stdin, stdout)

	sessionShutdown, cancelSessions := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelSessions()
	hub.CloseSessions(sessionShutdown)

	if runErr != nil {
		return runErr
	}
	fmt.Fprintln(stderr, "goap: stopped")
	return nil
}

func loopbackHosts(addr string) []string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return []string{"localhost", "127.0.0.1", "::1"}
	default:

		return nil
	}
}

func homeDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
