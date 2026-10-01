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

// Package sotest is the SDK's SO dual-factor conformance suite: an SO rekeys
// the barrier, deletes its recovery keys and seals it through an SDK
// transport against the real server for that transport with the SO gate on,
// presenting the PIN with transport.WithSOPIN. Each network transport's test
// starts its server from the pieces a Fixture holds and hands the suite a
// connected client.
//
// Everything past authentication is real: the RBAC authorizer over the
// built-in roles, the role store the gate resolves the SO role in, the
// SOAuthenticator that verifies the PIN, and the software PIN backend that
// verifies the barrier SO PIN BarrierSeal carries in its so_pin field. The
// authenticator is the one double, standing in for the mTLS handshake.
//
// It is imported by tests only; a production import of it is a defect.
package sotest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Tenant and PIN the suite runs under.
const (
	Tenant = tenant.SystemTenantID
	PIN    = "correct horse battery staple"
)

// Fixture holds the server-side pieces a transport's start function wires.
type Fixture struct {
	// Roles is the real role store, holding Tenant's built-in roles.
	Roles role.RoleServicer
	// Verifier is the real SOAuthenticator with PIN committed for Tenant.
	Verifier *auth.SOAuthenticator
	// MW is the auth middleware: the real RBAC authorizer behind a fixed
	// authenticator presenting the officer.
	MW *authmw.AuthMiddleware
	// Gate is the SO gate for the transports that take one (WithSOGate).
	// REST builds the same gate from Roles and Verifier.
	Gate *authmw.SOGate
	// Barrier is the barrier service; it counts what reached it.
	Barrier *authtest.Barrier
	// PINs is the barrier PIN backend for the server's WithPINBackend, or
	// nil for none. The seal subtests set it to a real software backend
	// holding PIN, so a seal is admitted only when its so_pin is PIN; the
	// other subtests leave it nil, because the request-field PIN it checks
	// on rekey is not the one WithSOPIN binds on every transport.
	PINs pin.PINBackend
	// ServerTLS and ClientTLS are a matching self-signed pair for localhost.
	// The client presents a certificate, as transports that require one
	// before authentication expect.
	ServerTLS, ClientTLS *tls.Config
}

// Officer returns the officer's identity: the SO role by its persisted
// identifier, plus the health permission a transport's Connect checks with.
func Officer() *auth.Identity {
	id := authtest.SOIdentity(Tenant, "officer")
	id.ScopePermissions = []string{string(permission.AdminHealth), string(permission.AdminStatus)}
	return id
}

// NewFixture builds the pieces.
func NewFixture(t *testing.T) *Fixture {
	t.Helper()
	roles := authtest.BuiltInRoles(t, Tenant)
	verifier := authtest.SOVerifier(t, PIN, Tenant)
	caller := authtest.NewFixedAuthenticator(Officer())
	serverTLS, clientTLS := tlsPair(t)
	return &Fixture{
		Roles:     roles,
		Verifier:  verifier,
		MW:        authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller)),
		Gate:      authmw.NewSOGate(roles, verifier, audit.NewRingBuffer(64), audit.FailClosed, nil),
		Barrier:   &authtest.Barrier{},
		ServerTLS: serverTLS,
		ClientTLS: clientTLS,
	}
}

// Run is the conformance suite. start starts the transport's server from f
// and returns a client connected to it.
func Run(t *testing.T, start func(t *testing.T, f *Fixture) transport.BarrierClient) {
	t.Run("SO rekeys with the PIN", func(t *testing.T) {
		f := NewFixture(t)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		defer release()
		if _, err := c.BarrierRekey(ctx, &transport.BarrierRekeyRequest{Threshold: 2, Total: 3}); err != nil {
			t.Fatalf("BarrierRekey with the SO PIN: %v", err)
		}
		if got := f.Barrier.Rekeys(); got != 1 {
			t.Fatalf("rekeys = %d, want 1", got)
		}
	})
	t.Run("SO deletes recovery keys with the PIN", func(t *testing.T) {
		f := NewFixture(t)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		defer release()
		if err := c.BarrierDeleteRecoveryKeys(ctx); err != nil {
			t.Fatalf("BarrierDeleteRecoveryKeys with the SO PIN: %v", err)
		}
		if got := f.Barrier.RecoveryDeletes(); got != 1 {
			t.Fatalf("deletes = %d, want 1", got)
		}
	})
	t.Run("SO without the PIN is refused", func(t *testing.T) {
		f := NewFixture(t)
		c := start(t, f)
		if _, err := c.BarrierRekey(context.Background(), &transport.BarrierRekeyRequest{Threshold: 2, Total: 3}); err == nil {
			t.Fatal("BarrierRekey without the SO PIN succeeded")
		}
		if f.Barrier.Rekeys() != 0 {
			t.Fatal("a refused rekey reached the service")
		}
	})
	t.Run("BarrierSeal carries the bound PIN in so_pin", func(t *testing.T) {
		f := NewFixture(t)
		f.PINs = authtest.BarrierPINBackend(t, PIN)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		defer release()
		if err := c.BarrierSeal(ctx); err != nil {
			t.Fatalf("BarrierSeal with the SO PIN bound: %v", err)
		}
		if got := f.Barrier.Seals(); got != 1 {
			t.Fatalf("seals = %d, want 1", got)
		}
	})
	t.Run("BarrierSeal without the bound PIN is refused", func(t *testing.T) {
		f := NewFixture(t)
		f.PINs = authtest.BarrierPINBackend(t, PIN)
		c := start(t, f)
		if err := c.BarrierSeal(context.Background()); err == nil {
			t.Fatal("BarrierSeal without the SO PIN succeeded")
		}
		wrong, release := BindPIN(t, "not the PIN")
		defer release()
		if err := c.BarrierSeal(wrong); err == nil {
			t.Fatal("BarrierSeal with the wrong SO PIN bound succeeded")
		}
		if f.Barrier.Seals() != 0 {
			t.Fatal("a refused seal reached the service")
		}
	})
	t.Run("a released PIN is no longer sent", func(t *testing.T) {
		f := NewFixture(t)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		release()
		if _, err := c.BarrierRekey(ctx, &transport.BarrierRekeyRequest{Threshold: 2, Total: 3}); err == nil {
			t.Fatal("BarrierRekey after release succeeded")
		}
		if f.Barrier.Rekeys() != 0 {
			t.Fatal("a refused rekey reached the service")
		}
	})
}

// BindPIN binds pin with transport.WithSOPIN, failing the test if it cannot
// be sealed.
func BindPIN(t *testing.T, pin string) (context.Context, func()) {
	t.Helper()
	ctx, release, err := transport.WithSOPIN(context.Background(), []byte(pin))
	if err != nil {
		t.Fatalf("WithSOPIN: %v", err)
	}
	return ctx, release
}

// tlsPair returns a self-signed TLS 1.3 server config for localhost that
// requires a client certificate, and a client config that trusts it and
// presents the same certificate.
func tlsPair(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("sotest: key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("sotest: certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("sotest: parse: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	server := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	client := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS13,
	}
	return server, client
}
