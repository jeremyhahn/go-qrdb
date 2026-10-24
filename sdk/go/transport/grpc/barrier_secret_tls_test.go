// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package grpc

import (
	"context"
	"crypto/tls"
	"net"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestBarrierSecret_RequiresTLS: a barrier secret is never sent over a
// plaintext gRPC connection, is sent over TLS, and a request without one is
// still sent over plaintext.
func TestBarrierSecret_RequiresTLS(t *testing.T) {
	serverTLS, clientTLS := sotest.TLSPair(t)
	sotest.RunBarrierSecretTLS(t, "grpc", secretTarget(t, nil, nil), secretTarget(t, serverTLS, clientTLS))
}

// secretTarget serves a SecretProbe on loopback, over TLS when serverTLS is
// set, and connects a client to it.
func secretTarget(t *testing.T, serverTLS, clientTLS *tls.Config) sotest.SecretTarget {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	probe := &sotest.SecretProbe{}
	probe.Serve(t, lis, serverTLS)
	opts := []transport.Option{transport.WithAddress(lis.Addr().String()), transport.WithTimeout(sotest.RequestTimeout)}
	if clientTLS != nil {
		opts = append(opts, transport.WithTLSConfig(clientTLS))
	}
	tr, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return sotest.SecretTarget{Client: tr, Reached: probe.Reached}
}

// testTLS is one TLS pair for a test server and its client. A test whose call
// carries a barrier secret needs it: the transport sends one only over TLS. A
// nil *testTLS is plaintext.
type testTLS struct {
	server googlegrpc.ServerOption
	dial   googlegrpc.DialOption
}

func newTestTLS(t *testing.T) *testTLS {
	t.Helper()
	serverTLS, clientTLS := sotest.TLSPair(t)
	return newTestTLSFrom(serverTLS, clientTLS)
}

// newAnonymousTestTLS is newTestTLS with no client certificate, for a test
// that asserts what the server does with a caller who presents none.
func newAnonymousTestTLS(t *testing.T) *testTLS {
	t.Helper()
	serverTLS, clientTLS := sotest.TLSPair(t)
	serverTLS.ClientAuth = tls.NoClientCert
	clientTLS.Certificates = nil
	return newTestTLSFrom(serverTLS, clientTLS)
}

func newTestTLSFrom(serverTLS, clientTLS *tls.Config) *testTLS {
	return &testTLS{
		server: googlegrpc.Creds(credentials.NewTLS(serverTLS)),
		dial:   googlegrpc.WithTransportCredentials(credentials.NewTLS(clientTLS)),
	}
}

func (tt *testTLS) serverOptions() []googlegrpc.ServerOption {
	if tt == nil {
		return nil
	}
	return []googlegrpc.ServerOption{tt.server}
}

func (tt *testTLS) dialOption() googlegrpc.DialOption {
	if tt == nil {
		return googlegrpc.WithTransportCredentials(insecure.NewCredentials())
	}
	return tt.dial
}

// config returns the transport configuration for addr, TLS-enabled when tt is.
func (tt *testTLS) config(addr string) *transport.Config {
	return &transport.Config{Address: addr, TLSEnabled: tt != nil}
}
