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

package sdk_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof/initprooftest"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// initVerifierFixture is a facade's verifier on a fake clock, the caller's
// key, and the key source the facade holds the proof to.
type initVerifierFixture struct {
	v   *sdk.TenantBarrierInitVerifier
	clk *clocktest.Fake
	key *ecdsa.PrivateKey
	cs  tls.Certificate
}

func newInitVerifierFixture(t *testing.T) *initVerifierFixture {
	t.Helper()
	cert := initprooftest.Certificate(t, "alice")
	clk := clocktest.New()
	return &initVerifierFixture{
		v:   sdk.NewTenantBarrierInitVerifier(sdk.NewInitChallengeBook(clk, 0)),
		clk: clk,
		key: cert.PrivateKey.(*ecdsa.PrivateKey),
		cs:  cert,
	}
}

// expect holds the proof to the fixture's key.
func (f *initVerifierFixture) expect(t *testing.T) sdk.TenantBarrierInitKey {
	t.Helper()
	leaf, err := x509.ParseCertificate(f.cs.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	want := sdk.ComputeSPKIThumbprint(leaf)
	return func() (string, error) { return want, nil }
}

// begin runs init/begin for tenantID on conn's server end.
func (f *initVerifierFixture) begin(t *testing.T, conn *initprooftest.Conn, tenantID string) []byte {
	t.Helper()
	c, err := f.v.Begin(tenantID, &conn.Server)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if len(c.Nonce) == 0 {
		t.Fatal("Begin issued an empty nonce")
	}
	return c.Nonce
}

// prove is the client's half, transport.ProveTenantBarrierInit, on conn's
// client end, carried into the server's proof type.
func prove(t *testing.T, signer crypto.Signer, conn *initprooftest.Conn, tenantID string, nonce []byte) *sdk.TenantBarrierInitProof {
	t.Helper()
	p, err := transport.ProveTenantBarrierInit(signer, tenantID, nonce, &conn.Client)
	if err != nil {
		t.Fatalf("ProveTenantBarrierInit: %v", err)
	}
	return &sdk.TenantBarrierInitProof{Nonce: p.Nonce, PublicKey: p.PublicKey, Signature: p.Signature}
}

func requireInitProofReason(t *testing.T, err error, want sdk.InitProofReason) {
	t.Helper()
	var pe *sdk.FirstAdminProofError
	if !errors.As(err, &pe) || pe.Reason != want {
		t.Fatalf("err = %v; want *sdk.FirstAdminProofError %q", err, want)
	}
}

func TestTenantBarrierInitVerifier_RoundTrip(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn, "acme", f.begin(t, conn, "acme"))
	if err := f.v.Complete("acme", &conn.Server, proof, f.expect(t)); err != nil {
		t.Fatalf("a valid proof was refused: %v", err)
	}
	if n := f.v.Outstanding(); n != 0 {
		t.Fatalf("Outstanding after complete = %d, want 0", n)
	}
}

func TestTenantBarrierInitVerifier_NonceIsConsumedOnce(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn, "acme", f.begin(t, conn, "acme"))
	if err := f.v.Complete("acme", &conn.Server, proof, f.expect(t)); err != nil {
		t.Fatal(err)
	}
	err := f.v.Complete("acme", &conn.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonUnknownNonce)
}

// A proof signed over another nonce than the one it presents fails the
// signature; the presented nonce is still spent.
func TestTenantBarrierInitVerifier_AnotherNonceIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	issued := f.begin(t, conn, "acme")
	other := f.begin(t, conn, "acme")
	proof := prove(t, f.key, conn, "acme", other)
	proof.Nonce = issued
	err := f.v.Complete("acme", &conn.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonBadSignature)
	err = f.v.Complete("acme", &conn.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonUnknownNonce)
}

// A nonce issued on one connection and redeemed on another is refused.
func TestTenantBarrierInitVerifier_AnotherConnectionIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn1, conn2 := initprooftest.Dial(t, f.cs), initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn1, "acme", f.begin(t, conn1, "acme"))
	err := f.v.Complete("acme", &conn2.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonConnectionMismatch)
}

// A nonce redeemed on its own connection but signed over another
// connection's exporter fails the signature.
func TestTenantBarrierInitVerifier_AnotherExporterIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn1, conn2 := initprooftest.Dial(t, f.cs), initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn2, "acme", f.begin(t, conn1, "acme"))
	err := f.v.Complete("acme", &conn1.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonBadSignature)
}

func TestTenantBarrierInitVerifier_AnotherTenantIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn, "acme", f.begin(t, conn, "acme"))
	err := f.v.Complete("globex", &conn.Server, proof, f.expect(t))
	var pe *sdk.FirstAdminProofError
	if !errors.As(err, &pe) || pe.TenantID != "globex" {
		t.Fatalf("a proof for acme completed globex: %v", err)
	}
}

