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

package tenant

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// signerSPKI stands in for the administrator's public key; bookDouble only
// carries it into the statement.
var signerSPKI = []byte("admin-spki")

func beginNone(t *testing.T, svc *TenantService, tenantID, keyRef string) *TenantBarrierInitBeginResult {
	t.Helper()
	begun, err := svc.TenantBarrierInitBegin(context.Background(), &TenantBarrierInitBeginRequest{
		TenantID: tenantID, Strategy: seal.StrategyNone, KeyRef: keyRef, SignerPublicKey: signerSPKI,
	})
	if err != nil {
		t.Fatalf("TenantBarrierInitBegin: %v", err)
	}
	return begun
}

func complete(svc *TenantService, tenantID string, begun *TenantBarrierInitBeginResult, signature []byte) (*TenantBarrierInitResult, error) {
	return svc.TenantBarrierInitComplete(context.Background(), &TenantBarrierInitCompleteRequest{
		TenantID: tenantID, Reference: begun.Reference, Signature: signature,
	})
}

// Begin, a signature made away from the node, complete: the barrier is
// initialized under the statement the node prepared.
func TestTenantBarrierInitSigned_BeginSignComplete(t *testing.T) {
	book := &bookDouble{}
	svc, hook := newClaimService(t, book, "acme")
	begun := beginNone(t, svc, "acme", "kms://owner-key")
	if begun.TenantID != "acme" || begun.Strategy != string(seal.StrategyNone) ||
		len(begun.Reference) != SignedInitReferenceSize || len(begun.Statement) == 0 {
		t.Fatalf("begin = %+v", begun)
	}
	if initialized(t, svc, "acme") || len(book.claimed()) != 0 {
		t.Fatal("begin claimed a statement or keyed the barrier")
	}
	if _, err := complete(svc, "acme", begun, signDouble(begun.Statement)); err != nil {
		t.Fatalf("TenantBarrierInitComplete: %v", err)
	}
	got := book.claimed()
	if len(got) != 1 || got[0].strategy != seal.StrategyNone || got[0].keyRef != "kms://owner-key" ||
		"msg:"+got[0].statement != string(begun.Statement) {
		t.Fatalf("claims = %+v, want the begun statement", got)
	}
	if hook.count() != 1 || !initialized(t, svc, "acme") {
		t.Fatal("the barrier was not initialized after complete")
	}
}

func TestTenantBarrierInitSigned_PassphraseIsTheDefaultStrategy(t *testing.T) {
	svc, _ := newClaimService(t, &bookDouble{}, "acme")
	begun, err := svc.TenantBarrierInitBegin(context.Background(), &TenantBarrierInitBeginRequest{TenantID: "acme", SignerPublicKey: signerSPKI})
	if err != nil {
		t.Fatalf("TenantBarrierInitBegin: %v", err)
	}
	if begun.Strategy != string(seal.StrategyPassphrase) {
		t.Fatalf("strategy = %q, want passphrase", begun.Strategy)
	}
	passphrase, err := NewSignedInitPassphrase([]byte("correct horse battery"))
	if err != nil {
		t.Fatalf("NewSignedInitPassphrase: %v", err)
	}
	defer passphrase.Destroy()
	if _, err := svc.TenantBarrierInitComplete(context.Background(), &TenantBarrierInitCompleteRequest{
		TenantID: "acme", Reference: begun.Reference, Signature: signDouble(begun.Statement), Passphrase: passphrase,
	}); err != nil {
		t.Fatalf("TenantBarrierInitComplete: %v", err)
	}
	if !initialized(t, svc, "acme") {
		t.Fatal("the barrier was not initialized")
	}
}

func TestNewSignedInitPassphrase_EmptyIsNil(t *testing.T) {
	for _, p := range [][]byte{nil, {}} {
		buf, err := NewSignedInitPassphrase(p)
		if err != nil || buf != nil {
			t.Fatalf("NewSignedInitPassphrase(%q) = %v, %v; want nil, nil", p, buf, err)
		}
	}
}

func TestNewSignedInitPassphrase_MovesIntoProtectedMemoryAndWipes(t *testing.T) {
	p := []byte("correct horse battery")
	buf, err := NewSignedInitPassphrase(p)
	if err != nil {
		t.Fatalf("NewSignedInitPassphrase: %v", err)
	}
	defer buf.Destroy()
	if !bytes.Equal(p, make([]byte, len(p))) {
		t.Fatalf("source %q not wiped", p)
	}
	if ok, err := buf.EqualBytes([]byte("correct horse battery")); err != nil || !ok {
		t.Fatalf("buffer holds the wrong value: equal=%v err=%v", ok, err)
	}
}

