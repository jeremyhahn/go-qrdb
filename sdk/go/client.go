// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import (
	"context"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Client is the top-level QRDB SDK client. It wraps a transport and
// exposes typed KV and Admin service interfaces. The transport determines
// the communication mechanism (REST, gRPC, embedded, etc.).
type Client struct {
	transport transport.Transport
	closed    atomic.Bool
}

// NewClient creates a new SDK client using the given transport.
// The transport must already be constructed; use the appropriate
// transport package (rest, embedded, grpc) to create one.
// Returns an error if transport is nil.
func NewClient(t transport.Transport) (*Client, error) {
	if t == nil {
		return nil, &NilTransportError{}
	}
	return &Client{
		transport: t,
	}, nil
}

// Connect establishes the transport connection. For network transports
// this performs the initial handshake; for embedded transports this is
// a no-op.
func (c *Client) Connect(ctx context.Context) error {
	if c.closed.Load() {
		return &ClientClosedError{}
	}
	return c.transport.Connect(ctx)
}

// KV returns a KVClient for data-plane key-value operations.
func (c *Client) KV() transport.KVClient {
	return c.transport.(transport.KVClient)
}

// Admin returns an AdminClient for control-plane cluster operations.
func (c *Client) Admin() transport.AdminClient {
	return c.transport.(transport.AdminClient)
}

// Healthy returns true if the transport connection is healthy.
func (c *Client) Healthy(ctx context.Context) bool {
	if c.closed.Load() {
		return false
	}
	return c.transport.Healthy(ctx)
}

// Close shuts down the client and releases all resources held by the
// underlying transport. After Close returns, all operations will return
// ClientClosedError.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil // already closed
	}
	return c.transport.Close()
}
