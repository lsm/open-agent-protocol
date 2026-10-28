package rpc

import (
	"context"
	"errors"
	"io"

	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
)

var ErrClosed = errors.New("Copilot RPC closed")

type Client struct{}

func NewClient(io.ReadWriteCloser, int, int) *Client         { return &Client{} }
func (*Client) Call(context.Context, string, any, any) error { return ErrClosed }
func (*Client) Notifications() <-chan native.Message         { return nil }
func (*Client) Done() <-chan struct{}                        { return nil }
func (*Client) Err() error                                   { return ErrClosed }
func (*Client) Close() error                                 { return nil }