func TestTenantBarrierInitSigned_WrongSignature(t *testing.T) {
	book := &bookDouble{}
	svc, hook := newClaimService(t, book, "acme")
	begun := beginNone(t, svc, "acme", "")
	if _, err := complete(svc, "acme", begun, []byte("forged")); !errors.Is(err, errBadSignature) {
		t.Fatalf("complete = %v, want the book's signature refusal", err)
	}
	if hook.count() != 0 || initialized(t, svc, "acme") || len(book.claimed()) != 0 {
		t.Fatal("a wrong signature initialized the barrier")
	}
	// The reference was spent by the attempt.
	var se *SignedInitError
	if _, err := complete(svc, "acme", begun, signDouble(begun.Statement)); !errors.As(err, &se) || se.Reason != SignedInitUnknown {
		t.Fatalf("retry = %v, want SignedInitUnknown", err)
	}
}

func TestTenantBarrierInitSigned_ReplayedStatement(t *testing.T) {
	book := &bookDouble{}
	svc, _ := newClaimService(t, book, "acme")
	begun := beginNone(t, svc, "acme", "")
	sig := signDouble(begun.Statement)
	if _, err := complete(svc, "acme", begun, sig); err != nil {
		t.Fatalf("complete: %v", err)
	}
	var se *SignedInitError
	if _, err := complete(svc, "acme", begun, sig); !errors.As(err, &se) || se.Reason != SignedInitUnknown || se.TenantID != "acme" {
		t.Fatalf("replay = %v, want SignedInitUnknown", err)
	}
	if len(book.claimed()) != 1 {
		t.Fatal("the replay claimed a second statement")
	}
}

func TestTenantBarrierInitSigned_CompleteWithoutBegin(t *testing.T) {
	svc, _ := newClaimService(t, &bookDouble{}, "acme")
	var se *SignedInitError
	_, err := complete(svc, "acme", &TenantBarrierInitBeginResult{Reference: bytes.Repeat([]byte{7}, SignedInitReferenceSize)}, []byte("sig"))
	if !errors.As(err, &se) || se.Reason != SignedInitUnknown {
		t.Fatalf("complete = %v, want SignedInitUnknown", err)
	}
	if initialized(t, svc, "acme") {
		t.Fatal("a complete without a begin initialized the barrier")
	}
}

// A reference begun for one tenant does not complete another's init.
func TestTenantBarrierInitSigned_ReferenceIsBoundToItsTenant(t *testing.T) {
	svc, _ := newClaimService(t, &bookDouble{}, "acme")
	if _, err := svc.CreateTenant(context.Background(), "other", "other", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	begun := beginNone(t, svc, "acme", "")
	var se *SignedInitError
	if _, err := complete(svc, "other", begun, signDouble(begun.Statement)); !errors.As(err, &se) || se.Reason != SignedInitUnknown {
		t.Fatalf("complete = %v, want SignedInitUnknown", err)
	}
}

func TestTenantBarrierInitSigned_ExpiredReference(t *testing.T) {
	svc, _ := newClaimService(t, &bookDouble{}, "acme")
	now := time.Unix(1_800_000_000, 0)
	svc.signedInits.now = func() time.Time { return now }
	begun := beginNone(t, svc, "acme", "")
	if !begun.ExpiresAt.Equal(now.Add(SignedInitTTL)) {
		t.Fatalf("ExpiresAt = %v, want %v", begun.ExpiresAt, now.Add(SignedInitTTL))
	}
	now = now.Add(SignedInitTTL + time.Second)
	var se *SignedInitError
	if _, err := complete(svc, "acme", begun, signDouble(begun.Statement)); !errors.As(err, &se) || se.Reason != SignedInitExpired {
		t.Fatalf("complete = %v, want SignedInitExpired", err)
	}
	if initialized(t, svc, "acme") {
		t.Fatal("an expired reference initialized the barrier")
	}
}

func TestTenantBarrierInitSigned_RequiredFields(t *testing.T) {
	svc, _ := newClaimService(t, &bookDouble{}, "acme")
	ctx := context.Background()
	if _, err := svc.TenantBarrierInitBegin(ctx, nil); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("begin(nil) = %v", err)
	}
	if _, err := svc.TenantBarrierInitComplete(ctx, &TenantBarrierInitCompleteRequest{}); !errors.Is(err, ErrEmptyTenantID) {
		t.Fatalf("complete(no tenant) = %v", err)
	}
	for name, run := range map[string]func() error{
		"signer public key": func() error {
			_, err := svc.TenantBarrierInitBegin(ctx, &TenantBarrierInitBeginRequest{TenantID: "acme"})
			return err
		},
		"statement reference": func() error {
			_, err := svc.TenantBarrierInitComplete(ctx, &TenantBarrierInitCompleteRequest{TenantID: "acme", Signature: []byte("s")})
			return err
		},
		"signature": func() error {
			_, err := svc.TenantBarrierInitComplete(ctx, &TenantBarrierInitCompleteRequest{TenantID: "acme", Reference: []byte("r")})
			return err
		},
	} {
		var fe *SignedInitFieldRequiredError
		if err := run(); !errors.As(err, &fe) || fe.Field != name {
			t.Errorf("%s: err = %v, want SignedInitFieldRequiredError", name, err)
		}
	}
}

