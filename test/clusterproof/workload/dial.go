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

//go:build clusterproof

package workload

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

// KV is the slice of the SDK's guarded key-value surface the workload drives.
// transport.GuardedKVClient satisfies it, so every SDK transport that carries
// the guarded verbs does.
type KV interface {
	Put(ctx context.Context, key string, value []byte, opts ...transport.WriteOption) error
	Get(ctx context.Context, key string, opts ...transport.ReadOption) ([]byte, error)
	Delete(ctx context.Context, key string, opts ...transport.WriteOption) error
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
}

var _ KV = (transport.GuardedKVClient)(nil)

// Conn is one client's connection to one node.
type Conn interface {
	KV
	Close() error
}

// Dialer opens a connection to one endpoint. It is the seam between the
// workload and a cluster: NewTransportDialer reaches real nodes over the SDK's
// network transports, and a test passes a dialer onto an in-process node or a
// fake.
type Dialer func(ctx context.Context, endpoint string) (Conn, error)

// transportCtor builds an unconnected SDK transport.
type transportCtor func(opts ...transport.Option) (transport.Transport, error)

// transports maps a Config.Protocol to its SDK transport.
var transports = map[string]transportCtor{
	"quic": func(opts ...transport.Option) (transport.Transport, error) { return quic.New(opts...) },
	"grpc": func(opts ...transport.Option) (transport.Transport, error) { return grpc.New(opts...) },
	"rest": func(opts ...transport.Option) (transport.Transport, error) { return rest.New(opts...) },
}

// Connector opens a connected SDK transport to one endpoint. The soak uses it
// to reach a node's admin surface over the same transport and credentials the
// workload's clients use.
type Connector func(ctx context.Context, endpoint string) (transport.Transport, error)

// NewTransportConnector returns a Connector over the configured SDK
// transport, with the configured credentials.
func NewTransportConnector(cfg Config) (Connector, error) {
	ctor, ok := transports[cfg.Protocol]
	if !ok {
		return nil, &UnknownProtocolError{Protocol: cfg.Protocol}
	}
	creds := cfg.Credentials
	timeout := time.Duration(cfg.OpTimeout)
	return func(ctx context.Context, endpoint string) (transport.Transport, error) {
		opts := []transport.Option{transport.WithAddress(endpoint), transport.WithTimeout(timeout)}
		if creds.CAFile != "" {
			opts = append(opts, transport.WithTLS(creds.CertFile, creds.KeyFile, creds.CAFile))
		}
		t, err := ctor(opts...)
		if err != nil {
			return nil, &DialError{Endpoint: endpoint, Err: err}
		}
		if err := t.Connect(ctx); err != nil {
			_ = t.Close()
			return nil, &DialError{Endpoint: endpoint, Err: err}
		}
		return t, nil
	}, nil
}

// NewTransportDialer returns a Dialer that connects to an endpoint over the
// configured SDK transport, with the configured credentials.
func NewTransportDialer(cfg Config) (Dialer, error) {
	connect, err := NewTransportConnector(cfg)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, endpoint string) (Conn, error) {
		t, err := connect(ctx, endpoint)
		if err != nil {
			return nil, err
		}
		kv, ok := t.(transport.GuardedKVClient)
		if !ok {
			_ = t.Close()
			return nil, &UnsupportedTransportError{Protocol: cfg.Protocol}
		}
		return transportConn{GuardedKVClient: kv, t: t}, nil
	}, nil
}

// transportConn closes the transport it was dialed on.
type transportConn struct {
	transport.GuardedKVClient
	t transport.Transport
}

func (c transportConn) Close() error { return c.t.Close() }
