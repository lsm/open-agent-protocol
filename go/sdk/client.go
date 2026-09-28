package sdk

import (
	"context"
	"time"
)

type Client struct {
	Auth *AuthService

	Models *ModelsService

	Provider *ProviderService

	Agent *AgentService

	transport *transport
}

func New(ctx context.Context, opts *Options) (*Client, error) {
	if opts == nil {
		opts = &Options{}
	}
	command, err := ResolveBinary(ctx, opts)
	if err != nil {
		return nil, err
	}
	transport, err := startTransport(ctx, command, opts)
	if err != nil {
		return nil, err
	}
	return newClient(transport, opts.requestTimeout()), nil
}

func newClient(transport *transport, timeout time.Duration) *Client {
	client := &Client{transport: transport}
	client.Auth = &AuthService{transport: transport, timeout: timeout}
	client.Models = &ModelsService{transport: transport, timeout: timeout}
	client.Provider = &ProviderService{transport: transport, timeout: timeout}
	client.Agent = &AgentService{transport: transport, timeout: timeout}
	return client
}

func (c *Client) Close() error { return c.transport.close() }
