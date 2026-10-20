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
	"context"
	"crypto/ecdsa"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// claimerFixture is a Claimer over a real Book and memKV, anchored to admin.
type claimerFixture struct {
	kv      *memKV
	admin   *ecdsa.PrivateKey
	claimer *Claimer
}

func newClaimerFixture(t *testing.T) *claimerFixture {
	t.Helper()
	f := &claimerFixture{kv: newMemKV(), admin: newKey(t)}
	anchor := thumbprint(t, f.admin.Public())
	f.claimer = NewClaimer(NewBook(f.kv, nil), func(context.Context, clusterseal.Barrier) (Anchors, error) {
		return Anchors{FirstAdmin: anchor}, nil
	})
	return f
}

func (f *claimerFixture) encoded(t *testing.T, s InitStatement) []byte {
	t.Helper()
	raw, err := signed(t, f.admin, s).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return raw
}

func TestClaimer_ClaimsTheStatementTheRequestNames(t *testing.T) {
	ctx, f := context.Background(), newClaimerFixture(t)
	raw := f.encoded(t, unsigned(1, "kms://key"))
	if err := f.claimer.ClaimTenantInit(ctx, "acme", seal.StrategyPassphrase, "kms://key", raw); err != nil {
		t.Fatalf("ClaimTenantInit: %v", err)
	}
	// The owner's retry on this or another node claims the same record.
	if err := f.claimer.ClaimTenantInit(ctx, "acme", seal.StrategyPassphrase, "kms://key", raw); err != nil {
		t.Fatalf("ClaimTenantInit retry: %v", err)
	}
	if _, found, _ := f.kv.Get(ctx, Prefix+string(clusterseal.TenantBarrier("acme"))); !found {
		t.Fatal("no init record was written")
	}
}

func TestClaimer_RefusesAStatementForAnotherInit(t *testing.T) {
	f := newClaimerFixture(t)
	raw := f.encoded(t, unsigned(1, "kms://key"))
	for name, call := range map[string]func() error{
		"other tenant": func() error {
			return f.claimer.ClaimTenantInit(context.Background(), "globex", seal.StrategyPassphrase, "kms://key", raw)
		},
		"other strategy": func() error {
			return f.claimer.ClaimTenantInit(context.Background(), "acme", seal.StrategyNone, "kms://key", raw)
		},
		"other key": func() error {
			return f.claimer.ClaimTenantInit(context.Background(), "acme", seal.StrategyPassphrase, "kms://other", raw)
		},
	} {
		t.Run(name, func(t *testing.T) {
			wantReason(t, call(), ReasonMismatch)
		})
	}
	if got, _ := f.kv.Scan(context.Background(), Prefix); len(got) != 0 {
		t.Fatalf("a mismatched statement was recorded: %v", got)
	}
}

// A Shamir statement is claimed against the split this node prepared: the
// threshold, the roster and the unseal key's check value must all be the
// ones the owner signed.
func TestClaimer_ClaimsTheShamirStatementForThePreparedSplit(t *testing.T) {
	ctx, f := context.Background(), newClaimerFixture(t)
	prepared := &seal.ShamirInitParams{Threshold: 2, Roster: []byte("roster"), UnsealKeyCheck: []byte("check")}
	raw, err := SignTenantShamirInit(f.admin, "acme", prepared.Threshold, prepared.Roster, prepared.UnsealKeyCheck, testNow)
	if err != nil {
		t.Fatalf("SignTenantShamirInit: %v", err)
	}
	if err := f.claimer.ClaimTenantShamirInit(ctx, "acme", prepared, raw); err != nil {
		t.Fatalf("ClaimTenantShamirInit: %v", err)
	}
	if _, found, _ := f.kv.Get(ctx, Prefix+string(clusterseal.TenantBarrier("acme"))); !found {
		t.Fatal("no init record was written")
	}
}

func TestClaimer_RefusesAShamirStatementForAnotherSplit(t *testing.T) {
	f := newClaimerFixture(t)
	raw, err := SignTenantShamirInit(f.admin, "acme", 2, []byte("roster"), []byte("check"), testNow)
	if err != nil {
		t.Fatalf("SignTenantShamirInit: %v", err)
	}
	for name, prepared := range map[string]*seal.ShamirInitParams{
		"nothing prepared": nil,
		"other threshold":  {Threshold: 3, Roster: []byte("roster"), UnsealKeyCheck: []byte("check")},
		"other roster":     {Threshold: 2, Roster: []byte("other"), UnsealKeyCheck: []byte("check")},
		"other key":        {Threshold: 2, Roster: []byte("roster"), UnsealKeyCheck: []byte("other")},
	} {
		t.Run(name, func(t *testing.T) {
			wantReason(t, f.claimer.ClaimTenantShamirInit(context.Background(), "acme", prepared, raw), ReasonMismatch)
		})
	}
	if got, _ := f.kv.Scan(context.Background(), Prefix); len(got) != 0 {
		t.Fatalf("a mismatched statement was recorded: %v", got)
	}
}

func TestClaimer_RefusesMalformedAndForeignStatements(t *testing.T) {
	ctx, f := context.Background(), newClaimerFixture(t)
	wantReason(t, f.claimer.ClaimTenantInit(ctx, "acme", seal.StrategyPassphrase, "", []byte("{")), ReasonMalformed)

	forged, err := signed(t, newKey(t), unsigned(1, "")).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	wantReason(t, f.claimer.ClaimTenantInit(ctx, "acme", seal.StrategyPassphrase, "", forged), ReasonWrongSigner)
}

