// Copyright (c) 2026 Jeremy Hahn
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

package middleware

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof/initprooftest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Tenant barrier init/begin and init/complete -- the proof of possession of
// the first admin's key bound to the TLS exporter. Every connection here is a
// REAL TLS 1.3 handshake over an in-memory pipe, so the exporter both ends
// derive is crypto/tls's own, not a value a double chose.
// ---------------------------------------------------------------------------

// liveConn is one completed mTLS connection: the client end's state, from
// which the caller derives the exporter it signs, and the server end's, which
// the transport places in the request context.
type liveConn struct {
	client tls.ConnectionState
	server tls.ConnectionState
}

// dialLive completes a TLS 1.3 handshake in which the client presents ck.
func dialLive(t *testing.T, ck certKey) liveConn {
	t.Helper()
	conn := initprooftest.Dial(t, tls.Certificate{Certificate: [][]byte{ck.cert.Raw}, PrivateKey: ck.key})
	return liveConn{client: conn.Client, server: conn.Server}
}

// ctx is the request context a transport builds for a call on this
// connection by caller.
func (c liveConn) ctx(caller *auth.Identity) context.Context {
	server := c.server
	return auth.WithIdentity(auth.WithTLSState(context.Background(), &server), caller)
}

// prove signs the init proof for tenantID and nonce on this connection with
// signer, presenting pub as the signing key.
func (c liveConn) prove(t *testing.T, signer crypto.Signer, pub crypto.PublicKey, tenantID string, nonce []byte) *TenantBarrierInitProof {
	t.Helper()
	client := c.client
	exporter, err := initproof.Exporter(&client)
	if err != nil {
		t.Fatalf("client exporter: %v", err)
	}
	sig, err := initproof.Sign(signer, tenantID, nonce, exporter)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return &TenantBarrierInitProof{Nonce: nonce, PublicKey: spki, Signature: sig}
}

// initFixture is a self-managed tenant "acme" onboarded with first admin
// alice: no role can grant tenant_barrier:init yet, its barrier has never
// been initialized, and the pending record holds alice's SPKI.
type initFixture struct {
	m      *AuthMiddleware
	clk    *clocktest.Fake
	alice  certKey
	status *fakeBarrierStatusReader
	caller *auth.Identity
}

func newInitFixture(t *testing.T, authz *mockAuthorizer) *initFixture {
	t.Helper()
	alice := mintCert(t, "alice", "acme", nil, "")
	store := newPendingStore(t)
	if err := store.Create(context.Background(), "acme", &tenantadmin.Pending{
		Username:   "alice",
		Thumbprint: user.ComputeSPKIThumbprint(alice.cert),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	clk := clocktest.New()
	return &initFixture{
		m: New(authz, WithPendingTenantAdmins(store), WithMTLSBindingPolicy(auth.MTLSBindingRequired),
			WithInitChallengeBook(initproof.NewBook(clk, 0))),
		clk:    clk,
		alice:  alice,
		status: &fakeBarrierStatusReader{},
		caller: &auth.Identity{Subject: "alice", TenantID: "acme"},
	}
}

func denyAll() *mockAuthorizer {
	return &mockAuthorizer{err: errors.New("no role can satisfy tenant_barrier:init yet")}
}

// begin runs init/begin on conn as the fixture's caller.
func (f *initFixture) begin(t *testing.T, conn liveConn) []byte {
	t.Helper()
	c, err := f.m.BeginTenantBarrierInit(conn.ctx(f.caller), "acme", f.status)
	if err != nil {
		t.Fatalf("BeginTenantBarrierInit: %v", err)
	}
	return c.Nonce
}

func requireProofReason(t *testing.T, err error, want initproof.Reason) {
	t.Helper()
	var pe *tenant.FirstAdminProofError
	if !errors.As(err, &pe) || pe.Reason != want {
		t.Fatalf("err = %v; want *tenant.FirstAdminProofError %q", err, want)
	}
	if ClassifyAuthError(err) != AuthFailureUnauthenticated {
		t.Fatalf("%v does not classify as unauthenticated", err)
	}
}

func TestTenantBarrierInitProof_FirstAdminKeyOnTheSameConnection(t *testing.T) {
	f := newInitFixture(t, denyAll())
	conn := dialLive(t, f.alice)
	nonce := f.begin(t, conn)
	proof := conn.prove(t, f.alice.key, &f.alice.key.PublicKey, "acme", nonce)
	if err := f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, proof); err != nil {
		t.Fatalf("the first admin's proof was refused: %v", err)
	}
}

func TestTenantBarrierInitProof_ReplayIsRefused(t *testing.T) {
	f := newInitFixture(t, denyAll())
	conn := dialLive(t, f.alice)
	proof := conn.prove(t, f.alice.key, &f.alice.key.PublicKey, "acme", f.begin(t, conn))
	if err := f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, proof); err != nil {
		t.Fatal(err)
	}
	err := f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, proof)
	requireProofReason(t, err, initproof.ReasonUnknownNonce)
}

