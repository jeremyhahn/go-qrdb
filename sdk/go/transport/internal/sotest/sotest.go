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
// the barrier, generates and deletes its recovery keys, initializes it with
// Shamir, seals it and runs the ceremony through an SDK transport against the
// real server for that transport with the SO gate on, presenting the PIN with
// transport.WithSOPIN only -- never by setting a request's SOPin by hand. Each network transport's test
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
	"errors"
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
	// nil for none. The request-field subtests set it to a real software
	// backend, so an operation is admitted only when its own so_pin field
	// carries PIN; the gate-only subtests leave it nil.
	PINs pin.PINBackend
	// Ceremony is the ceremony service; it records the SO PIN each request
	// handed it. CA is the ceremony CA the server needs configured.
	Ceremony *authtest.Ceremony
	CA       *authtest.CeremonyCA
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
		Ceremony:  &authtest.Ceremony{},
		CA:        &authtest.CeremonyCA{},
		ServerTLS: serverTLS,
		ClientTLS: clientTLS,
	}
}

// Client is what the suite drives: the barrier and ceremony surfaces of one
// SDK transport.
type Client interface {
	transport.BarrierClient
	transport.CeremonyClient
}

// Run is the conformance suite. start starts the transport's server from f
// and returns a client connected to it.
func Run(t *testing.T, start func(t *testing.T, f *Fixture) Client) {
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
	t.Run("BarrierRekey fills so_pin from the bound PIN", func(t *testing.T) {
		f := NewFixture(t)
		f.PINs = authtest.BarrierPINBackend(t, PIN)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		defer release()
		if _, err := c.BarrierRekey(ctx, &transport.BarrierRekeyRequest{Threshold: 2, Total: 3}); err != nil {
			t.Fatalf("BarrierRekey with the SO PIN bound: %v", err)
		}
		if got := f.Barrier.Rekeys(); got != 1 {
			t.Fatalf("rekeys = %d, want 1", got)
		}
	})
	t.Run("BarrierGenerateRecoveryKeys fills so_pin from the bound PIN", func(t *testing.T) {
		f := NewFixture(t)
		f.PINs = authtest.BarrierPINBackend(t, PIN)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		defer release()
		if _, err := c.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, Total: 3}); err != nil {
			t.Fatalf("BarrierGenerateRecoveryKeys with the SO PIN bound: %v", err)
		}
		if got := f.Barrier.RecoveryGenerations(); got != 1 {
			t.Fatalf("generations = %d, want 1", got)
		}
	})
	t.Run("a conflicting explicit SOPin is refused before sending", func(t *testing.T) {
		f := NewFixture(t)
		f.PINs = authtest.BarrierPINBackend(t, PIN)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		defer release()
		calls := map[string]func() error{
			"BarrierRekey": func() error {
				_, err := c.BarrierRekey(ctx, &transport.BarrierRekeyRequest{Threshold: 2, Total: 3, SOPin: "other"})
				return err
			},
			"BarrierGenerateRecoveryKeys": func() error {
				_, err := c.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, Total: 3, SOPin: "other"})
				return err
			},
			"BarrierInitializeShamir": func() error {
				_, err := c.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{Secret: []byte("secret"), Threshold: 2, TotalShares: 3, SOPin: "other"})
				return err
			},
			"CeremonyInitialize": func() error {
				_, err := c.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{SOPin: "other"})
				return err
			},
			"CeremonySignCSR": func() error {
				_, err := c.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{Username: "officer", SOPin: "other", CSRPEM: "csr", Role: "officer"})
				return err
			},
		}
		for method, call := range calls {
			var conflict *transport.SOPINConflictError
			if err := call(); !errors.As(err, &conflict) || conflict.Method != method {
				t.Fatalf("%s: err = %v, want *transport.SOPINConflictError", method, err)
			}
		}
		if f.Barrier.Rekeys()+f.Barrier.RecoveryGenerations()+f.Barrier.ShamirInits() != 0 || len(f.Ceremony.SOPINs()) != 0 {
			t.Fatal("a conflicting request reached the service")
		}
	})
	t.Run("BarrierInitializeShamir fills so_pin from the bound PIN", func(t *testing.T) {
		f := NewFixture(t)
		// Initialization SETS the barrier SO PIN from the request's so_pin.
		f.PINs = authtest.EmptyBarrierPINBackend(t)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		defer release()
		if _, err := c.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{Secret: []byte("secret"), Threshold: 2, TotalShares: 3}); err != nil {
			t.Fatalf("BarrierInitializeShamir with the SO PIN bound: %v", err)
		}
		if got := f.Barrier.ShamirInits(); got != 1 {
			t.Fatalf("shamir inits = %d, want 1", got)
		}
		if err := f.PINs.VerifySOPIN(PIN); err != nil {
			t.Fatalf("the barrier SO PIN is not the bound PIN: %v", err)
		}
	})
	t.Run("the ceremony fills so_pin from the bound PIN", func(t *testing.T) {
		f := NewFixture(t)
		c := start(t, f)
		ctx, release := BindPIN(t, PIN)
		defer release()
		if _, err := c.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{}); err != nil {
			t.Fatalf("CeremonyInitialize with the SO PIN bound: %v", err)
		}
		if _, err := c.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{Username: "officer", CSRPEM: "csr", Role: "officer"}); err != nil {
			t.Fatalf("CeremonySignCSR with the SO PIN bound: %v", err)
		}
		if got := f.Ceremony.SOPINs(); len(got) != 2 || got[0] != PIN || got[1] != PIN {
			t.Fatalf("the ceremony saw %d SO PINs, not the bound PIN twice", len(got))
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
