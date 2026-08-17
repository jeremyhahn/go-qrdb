//go:build qrdb_sql

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

package driver

import (
	"context"
	"database/sql/driver"
	"io"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

// Compile-time interface assertions.
var (
	_ driver.Connector = (*NetworkConnector)(nil)
	_ io.Closer        = (*NetworkConnector)(nil)
)

// sqlTransport is the behaviour the network connector needs from an SDK
// transport: a SQL surface, plus connection lifecycle.
type sqlTransport interface {
	transport.SQLProvider
	Connect(ctx context.Context) error
	Close() error
}

// transportFactories maps a DSN scheme to the SDK transport that serves it.
//
// Routing through the SDK transports rather than a hand-rolled HTTP client is
// what makes grpc:// actually speak gRPC — the previous implementation sent
// REST requests for both schemes — and it is also what supplies TLS, request
// authentication, and leader forwarding, none of which the hand-rolled client
// performed.
var transportFactories = map[string]func(...transport.Option) (sqlTransport, error){
	SchemeREST: func(opts ...transport.Option) (sqlTransport, error) {
		return resttransport.New(opts...)
	},
	SchemeGRPC: func(opts ...transport.Option) (sqlTransport, error) {
		return grpctransport.New(opts...)
	},
}

// NetworkConnector creates connections to a remote qrdb server over the
// transport named by the DSN scheme.
type NetworkConnector struct {
	cfg *DSNConfig

	mu        sync.Mutex
	transport sqlTransport
	closed    atomic.Bool
}

// NewNetworkConnector creates a new NetworkConnector from the given DSN
// configuration. The transport is established on the first Connect.
func NewNetworkConnector(cfg *DSNConfig) *NetworkConnector {
	return &NetworkConnector{cfg: cfg}
}

// Connect returns a new connection to the remote qrdb server, establishing
// the shared transport on first use.
func (c *NetworkConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.closed.Load() {
		return nil, &ConnectorClosedError{}
	}

	tr, err := c.getOrCreateTransport(ctx)
	if err != nil {
		return nil, err
	}

	return &networkConn{
		sqlClient: tr.SQLClient(),
		database:  c.cfg.Database,
		scheme:    c.cfg.Scheme,
	}, nil
}

// Driver returns the driver that created this connector.
func (c *NetworkConnector) Driver() driver.Driver {
	return &Driver{}
}

// Close releases the underlying transport.
func (c *NetworkConnector) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport == nil {
		return nil
	}
	err := c.transport.Close()
	c.transport = nil
	return err
}

// getOrCreateTransport lazily builds and connects the SDK transport.
func (c *NetworkConnector) getOrCreateTransport(ctx context.Context) (sqlTransport, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.transport != nil {
		return c.transport, nil
	}

	factory, ok := transportFactories[c.cfg.Scheme]
	if !ok {
		return nil, &UnsupportedSchemeError{Scheme: c.cfg.Scheme}
	}

	opts := []transport.Option{transport.WithAddress(c.cfg.Host)}
	if c.cfg.CAFile != "" || c.cfg.CertFile != "" || c.cfg.KeyFile != "" {
		opts = append(opts, transport.WithTLS(c.cfg.CertFile, c.cfg.KeyFile, c.cfg.CAFile))
	}

	tr, err := factory(opts...)
	if err != nil {
		return nil, err
	}
	if err := tr.Connect(ctx); err != nil {
		_ = tr.Close()
		return nil, err
	}

	c.transport = tr
	return tr, nil
}