// A proof lifted off the connection it was made for and replayed on another
// -- even one authenticated by the same certificate -- fails.
func TestTenantBarrierInitProof_ReplayOnAnotherConnectionIsRefused(t *testing.T) {
	f := newInitFixture(t, denyAll())
	conn1, conn2 := dialLive(t, f.alice), dialLive(t, f.alice)
	proof := conn1.prove(t, f.alice.key, &f.alice.key.PublicKey, "acme", f.begin(t, conn1))
	err := f.m.AuthorizeTenantBarrierInitProof(conn2.ctx(f.caller), "acme", f.status, proof)
	requireProofReason(t, err, initproof.ReasonConnectionMismatch)
}

// A nonce issued on the completing connection but signed over another
// connection's exporter fails the signature, not the nonce.
func TestTenantBarrierInitProof_ExporterMismatchIsRefused(t *testing.T) {
	f := newInitFixture(t, denyAll())
	conn1, conn2 := dialLive(t, f.alice), dialLive(t, f.alice)
	nonce := f.begin(t, conn1)
	proof := conn2.prove(t, f.alice.key, &f.alice.key.PublicKey, "acme", nonce)
	err := f.m.AuthorizeTenantBarrierInitProof(conn1.ctx(f.caller), "acme", f.status, proof)
	requireProofReason(t, err, initproof.ReasonBadSignature)
}

func TestTenantBarrierInitProof_ExpiredChallengeIsRefused(t *testing.T) {
	f := newInitFixture(t, denyAll())
	conn := dialLive(t, f.alice)
	proof := conn.prove(t, f.alice.key, &f.alice.key.PublicKey, "acme", f.begin(t, conn))
	f.clk.Advance(initproof.ChallengeTTL + time.Second)
	err := f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, proof)
	requireProofReason(t, err, initproof.ReasonExpired)
}

func TestTenantBarrierInitProof_WrongKeyIsRefused(t *testing.T) {
	f := newInitFixture(t, denyAll())
	conn := dialLive(t, f.alice)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof := conn.prove(t, other, &other.PublicKey, "acme", f.begin(t, conn))
	err = f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, proof)
	requireProofReason(t, err, initproof.ReasonWrongKey)
}

// Naming the first admin's key while signing with another fails the
// signature.
func TestTenantBarrierInitProof_SignatureByAnotherKeyIsRefused(t *testing.T) {
	f := newInitFixture(t, denyAll())
	conn := dialLive(t, f.alice)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof := conn.prove(t, other, &f.alice.key.PublicKey, "acme", f.begin(t, conn))
	err = f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, proof)
	requireProofReason(t, err, initproof.ReasonBadSignature)
}

// An RBAC grant admits the caller to the gate, never past the proof.
func TestTenantBarrierInitProof_RBACGrantDoesNotSubstitute(t *testing.T) {
	f := newInitFixture(t, &mockAuthorizer{})
	conn := dialLive(t, f.alice)
	nonce := f.begin(t, conn)
	err := f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, &TenantBarrierInitProof{Nonce: nonce})
	requireProofReason(t, err, initproof.ReasonAbsent)
	err = f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, nil)
	requireProofReason(t, err, initproof.ReasonAbsent)
}

func TestTenantBarrierInitProof_MalformedKeyIsRefused(t *testing.T) {
	f := newInitFixture(t, denyAll())
	conn := dialLive(t, f.alice)
	proof := conn.prove(t, f.alice.key, &f.alice.key.PublicKey, "acme", f.begin(t, conn))
	proof.PublicKey = []byte("not a SubjectPublicKeyInfo")
	err := f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "acme", f.status, proof)
	requireProofReason(t, err, initproof.ReasonMalformedKey)
}

// A connection without TLS 1.3 keying material -- an assembled state, as the
// embedded transport's forwarded caller carries -- cannot begin or complete.
func TestTenantBarrierInitProof_NoExporterIsRefused(t *testing.T) {
	f := newInitFixture(t, denyAll())
	ctx := auth.WithIdentity(mtlsContext(f.alice), f.caller)
	_, err := f.m.BeginTenantBarrierInit(ctx, "acme", f.status)
	requireProofReason(t, err, initproof.ReasonNoExporter)
	err = f.m.AuthorizeTenantBarrierInitProof(ctx, "acme", f.status,
		&TenantBarrierInitProof{Nonce: []byte("n"), PublicKey: []byte("k"), Signature: []byte("s")})
	requireProofReason(t, err, initproof.ReasonNoExporter)
}

// begin admits exactly whom init admits: another certificate of the tenant
// gets no nonce.
func TestBeginTenantBarrierInit_RefusesWhomInitRefuses(t *testing.T) {
	f := newInitFixture(t, denyAll())
	mallory := mintCert(t, "alice", "acme", nil, "")
	conn := dialLive(t, mallory)
	_, err := f.m.BeginTenantBarrierInit(conn.ctx(f.caller), "acme", f.status)
	var mismatch *FirstInitCertificateMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected *FirstInitCertificateMismatchError, got %T: %v", err, err)
	}
	if n := f.m.initChallenges.Outstanding(); n != 0 {
		t.Fatalf("a refused begin issued %d challenges", n)
	}
}

