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

package barrierinit

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// signDetached is what a signer outside the node does with the message begin
// returned: ECDSA over its SHA-256 digest in ASN.1 form.
func signDetached(t *testing.T, key *ecdsa.PrivateKey, message []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(message)
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("SignASN1: %v", err)
	}
	return sig
}

func spki(t *testing.T, pub any) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	return der
}

func TestNewTenantInit_DefaultsToPassphraseAtEpochOne(t *testing.T) {
	s := NewTenantInit("acme", "", "kms://key", testNow)
	if s.Barrier != clusterseal.TenantBarrier("acme") || s.Epoch != 1 || s.StrategyID != seal.StrategyPassphrase ||
		s.KeyRef != "kms://key" || s.IssuedAt != testNow.Unix() {
		t.Fatalf("statement = %+v", s)
	}
	if got := NewTenantInit("acme", string(seal.StrategyNone), "", testNow); got.StrategyID != seal.StrategyNone {
		t.Fatalf("strategy = %q, want none", got.StrategyID)
	}
}

func TestNewTenantShamirInit_HashesTheRoster(t *testing.T) {
	s := NewTenantShamirInit("acme", 2, []byte("roster"), []byte("check"), testNow)
	sum := sha256.Sum256([]byte("roster"))
	if s.StrategyID != seal.StrategyShamir || s.Threshold != 2 || !bytes.Equal(s.RosterHash, sum[:]) ||
		!bytes.Equal(s.UnsealKeyCheck, []byte("check")) || s.Epoch != 1 {
		t.Fatalf("statement = %+v", s)
	}
	if _, err := ForSigner(s, newKey(t).Public()); err != nil {
		t.Fatalf("ForSigner: %v", err)
	}
}

func TestForSigner_NamesTheKeyAndClearsTheSignature(t *testing.T) {
	key := newKey(t)
	s := NewTenantInit("acme", "", "", testNow)
	s.Signature = []byte("stale")
	got, err := ForSigner(s, key.Public())
	if err != nil {
		t.Fatalf("ForSigner: %v", err)
	}
	if !bytes.Equal(got.SignerSPKI, spki(t, key.Public())) || got.Signature != nil {
		t.Fatalf("statement = %+v, want the key's SPKI and no signature", got)
	}
}

func TestForSigner_Refusals(t *testing.T) {
	_, err := ForSigner(NewTenantInit("acme", "", "", testNow), struct{}{})
	wantReason(t, err, ReasonMalformedKey)
	_, err = ForSigner(InitStatement{}, newKey(t).Public())
	wantReason(t, err, ReasonMalformed)
}

