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

package unix

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestBarrierSecret_RequiresTLS: a Unix socket is plaintext unless TLS is
// configured, so a barrier secret is never sent over one without TLS, is sent
// over one with TLS, and a request without a secret is still sent over a
// plaintext socket -- the SO PIN's rule on this transport.
func TestBarrierSecret_RequiresTLS(t *testing.T) {
	serverTLS, clientTLS := sotest.TLSPair(t)
	sotest.RunBarrierSecretTLS(t, "unix", secretTarget(t, nil, nil), secretTarget(t, serverTLS, clientTLS))
}

// TestSealStoreSecret_RequiresTLS proves Put and Get of a seal-store secret
// refuse a plaintext socket before any wire call, and reach the server over
// TLS: a Unix socket has no exemption.
func TestSealStoreSecret_RequiresTLS(t *testing.T) {
	ctx := context.Background()
	plain := secretTarget(t, nil, nil)
	ep := sealStoreEndpoints{t: plain.Client.(*Transport)}
	var tlsErr *transport.BarrierSecretRequiresTLSError
	if err := ep.PutSealStoreSecret(ctx, "alpha", []byte("alpha-secret")); !errors.As(err, &tlsErr) {
		t.Fatalf("Put over plaintext: err = %v, want *transport.BarrierSecretRequiresTLSError", err)
	}
	if _, err := ep.GetSealStoreSecret(ctx, "alpha"); !errors.As(err, &tlsErr) {
		t.Fatalf("Get over plaintext: err = %v, want *transport.BarrierSecretRequiresTLSError", err)
	}
	if n := plain.Reached(); n != 0 {
		t.Fatalf("plaintext seal-store secret calls reached the server %d times, want 0", n)
	}

	serverTLS, clientTLS := sotest.TLSPair(t)
	secure := secretTarget(t, serverTLS, clientTLS)
	sep := sealStoreEndpoints{t: secure.Client.(*Transport)}
	if err := sep.PutSealStoreSecret(ctx, "alpha", []byte("alpha-secret")); errors.As(err, &tlsErr) {
		t.Fatalf("Put over TLS refused: %v", err)
	}
	if n := secure.Reached(); n == 0 {
		t.Fatal("Put over TLS never reached the server")
	}
}

// secretTarget serves a SecretProbe on a Unix socket, over TLS when serverTLS
// is set, and connects a client to it.
func secretTarget(t *testing.T, serverTLS, clientTLS *tls.Config) sotest.SecretTarget {
	t.Helper()
	socketPath := tempSocketPath(t)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	probe := &sotest.SecretProbe{}
	probe.Serve(t, lis, serverTLS)
	opts := []transport.Option{transport.WithAddress(socketPath), transport.WithTimeout(sotest.RequestTimeout)}
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