func TestBeginTenantBarrierInit_LimitIsRateLimited(t *testing.T) {
	f := newInitFixture(t, denyAll())
	f.m.initChallenges = initproof.NewBook(f.clk, 1)
	conn := dialLive(t, f.alice)
	f.begin(t, conn)
	_, err := f.m.BeginTenantBarrierInit(conn.ctx(f.caller), "acme", f.status)
	var tm *initproof.TooManyChallengesError
	if !errors.As(err, &tm) || ClassifyAuthError(err) != AuthFailureRateLimited {
		t.Fatalf("second begin over the limit: %T %v", err, err)
	}
}

// With no pending admin -- the admin was bound at create, under the
// "enrolled" posture -- the proof is held to the certificate on the
// connection.
func TestTenantBarrierInitProof_NoPendingAdminBindsToTheConnectionCertificate(t *testing.T) {
	bob := mintCert(t, "bob", "acme", nil, "")
	m := New(&mockAuthorizer{}, WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	caller := &auth.Identity{Subject: "bob", TenantID: "acme"}
	status := &fakeBarrierStatusReader{initialized: true}
	conn := dialLive(t, bob)

	c, err := m.BeginTenantBarrierInit(conn.ctx(caller), "acme", status)
	if err != nil {
		t.Fatal(err)
	}
	proof := conn.prove(t, bob.key, &bob.key.PublicKey, "acme", c.Nonce)
	if err := m.AuthorizeTenantBarrierInitProof(conn.ctx(caller), "acme", status, proof); err != nil {
		t.Fatalf("the connection certificate's key was refused: %v", err)
	}

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c, err = m.BeginTenantBarrierInit(conn.ctx(caller), "acme", status)
	if err != nil {
		t.Fatal(err)
	}
	proof = conn.prove(t, other, &other.PublicKey, "acme", c.Nonce)
	err = m.AuthorizeTenantBarrierInitProof(conn.ctx(caller), "acme", status, proof)
	requireProofReason(t, err, initproof.ReasonWrongKey)
}

// No pending admin and no certificate on the connection: there is no key the
// proof could be by.
func TestTenantBarrierInitProof_NoKeyToProveIsRefused(t *testing.T) {
	m := New(&mockAuthorizer{})
	conn := dialLive(t, mintCert(t, "bob", "acme", nil, ""))
	server := conn.server
	server.PeerCertificates, server.VerifiedChains = nil, nil
	ctx := auth.WithIdentity(auth.WithTLSState(context.Background(), &server), &auth.Identity{Subject: "bob", TenantID: "acme"})
	c, err := m.BeginTenantBarrierInit(ctx, "acme", &fakeBarrierStatusReader{})
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof := conn.prove(t, key, &key.PublicKey, "acme", c.Nonce)
	err = m.AuthorizeTenantBarrierInitProof(ctx, "acme", &fakeBarrierStatusReader{}, proof)
	requireProofReason(t, err, initproof.ReasonNoPendingAdmin)
}

// A pending-admin store that cannot answer refuses the proof, fail-closed. The
// RBAC grant takes the caller past the window so the proof check is what
// reads the store.
func TestTenantBarrierInitProof_StoreErrorFailsClosed(t *testing.T) {
	want := errors.New("replicated store unreachable")
	alice := mintCert(t, "alice", "acme", nil, "")
	m := New(&mockAuthorizer{}, WithPendingTenantAdmins(&erroringPendingStore{err: want}))
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	conn := dialLive(t, alice)
	c, err := m.BeginTenantBarrierInit(conn.ctx(caller), "acme", &fakeBarrierStatusReader{})
	if err != nil {
		t.Fatal(err)
	}
	proof := conn.prove(t, alice.key, &alice.key.PublicKey, "acme", c.Nonce)
	err = m.AuthorizeTenantBarrierInitProof(conn.ctx(caller), "acme", &fakeBarrierStatusReader{}, proof)
	var su *StoreUnavailableError
	if !errors.As(err, &su) || !errors.Is(err, want) {
		t.Fatalf("expected *StoreUnavailableError wrapping the store failure, got %T: %v", err, err)
	}
}

// A proof made for one tenant does not complete another's init.
func TestTenantBarrierInitProof_OtherTenantIsRefused(t *testing.T) {
	f := newInitFixture(t, &mockAuthorizer{})
	conn := dialLive(t, f.alice)
	proof := conn.prove(t, f.alice.key, &f.alice.key.PublicKey, "acme", f.begin(t, conn))
	err := f.m.AuthorizeTenantBarrierInitProof(conn.ctx(f.caller), "globex", f.status, proof)
	requireProofReason(t, err, initproof.ReasonUnknownNonce)
}