// A proof for another tenant, signed over a nonce really issued for the
// completing tenant, fails the signature.
func TestTenantBarrierInitVerifier_SignedForAnotherTenantIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn, "globex", f.begin(t, conn, "acme"))
	err := f.v.Complete("acme", &conn.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonBadSignature)
}

func TestTenantBarrierInitVerifier_ExpiredNonceIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn, "acme", f.begin(t, conn, "acme"))
	f.clk.Advance(sdk.InitChallengeTTL + time.Second)
	err := f.v.Complete("acme", &conn.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonExpired)
}

func TestTenantBarrierInitVerifier_UnknownNonceIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn, "acme", []byte("never issued by this verifier"))
	err := f.v.Complete("acme", &conn.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonUnknownNonce)
}

func TestTenantBarrierInitVerifier_WrongKeyIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	proof := prove(t, other, conn, "acme", f.begin(t, conn, "acme"))
	err = f.v.Complete("acme", &conn.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonWrongKey)
}

func TestTenantBarrierInitVerifier_AbsentProofAndKeyAreRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	requireInitProofReason(t, f.v.Complete("acme", &conn.Server, nil, f.expect(t)), sdk.InitProofReasonAbsent)

	proof := prove(t, f.key, conn, "acme", f.begin(t, conn, "acme"))
	err := f.v.Complete("acme", &conn.Server, proof, nil)
	requireInitProofReason(t, err, sdk.InitProofReasonNoPendingAdmin)

	proof = prove(t, f.key, conn, "acme", f.begin(t, conn, "acme"))
	err = f.v.Complete("acme", &conn.Server, proof, func() (string, error) { return "", nil })
	requireInitProofReason(t, err, sdk.InitProofReasonNoPendingAdmin)
}

// The key source's failure is returned as it is, and the nonce is spent.
func TestTenantBarrierInitVerifier_KeySourceErrorFailsClosed(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	want := errors.New("pending admin store unreachable")
	proof := prove(t, f.key, conn, "acme", f.begin(t, conn, "acme"))
	if err := f.v.Complete("acme", &conn.Server, proof, func() (string, error) { return "", want }); !errors.Is(err, want) {
		t.Fatalf("err = %v; want the key source's error", err)
	}
	requireInitProofReason(t, f.v.Complete("acme", &conn.Server, proof, f.expect(t)), sdk.InitProofReasonUnknownNonce)
}

func TestTenantBarrierInitVerifier_MalformedKeyIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	conn := initprooftest.Dial(t, f.cs)
	proof := prove(t, f.key, conn, "acme", f.begin(t, conn, "acme"))
	proof.PublicKey = []byte("not a SubjectPublicKeyInfo")
	err := f.v.Complete("acme", &conn.Server, proof, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonMalformedKey)
}

// A connection with no TLS 1.3 exporter can neither begin nor complete.
func TestTenantBarrierInitVerifier_NoExporterIsRefused(t *testing.T) {
	f := newInitVerifierFixture(t)
	_, err := f.v.Begin("acme", nil)
	requireInitProofReason(t, err, sdk.InitProofReasonNoExporter)
	err = f.v.Complete("acme", &tls.ConnectionState{}, &sdk.TenantBarrierInitProof{
		Nonce: []byte("n"), PublicKey: []byte("k"), Signature: []byte("s"),
	}, f.expect(t))
	requireInitProofReason(t, err, sdk.InitProofReasonNoExporter)
}

func TestTenantBarrierInitVerifier_BeginOverTheLimitIsRefused(t *testing.T) {
	v := sdk.NewTenantBarrierInitVerifier(sdk.NewInitChallengeBook(clocktest.New(), 1))
	conn := initprooftest.Dial(t, initprooftest.Certificate(t, "alice"))
	if _, err := v.Begin("acme", &conn.Server); err != nil {
		t.Fatal(err)
	}
	_, err := v.Begin("acme", &conn.Server)
	var tm *sdk.InitTooManyChallengesError
	if !errors.As(err, &tm) {
		t.Fatalf("second begin over the limit: %T %v", err, err)
	}
}

// A nil book is a fresh one with the default limit.
func TestNewTenantBarrierInitVerifier_NilBook(t *testing.T) {
	v := sdk.NewTenantBarrierInitVerifier(nil)
	conn := initprooftest.Dial(t, initprooftest.Certificate(t, "alice"))
	for range 2 {
		if _, err := v.Begin("acme", &conn.Server); err != nil {
			t.Fatal(err)
		}
	}
	if n := v.Outstanding(); n != 2 {
		t.Fatalf("Outstanding = %d, want 2", n)
	}
	if sdk.InitChallengeMaxOutstanding < 2 {
		t.Fatalf("InitChallengeMaxOutstanding = %d", sdk.InitChallengeMaxOutstanding)
	}
}
