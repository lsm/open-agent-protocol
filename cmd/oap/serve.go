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

		daemon.Close()
		settle, cancelSettle := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
		defer cancelSettle()
		hub.CloseSessions(settle)
		return err
	case <-signals.Done():
	}

	fmt.Fprintln(stderr, "oap: shutting down")
	httpShutdown, cancelHTTP := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelHTTP()
	if err := httpServer.Shutdown(httpShutdown); err != nil {
		fmt.Fprintf(stderr, "oap: http shutdown: %v\n", err)
	}

	daemon.Close()
	sessionShutdown, cancelSessions := context.WithTimeout(context.Background(), serve.DefaultShutdownTimeout)
	defer cancelSessions()
	hub.CloseSessions(sessionShutdown)
	fmt.Fprintln(stderr, "oap: stopped")
	return nil
}

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

	if runErr != nil {
		return runErr
	}
	fmt.Fprintln(stderr, "oap: stopped")
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