func TestTenantBarrierInitSigned_UnsupportedWithoutABook(t *testing.T) {
	svc, _, _ := newHookService(t)
	ctx := context.Background()
	var ue *SignedInitUnsupportedError
	if _, err := svc.TenantBarrierInitBegin(ctx, &TenantBarrierInitBeginRequest{TenantID: "acme", SignerPublicKey: signerSPKI}); !errors.As(err, &ue) {
		t.Fatalf("begin = %v, want SignedInitUnsupportedError", err)
	}
	if _, err := svc.TenantBarrierInitComplete(ctx, &TenantBarrierInitCompleteRequest{TenantID: "acme", Reference: []byte("r"), Signature: []byte("s")}); !errors.As(err, &ue) {
		t.Fatalf("complete = %v, want SignedInitUnsupportedError", err)
	}
}

func TestTenantBarrierInitSigned_BeginRefusals(t *testing.T) {
	svc, _ := newClaimService(t, &bookDouble{}, "acme")
	ctx := context.Background()
	if _, err := svc.TenantBarrierInitBegin(ctx, &TenantBarrierInitBeginRequest{TenantID: "ghost", Strategy: seal.StrategyNone, SignerPublicKey: signerSPKI}); err == nil {
		t.Fatal("begin for an unknown tenant succeeded")
	}
	if _, err := svc.TenantBarrierInitBegin(ctx, &TenantBarrierInitBeginRequest{TenantID: "acme", Strategy: "no-such-strategy", SignerPublicKey: signerSPKI}); err == nil {
		t.Fatal("begin for an unknown strategy succeeded")
	}
	if _, err := svc.TenantBarrierInitBegin(ctx, &TenantBarrierInitBeginRequest{TenantID: "acme", Strategy: seal.StrategyNone, SignerPublicKey: []byte{}}); err == nil {
		t.Fatal("begin without a key succeeded")
	}
}

func TestTenantBarrierInitSigned_Shamir(t *testing.T) {
	book := &bookDouble{}
	svc, ctx := newShamirClaimService(t, book, "acme")
	begun, err := svc.TenantBarrierInitBegin(ctx, &TenantBarrierInitBeginRequest{
		TenantID: "acme", Strategy: seal.StrategyShamir, Threshold: 2, Shares: 2, Roster: shamirClaimRoster(t), SignerPublicKey: signerSPKI,
	})
	if err != nil {
		t.Fatalf("TenantBarrierInitBegin: %v", err)
	}
	if begun.Threshold != 2 || len(begun.Holders) != 2 || initialized(t, svc, "acme") {
		t.Fatalf("begin = %+v, want the prepared split and no key", begun)
	}
	res, err := svc.TenantBarrierInitComplete(ctx, &TenantBarrierInitCompleteRequest{
		TenantID: "acme", Reference: begun.Reference, Signature: signDouble(begun.Statement),
	})
	if err != nil {
		t.Fatalf("TenantBarrierInitComplete: %v", err)
	}
	if got := book.claimed(); len(got) != 1 || got[0].strategy != seal.StrategyShamir {
		t.Fatalf("claims = %+v, want one Shamir claim", got)
	}
	if !initialized(t, svc, "acme") || len(res.UnsealKeyCheck) != seal.UnsealKeyCheckSize {
		t.Fatalf("complete = %+v, want an initialized barrier", res)
	}
}

func TestSignedInitBook_Limit(t *testing.T) {
	var b signedInitBook
	now := time.Unix(1_800_000_000, 0)
	b.now = func() time.Time { return now }
	for i := 0; i < MaxOutstandingSignedInits; i++ {
		if _, err := b.issue(&signedInit{tenantID: "acme", expiresAt: now.Add(SignedInitTTL)}); err != nil {
			t.Fatalf("issue %d: %v", i, err)
		}
	}
	var te *TooManySignedInitsError
	if _, err := b.issue(&signedInit{tenantID: "acme", expiresAt: now.Add(SignedInitTTL)}); !errors.As(err, &te) || te.Limit != MaxOutstandingSignedInits {
		t.Fatalf("issue past the limit = %v, want TooManySignedInitsError", err)
	}
	// Once the outstanding statements lapse, the sweep frees their slots.
	now = now.Add(SignedInitTTL + time.Second)
	if _, err := b.issue(&signedInit{tenantID: "acme", expiresAt: now.Add(SignedInitTTL)}); err != nil {
		t.Fatalf("issue after expiry: %v", err)
	}
	if b.count.Load() != 1 {
		t.Fatalf("count = %d, want 1", b.count.Load())
	}
}

func TestSignedInitBook_DefaultClockAndErrorText(t *testing.T) {
	var b signedInitBook
	if d := time.Since(b.clock()); d < 0 || d > time.Minute {
		t.Fatalf("clock = %v, want time.Now", b.clock())
	}
	for _, err := range []error{
		&SignedInitError{TenantID: "acme", Reason: SignedInitExpired},
		&SignedInitFieldRequiredError{Field: "signature"},
		&TooManySignedInitsError{Limit: 3},
		&SignedInitUnsupportedError{TenantID: "acme"},
	} {
		if err.Error() == "" {
			t.Errorf("%T has no text", err)
		}
	}
}
