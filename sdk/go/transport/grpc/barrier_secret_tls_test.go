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
	"errors"
	"net"
	"sync/atomic"
	"testing"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
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

// TestBarrierSecret_HTTPAddressIsRefused: an address configured with an
// explicit http:// scheme refuses every secret-carrying call in
// sotest.SecretCalls, whatever the TLS config says (#1639), and the server
// never sees the call.
func TestBarrierSecret_HTTPAddressIsRefused(t *testing.T) {
	serverTLS, clientTLS := sotest.TLSPair(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	probe := &sotest.SecretProbe{}
	probe.Serve(t, lis, serverTLS)
	tr, err := New(
		transport.WithAddress("http://"+lis.Addr().String()),
		transport.WithTimeout(sotest.RequestTimeout),
		transport.WithTLSConfig(clientTLS),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	sotest.RunHTTPAddressRefused(t, "grpc", sotest.SecretTarget{Client: tr, Reached: probe.Reached})
}

// TestSOPIN_BoundPINRefusedOverHTTPAddress: a PIN bound on the context by
// transport.WithSOPIN, on a unary and on a streaming RPC, is refused with
// *transport.SOPINRequiresTLSError when the address names http:// explicitly,
// even though the client is configured for TLS and the server serves TLS
// (#1639). The request fields carry no secret, so only the client
// interceptors stand between the PIN and the wire, and the server never sees
// the PIN.
func TestSOPIN_BoundPINRefusedOverHTTPAddress(t *testing.T) {
	serverTLS, clientTLS := sotest.TLSPair(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var sawPIN atomic.Int64
	record := func(ctx context.Context) {
		if md, ok := metadata.FromIncomingContext(ctx); ok && len(md.Get(transport.SOPINMetadataKey)) > 0 {
			sawPIN.Add(1)
		}
	}
	srv := googlegrpc.NewServer(
		googlegrpc.Creds(credentials.NewTLS(serverTLS)),
		googlegrpc.ChainUnaryInterceptor(func(ctx context.Context, req any, _ *googlegrpc.UnaryServerInfo, handler googlegrpc.UnaryHandler) (any, error) {
			record(ctx)
			return handler(ctx, req)
		}),
		googlegrpc.ChainStreamInterceptor(func(srv any, ss googlegrpc.ServerStream, _ *googlegrpc.StreamServerInfo, handler googlegrpc.StreamHandler) error {
			record(ss.Context())
			return handler(srv, ss)
		}),
	)
	pb.RegisterQRDBServiceServer(srv, &sotest.Probe{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	tr, err := New(
		transport.WithAddress("http://"+lis.Addr().String()),
		transport.WithTimeout(sotest.RequestTimeout),
		transport.WithTLSConfig(clientTLS),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	calls := map[string]func(ctx context.Context) error{
		"unary/BarrierRekeyStart": func(ctx context.Context) error {
			_, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2})
			return err
		},
		"stream/Watch": func(ctx context.Context) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			_, err := tr.Watch(ctx, "k", transport.WatchOptions{})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			ctx, release := sotest.BindPIN(t, sotest.PIN)
			defer release()
			err := call(ctx)
			var tlsErr *transport.SOPINRequiresTLSError
			if !errors.As(err, &tlsErr) {
				t.Fatalf("err = %v, want *transport.SOPINRequiresTLSError", err)
			}
			if n := sawPIN.Load(); n != 0 {
				t.Fatalf("the SO PIN reached the server %d times over an http:// address", n)
			}
		})
	}
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
