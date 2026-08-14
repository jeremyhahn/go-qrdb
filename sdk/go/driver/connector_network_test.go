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
	"errors"
	"io"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// stubTransport is a sqlTransport that records lifecycle calls, standing in
// for a real SDK transport so connector behaviour can be asserted without a
// server.
type stubTransport struct {
	client       transport.SQLClient
	connectErr   error
	connectCalls int
	closeCalls   int
}

func (s *stubTransport) SQLClient() transport.SQLClient { return s.client }

func (s *stubTransport) Connect(_ context.Context) error {
	s.connectCalls++
	return s.connectErr
}

func (s *stubTransport) Close() error {
	s.closeCalls++
	return nil
}

// withStubTransport installs a stub factory for the given scheme for the
// duration of the test, restoring the real factory afterwards.
func withStubTransport(t *testing.T, scheme string, stub *stubTransport) {
	t.Helper()

	original, existed := transportFactories[scheme]
	transportFactories[scheme] = func(...transport.Option) (sqlTransport, error) {
		return stub, nil
	}
	t.Cleanup(func() {
		if existed {
			transportFactories[scheme] = original
			return
		}
		delete(transportFactories, scheme)
	})
}

// TestNetworkConnector_InterfaceCompliance verifies the connector satisfies
// the interfaces database/sql expects.
func TestNetworkConnector_InterfaceCompliance(t *testing.T) {
	var _ driver.Connector = (*NetworkConnector)(nil)
	var _ io.Closer = (*NetworkConnector)(nil)
}

// TestNetworkConnector_Driver verifies Driver returns the qrdb driver.
func TestNetworkConnector_Driver(t *testing.T) {
	cfg, err := ParseDSN("rest://localhost:63001/mydb")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}
	if NewNetworkConnector(cfg).Driver() == nil {
		t.Error("Driver() = nil")
	}
}

// TestNetworkConnector_Connect verifies the connection carries the configured
// database and scheme and is backed by the transport's SQL client.
func TestNetworkConnector_Connect(t *testing.T) {
	stub := &stubTransport{client: &fakeSQLClient{}}
	withStubTransport(t, SchemeREST, stub)

	cfg, err := ParseDSN("rest://localhost:63001/mydb")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	connector := NewNetworkConnector(cfg)
	defer func() { _ = connector.Close() }()

	conn, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	nc, ok := conn.(*networkConn)
	if !ok {
		t.Fatalf("conn = %T, want *networkConn", conn)
	}
	if nc.database != "mydb" {
		t.Errorf("database = %q, want mydb", nc.database)
	}
	if nc.scheme != SchemeREST {
		t.Errorf("scheme = %q, want %q", nc.scheme, SchemeREST)
	}
	if nc.sqlClient != stub.client {
		t.Error("connection is not backed by the transport's SQL client")
	}
}

// TestNetworkConnector_GRPCUsesGRPCTransport verifies a grpc:// DSN builds a
// gRPC transport.
//
// This is a regression guard: both schemes previously routed to the same
// hand-rolled HTTP client, so a grpc:// DSN silently spoke REST.
func TestNetworkConnector_GRPCUsesGRPCTransport(t *testing.T) {
	restStub := &stubTransport{client: &fakeSQLClient{}}
	grpcStub := &stubTransport{client: &fakeSQLClient{}}
	withStubTransport(t, SchemeREST, restStub)
	withStubTransport(t, SchemeGRPC, grpcStub)

	cfg, err := ParseDSN("grpc://server:50051/mydb")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	connector := NewNetworkConnector(cfg)
	defer func() { _ = connector.Close() }()

	if _, err := connector.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if grpcStub.connectCalls != 1 {
		t.Errorf("gRPC transport connect calls = %d, want 1", grpcStub.connectCalls)
	}
	if restStub.connectCalls != 0 {
		t.Errorf("REST transport was used for a grpc:// DSN (%d calls)", restStub.connectCalls)
	}
}