func TestAttach_AdmitsADetachedSignature(t *testing.T) {
	key := newKey(t)
	s, err := ForSigner(NewTenantInit("acme", "", "kms://key", testNow), key.Public())
	if err != nil {
		t.Fatalf("ForSigner: %v", err)
	}
	sig := signDetached(t, key, s.Message())
	got, err := Attach(s, sig)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if !bytes.Equal(got.Signature, sig) {
		t.Fatal("the signature was not attached")
	}
	if err := Verify(got, Anchors{FirstAdmin: thumbprint(t, key.Public())}); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestAttach_AdmitsEd25519(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	s, err := ForSigner(NewTenantInit("acme", "", "", testNow), pub)
	if err != nil {
		t.Fatalf("ForSigner: %v", err)
	}
	if _, err := Attach(s, ed25519.Sign(priv, s.Message())); err != nil {
		t.Fatalf("Attach: %v", err)
	}
}

func TestAttach_Refusals(t *testing.T) {
	key := newKey(t)
	s, err := ForSigner(NewTenantInit("acme", "", "", testNow), key.Public())
	if err != nil {
		t.Fatalf("ForSigner: %v", err)
	}
	other := signDetached(t, newKey(t), s.Message())
	_, err = Attach(s, other)
	wantReason(t, err, ReasonBadSignature)

	_, err = Attach(s, nil)
	wantReason(t, err, ReasonBadSignature)

	bad := s
	bad.SignerSPKI = []byte("not a key")
	_, err = Attach(bad, signDetached(t, key, s.Message()))
	wantReason(t, err, ReasonMalformedKey)
}

// Prepare, sign away from the node, attach, claim: the statement a remote
// administrator signs is admitted exactly as one SignTenantInit made.
func TestClaimer_PrepareAttachClaim(t *testing.T) {
	ctx, f := context.Background(), newClaimerFixture(t)
	statement, message, err := f.claimer.PrepareTenantInit("acme", seal.StrategyPassphrase, "kms://key", nil, spki(t, f.admin.Public()), testNow)
	if err != nil {
		t.Fatalf("PrepareTenantInit: %v", err)
	}
	raw, err := f.claimer.AttachTenantInit(statement, signDetached(t, f.admin, message))
	if err != nil {
		t.Fatalf("AttachTenantInit: %v", err)
	}
	if err := f.claimer.ClaimTenantInit(ctx, "acme", seal.StrategyPassphrase, "kms://key", raw); err != nil {
		t.Fatalf("ClaimTenantInit: %v", err)
	}
	if _, found, _ := f.kv.Get(ctx, Prefix+string(clusterseal.TenantBarrier("acme"))); !found {
		t.Fatal("no init record was written")
	}
}

func TestClaimer_PrepareAttachClaimShamir(t *testing.T) {
	ctx, f := context.Background(), newClaimerFixture(t)
	prepared := &seal.ShamirInitParams{Threshold: 2, Roster: []byte("roster"), UnsealKeyCheck: []byte("check")}
	statement, message, err := f.claimer.PrepareTenantInit("acme", seal.StrategyShamir, "", prepared, spki(t, f.admin.Public()), testNow)
	if err != nil {
		t.Fatalf("PrepareTenantInit: %v", err)
	}
	raw, err := f.claimer.AttachTenantInit(statement, signDetached(t, f.admin, message))
	if err != nil {
		t.Fatalf("AttachTenantInit: %v", err)
	}
	if err := f.claimer.ClaimTenantShamirInit(ctx, "acme", prepared, raw); err != nil {
		t.Fatalf("ClaimTenantShamirInit: %v", err)
	}
}

// A statement signed by a key other than the pending first admin's attaches
// -- the signature is its own key's -- and is refused when claimed.
func TestClaimer_PreparedForAnotherSignerIsRefusedAtClaim(t *testing.T) {
	f := newClaimerFixture(t)
	stranger := newKey(t)
	statement, message, err := f.claimer.PrepareTenantInit("acme", seal.StrategyNone, "", nil, spki(t, stranger.Public()), testNow)
	if err != nil {
		t.Fatalf("PrepareTenantInit: %v", err)
	}
	raw, err := f.claimer.AttachTenantInit(statement, signDetached(t, stranger, message))
	if err != nil {
		t.Fatalf("AttachTenantInit: %v", err)
	}
	wantReason(t, f.claimer.ClaimTenantInit(context.Background(), "acme", seal.StrategyNone, "", raw), ReasonWrongSigner)
}

func TestClaimer_PrepareAndAttachRefusals(t *testing.T) {
	f := newClaimerFixture(t)
	_, _, err := f.claimer.PrepareTenantInit("acme", seal.StrategyNone, "", nil, []byte("not a key"), testNow)
	wantReason(t, err, ReasonMalformedKey)
	_, _, err = f.claimer.PrepareTenantInit("", seal.StrategyNone, "", nil, spki(t, f.admin.Public()), testNow)
	wantReason(t, err, ReasonMalformed)

	_, err = f.claimer.AttachTenantInit([]byte("{"), []byte("sig"))
	wantReason(t, err, ReasonMalformed)
	statement, _, err := f.claimer.PrepareTenantInit("acme", seal.StrategyNone, "", nil, spki(t, f.admin.Public()), testNow)
	if err != nil {
		t.Fatalf("PrepareTenantInit: %v", err)
	}
	_, err = f.claimer.AttachTenantInit(statement, []byte("forged"))
	wantReason(t, err, ReasonBadSignature)
}
