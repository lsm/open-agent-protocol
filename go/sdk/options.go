package sdk

import (
	"io"
	"log/slog"
	"time"
)

const (
	defaultHandshakeTimeout = 5 * time.Second

	defaultRequestTimeout = 30 * time.Second

	defaultShutdownGrace = 2 * time.Second
)

type Options struct {
	BinaryPath string

	BinaryURL string

	ChecksumSHA256 string

	CacheDir string

	Args []string

	Dir string

	Env []string

	ExpectedProtocolVersion string

	HandshakeTimeout time.Duration

	RequestTimeout time.Duration

	ShutdownGrace time.Duration

	Logger *slog.Logger
}

func (o *Options) args() []string {
	if o == nil || o.Args == nil {
		return []string{"serve", "agent,provider", "--stdio"}
	}
	return o.Args
}

func (o *Options) protocolVersion() string {
	if o == nil || o.ExpectedProtocolVersion == "" {
		return "0.1"
	}
	return o.ExpectedProtocolVersion
}

func (o *Options) handshakeTimeout() time.Duration {
	if o == nil || o.HandshakeTimeout <= 0 {
		return defaultHandshakeTimeout
	}
	return o.HandshakeTimeout
}

func (o *Options) requestTimeout() time.Duration {
	if o == nil || o.RequestTimeout <= 0 {
		return defaultRequestTimeout
	}
	return o.RequestTimeout
}

func (o *Options) shutdownGraceOrDefault() time.Duration {
	if o == nil || o.ShutdownGrace <= 0 {
		return defaultShutdownGrace
	}
	return o.ShutdownGrace
}

func (o *Options) logger() *slog.Logger {
	if o == nil || o.Logger == nil {
		return discardLogger
	}
	return o.Logger
}

var discardLogger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