func TestClaimer_AnAnchorFailureIsAStoreError(t *testing.T) {
	f := newClaimerFixture(t)
	unavailable := errors.New("tenant CA unavailable")
	c := NewClaimer(NewBook(f.kv, nil), func(context.Context, clusterseal.Barrier) (Anchors, error) {
		return Anchors{}, unavailable
	})
	err := c.ClaimTenantInit(context.Background(), "acme", seal.StrategyPassphrase, "", f.encoded(t, unsigned(1, "")))
	var se *StoreError
	if !errors.As(err, &se) || !errors.Is(err, unavailable) {
		t.Fatalf("ClaimTenantInit = %v, want a *StoreError wrapping the anchor failure", err)
	}
}

func TestClaimer_TenantInitReturnsTheVerifiedStatement(t *testing.T) {
	ctx, f := context.Background(), newClaimerFixture(t)
	if err := f.claimer.ClaimTenantInit(ctx, "acme", seal.StrategyPassphrase, "kms://key", f.encoded(t, unsigned(1, "kms://key"))); err != nil {
		t.Fatalf("ClaimTenantInit: %v", err)
	}
	got, err := f.claimer.TenantInit(ctx, "acme")
	if err != nil {
		t.Fatalf("TenantInit: %v", err)
	}
	if got.Barrier != clusterseal.TenantBarrier("acme") || got.Epoch != 1 ||
		got.StrategyID != seal.StrategyPassphrase || got.KeyRef != "kms://key" {
		t.Fatalf("TenantInit = %+v, want acme's epoch-1 passphrase statement for kms://key", got)
	}
}

func TestClaimer_TenantInitReturnsTheLatestEpoch(t *testing.T) {
	ctx, kv, admin, ca := context.Background(), newMemKV(), newKey(t), newCA(t)
	book := NewBook(kv, nil)
	first := Anchors{FirstAdmin: thumbprint(t, admin.Public())}
	if err := book.Claim(ctx, signed(t, admin, unsigned(1, "kms://acme/one")), first); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	two := unsigned(2, "kms://acme/two")
	two.SignerCertificate = ca.issue(t, admin.Public())
	later := Anchors{CA: ca.pool, At: testNow}
	if err := book.Advance(ctx, signed(t, admin, two), later); err != nil {
		t.Fatalf("Advance(2): %v", err)
	}
	c := NewClaimer(book, func(context.Context, clusterseal.Barrier) (Anchors, error) {
		return Anchors{FirstAdmin: first.FirstAdmin, CA: later.CA, At: later.At}, nil
	})
	got, err := c.TenantInit(ctx, "acme")
	if err != nil {
		t.Fatalf("TenantInit: %v", err)
	}
	if got.Epoch != 2 || got.KeyRef != "kms://acme/two" {
		t.Fatalf("TenantInit = epoch %d keyRef %q, want epoch 2 kms://acme/two", got.Epoch, got.KeyRef)
	}
}

func TestClaimer_TenantInitRefusesABarrierWithNoStatement(t *testing.T) {
	_, err := newClaimerFixture(t).claimer.TenantInit(context.Background(), "acme")
	var none *NoInitStatementError
	if !errors.As(err, &none) || none.Barrier != clusterseal.TenantBarrier("acme") {
		t.Fatalf("TenantInit = %v, want *NoInitStatementError for acme", err)
	}
	if none.Error() == "" {
		t.Fatal("empty error message")
	}
}

func TestClaimer_TenantInitRefusesAStatementThatNoLongerVerifies(t *testing.T) {
	ctx, f := context.Background(), newClaimerFixture(t)
	if err := f.claimer.ClaimTenantInit(ctx, "acme", seal.StrategyPassphrase, "", f.encoded(t, unsigned(1, ""))); err != nil {
		t.Fatalf("ClaimTenantInit: %v", err)
	}
	// The record is read through anchors naming another first admin.
	other := thumbprint(t, newKey(t).Public())
	c := NewClaimer(NewBook(f.kv, nil), func(context.Context, clusterseal.Barrier) (Anchors, error) {
		return Anchors{FirstAdmin: other}, nil
	})
	_, err := c.TenantInit(ctx, "acme")
	wantReason(t, err, ReasonWrongSigner)
}

func TestClaimer_TenantInitStoreAndAnchorFailures(t *testing.T) {
	ctx, f := context.Background(), newClaimerFixture(t)
	if err := f.claimer.ClaimTenantInit(ctx, "acme", seal.StrategyPassphrase, "", f.encoded(t, unsigned(1, ""))); err != nil {
		t.Fatalf("ClaimTenantInit: %v", err)
	}
	unavailable := errors.New("tenant CA unavailable")
	c := NewClaimer(NewBook(f.kv, nil), func(context.Context, clusterseal.Barrier) (Anchors, error) {
		return Anchors{}, unavailable
	})
	var se *StoreError
	if _, err := c.TenantInit(ctx, "acme"); !errors.As(err, &se) || !errors.Is(err, unavailable) {
		t.Fatalf("TenantInit = %v, want a *StoreError wrapping the anchor failure", err)
	}

	f.kv.fail(errors.New("unavailable"))
	if _, err := f.claimer.TenantInit(ctx, "acme"); !errors.As(err, &se) {
		t.Fatalf("TenantInit on a failing store = %v, want *StoreError", err)
	}
}