// TestNetworkConnector_TransportSharedAcrossConnections verifies the transport
// is built once and reused, rather than reconnected per connection.
func TestNetworkConnector_TransportSharedAcrossConnections(t *testing.T) {
	stub := &stubTransport{client: &fakeSQLClient{}}
	withStubTransport(t, SchemeREST, stub)

	cfg, _ := ParseDSN("rest://localhost:63001/mydb")
	connector := NewNetworkConnector(cfg)
	defer func() { _ = connector.Close() }()

	for i := 0; i < 3; i++ {
		if _, err := connector.Connect(context.Background()); err != nil {
			t.Fatalf("Connect %d: %v", i, err)
		}
	}

	if stub.connectCalls != 1 {
		t.Errorf("transport connect calls = %d, want 1", stub.connectCalls)
	}
}

// TestNetworkConnector_ConnectFailureClosesTransport verifies a transport that
// fails to connect is closed rather than leaked, and is not cached.
func TestNetworkConnector_ConnectFailureClosesTransport(t *testing.T) {
	sentinel := errors.New("dial failed")
	stub := &stubTransport{client: &fakeSQLClient{}, connectErr: sentinel}
	withStubTransport(t, SchemeREST, stub)

	cfg, _ := ParseDSN("rest://localhost:63001/mydb")
	connector := NewNetworkConnector(cfg)
	defer func() { _ = connector.Close() }()

	_, err := connector.Connect(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Connect error = %v, want %v", err, sentinel)
	}
	if stub.closeCalls != 1 {
		t.Errorf("transport close calls = %d, want 1 (transport leaked)", stub.closeCalls)
	}

	// A second attempt must retry rather than reuse a failed transport.
	_, _ = connector.Connect(context.Background())
	if stub.connectCalls != 2 {
		t.Errorf("connect calls = %d, want 2 (failed transport was cached)", stub.connectCalls)
	}
}

// TestNetworkConnector_CloseReleasesTransport verifies Close releases the
// transport and is idempotent, and that a closed connector refuses to connect.
func TestNetworkConnector_CloseReleasesTransport(t *testing.T) {
	stub := &stubTransport{client: &fakeSQLClient{}}
	withStubTransport(t, SchemeREST, stub)

	cfg, _ := ParseDSN("rest://localhost:63001/mydb")
	connector := NewNetworkConnector(cfg)

	if _, err := connector.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := connector.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if stub.closeCalls != 1 {
		t.Errorf("transport close calls = %d, want 1", stub.closeCalls)
	}

	if err := connector.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if stub.closeCalls != 1 {
		t.Errorf("transport closed twice (%d calls)", stub.closeCalls)
	}

	_, err := connector.Connect(context.Background())
	var closedErr *ConnectorClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("Connect after Close error = %v, want *ConnectorClosedError", err)
	}
}

// TestNetworkConnector_TLSOptionsFromDSN verifies TLS material named in the
// DSN is parsed and carried through to the transport options.
func TestNetworkConnector_TLSOptionsFromDSN(t *testing.T) {
	cfg, err := ParseDSN("rest://localhost:63001/mydb?ca=/tmp/ca.pem&cert=/tmp/c.pem&key=/tmp/k.pem")
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	if cfg.CAFile != "/tmp/ca.pem" {
		t.Errorf("CAFile = %q, want /tmp/ca.pem", cfg.CAFile)
	}
	if cfg.CertFile != "/tmp/c.pem" {
		t.Errorf("CertFile = %q, want /tmp/c.pem", cfg.CertFile)
	}
	if cfg.KeyFile != "/tmp/k.pem" {
		t.Errorf("KeyFile = %q, want /tmp/k.pem", cfg.KeyFile)
	}
}

// TestNetworkConnector_UnknownScheme verifies an unroutable scheme is
// reported rather than silently defaulting to a transport.
func TestNetworkConnector_UnknownScheme(t *testing.T) {
	connector := NewNetworkConnector(&DSNConfig{Scheme: "carrierpigeon", Host: "h", Database: "d"})
	defer func() { _ = connector.Close() }()

	_, err := connector.Connect(context.Background())

	var unsupported *UnsupportedSchemeError
	if !errors.As(err, &unsupported) {
		t.Errorf("error = %v, want *UnsupportedSchemeError", err)
	}
}
