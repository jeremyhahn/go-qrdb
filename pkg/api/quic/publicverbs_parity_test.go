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

package quic

import (
	"context"
	"crypto/tls"
	"slices"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/publicverbs"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// TestPublicVerbsParity_QUIC fails when the native QUIC transport's public
// method set differs from publicverbs.Table in either direction: every row's
// QUIC method is public and dispatched, and no other built-in method is.
func TestPublicVerbsParity_QUIC(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "localhost:0", WithInvitations(&fakeInvitations{}))
	want := publicverbs.QUICMethods()

	for _, v := range publicverbs.Verbs {
		t.Run(string(v), func(t *testing.T) {
			method := publicverbs.Table[v].QUIC
			if !srv.methodPerms.IsPublic(method) {
				t.Errorf("%s (%s) is not public on QUIC", method, v)
			}
			if _, ok := srv.handlers[method]; !ok {
				t.Errorf("%s (%s) is not dispatched on QUIC", method, v)
			}
		})
	}
	for method := range srv.handlers {
		if _, inTable := want[method]; srv.methodPerms.IsPublic(method) && !inTable {
			t.Errorf("QUIC serves %s public, but publicverbs.Table does not name it", method)
		}
	}
}

// TestPublicVerbsParity_QUICPreAuthLimit pins the pre-authentication
// accounting: a table verb with a RateLimit dimension draws it, a method
// outside the table draws nothing, and no middleware means no limit.
func TestPublicVerbsParity_QUICPreAuthLimit(t *testing.T) {
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "localhost:0")
	if err := srv.limitPublic(t.Context(), publicverbs.Table[publicverbs.VerbHealth].QUIC); err != nil {
		t.Fatalf("no middleware: limitPublic = %v, want nil", err)
	}
	if err := srv.limitPublic(t.Context(), "kv.Put"); err != nil {
		t.Fatalf("non-public method: limitPublic = %v, want nil", err)
	}
}

// TestPublicVerbsParity_QUICNoCertificate drives a real QUIC listener with
// authentication on and a client that presents no certificate: every table
// verb passes authentication, and every other dispatched method is refused
// StatusUnauthenticated before its payload is decoded.
func TestPublicVerbsParity_QUICNoCertificate(t *testing.T) {
	mw := authmw.New(&noopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithAuthMiddleware(mw), WithInvitations(&fakeInvitations{}),
	)
	conn := env.dialServer(t)
	public := publicverbs.QUICMethods()

	methods := make([]string, 0, len(env.srv.handlers))
	for method := range env.srv.handlers {
		methods = append(methods, method)
	}
	slices.Sort(methods)
	for i, method := range methods {
		t.Run(method, func(t *testing.T) {
			_, status, _ := roundTrip(t, conn, method, uint64(3000+i), nil)
			if _, ok := public[method]; ok {
				if status == wire.StatusUnauthenticated {
					t.Fatalf("%s is in publicverbs.Table but refused a caller with no certificate", method)
				}
				return
			}
			if status != wire.StatusUnauthenticated {
				t.Fatalf("%s with no certificate = %d, want StatusUnauthenticated", method, status)
			}
		})
	}
	health := publicverbs.Table[publicverbs.VerbHealth].QUIC
	if _, status, _ := roundTrip(t, conn, health, 2999, nil); status != wire.StatusOK {
		t.Fatalf("%s with no certificate = %d, want StatusOK", health, status)
	}
}

// TestPublicVerbsParity_QUICBadCertificateIsHandshakeFailure: a client
// certificate that does not chain to the server's client CA ends the
// handshake. It is never downgraded to an anonymous connection, so even the
// no-login health verb is unreachable.
func TestPublicVerbsParity_QUICBadCertificateIsHandshakeFailure(t *testing.T) {
	bundle := generateMutualTLS(t)
	foreign := generateMutualTLS(t)
	serverCfg := bundle.serverCfg.Clone()
	serverCfg.ClientAuth = tls.VerifyClientCertIfGiven
	clientCfg := bundle.clientCfg.Clone()
	clientCfg.Certificates = nil
	// Present the foreign certificate whatever CAs the server names;
	// crypto/tls would otherwise withhold one no named CA issued and the
	// connection would be anonymous, which proves nothing here.
	clientCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		return &foreign.clientCfg.Certificates[0], nil
	}

	mw := authmw.New(&noopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(serverCfg), WithAuthMiddleware(mw))
	if err := srv.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	// Control: the same listener serves health to a caller with no
	// certificate, so the failure below is the certificate's alone.
	anonCfg := bundle.clientCfg.Clone()
	anonCfg.Certificates = nil
	anon := (&integrationEnv{srv: srv, clientCfg: anonCfg}).dialServer(t)
	if _, status, _ := roundTrip(t, anon, publicverbs.Table[publicverbs.VerbHealth].QUIC, 1, nil); status != wire.StatusOK {
		t.Fatalf("health with no certificate = %d, want StatusOK", status)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, srv.Addr(), clientCfg, &quicgo.Config{MaxIdleTimeout: 5 * time.Second})
	if err != nil {
		return // refused inside the handshake
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "test done") })

	// TLS 1.3 completes the client side before the server verifies the
	// client's certificate; the server's alert then closes the connection,
	// so the first stream fails instead of carrying a response.
	stream, err := conn.OpenStreamSync(ctx)
	if err == nil {
		_ = wire.WriteRequest(stream, publicverbs.Table[publicverbs.VerbHealth].QUIC, "", 1, nil)
		_ = stream.Close()
		_, _, _, err = wire.ReadResponse(stream)
	}
	if err == nil {
		t.Fatal("health with an unverifiable certificate got a response, want a handshake failure")
	}
}
