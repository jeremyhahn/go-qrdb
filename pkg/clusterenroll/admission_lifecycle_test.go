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

// Admission lifecycle: renewing a LAPSED admission (the issued leaf's own
// NotAfter has passed) under the ordinary join rules, and permanently
// RETIRING an identity (RetireJoinIdentity) so it can never renew, lapsed
// or not.
package clusterenroll

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// admitAt writes an ADMITTED record for identity directly into store,
// bypassing the reserve/promote dance -- these tests only care about the
// record's SHAPE (NotAfter, serial), not how it got there.
func admitAt(t *testing.T, store kvstore.AtomicKVStore, identity string, notAfter time.Time, serial string) {
	t.Helper()
	if err := store.Put(context.Background(), joinIdentityPrefix+identity, encodeAdmitted(notAfter, serial)); err != nil {
		t.Fatalf("Put (admitted): %v", err)
	}
}

// ---------------------------------------------------------------------------
// classifyIdentityValue / parseAdmitted
// ---------------------------------------------------------------------------

func TestClassifyIdentityValue_LapsedAndActiveBoundary(t *testing.T) {
	future := encodeAdmitted(time.Now().Add(time.Hour), "1")
	past := encodeAdmitted(time.Now().Add(-time.Hour), "1")
	legacy := []byte(time.Now().UTC().Format(time.RFC3339))
	retired := encodeRetired("removed from the cluster")

	tests := []struct {
		name  string
		value []byte
		want  identityState
	}{
		{"nil is absent", nil, identityAbsent},
		{"future NotAfter is active", future, identityActive},
		{"past NotAfter is lapsed", past, identityLapsed},
		{"legacy bare timestamp never lapses", legacy, identityActive},
		{"retired blocks regardless of lapse", retired, identityRetired},
		{"unrecognized value fails closed to active", []byte("garbage"), identityActive},
		{"live pending reservation", pendingValue(time.Now().Add(time.Minute)), identityPendingLive},
		{"expired pending reservation is reclaimable (absent)", pendingValue(time.Now().Add(-time.Minute)), identityAbsent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyIdentityValue(tt.value); got != tt.want {
				t.Errorf("classifyIdentityValue(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestParseAdmitted_RoundTripsNotAfterAndSerial(t *testing.T) {
	notAfter := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	value := encodeAdmitted(notAfter, "123456789")
	rec, ok := parseAdmitted(value)
	if !ok {
		t.Fatalf("parseAdmitted(%q) ok = false, want true", value)
	}
	if !rec.NotAfter.Equal(notAfter) {
		t.Errorf("NotAfter = %v, want %v", rec.NotAfter, notAfter)
	}
	if rec.Serial != "123456789" {
		t.Errorf("Serial = %q, want %q", rec.Serial, "123456789")
	}
}

func TestParseAdmitted_LegacyBareTimestampHasZeroNotAfter(t *testing.T) {
	admittedAt := time.Now().Truncate(time.Second)
	rec, ok := parseAdmitted([]byte(admittedAt.UTC().Format(time.RFC3339)))
	if !ok {
		t.Fatal("parseAdmitted did not recognize a legacy bare RFC3339 timestamp")
	}
	if !rec.NotAfter.IsZero() {
		t.Errorf("legacy record NotAfter = %v, want zero (never lapses)", rec.NotAfter)
	}
	if !rec.AdmittedAt.Equal(admittedAt.UTC()) {
		t.Errorf("legacy record AdmittedAt = %v, want %v", rec.AdmittedAt, admittedAt.UTC())
	}
}

func TestParseAdmitted_PendingAndRetiredAreNotAdmittedShaped(t *testing.T) {
	if _, ok := parseAdmitted(pendingValue(time.Now().Add(time.Minute))); ok {
		t.Error("parseAdmitted treated a PENDING record as admitted-shaped")
	}
	if _, ok := parseAdmitted(encodeRetired("")); ok {
		t.Error("parseAdmitted treated a RETIRED record as admitted-shaped")
	}
}

// ---------------------------------------------------------------------------
// Renewing a lapsed admission: MintJoinToken / ConsumeJoinToken
// ---------------------------------------------------------------------------

func TestMintJoinToken_RenewsALapsedAdmission(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identity := "spiffe://test.cluster/node/node-1"
	admitAt(t, store, identity, time.Now().Add(-time.Hour), "1")

	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken for a lapsed admission = %v, want success (renewal under the ordinary rules)", err)
	}
	if token == "" {
		t.Fatal("MintJoinToken for a lapsed admission returned an empty token")
	}
}

func TestMintJoinToken_RefusedByAnActiveAdmission(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identity := "spiffe://test.cluster/node/node-1"
	admitAt(t, store, identity, time.Now().Add(time.Hour), "1")

	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var existing *ExistingMemberError
	if !errors.As(err, &existing) {
		t.Fatalf("MintJoinToken for an active admission = %v, want *ExistingMemberError", err)
	}
}

func TestConsumeJoinToken_RenewsALapsedAdmission(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identity := "spiffe://test.cluster/node/node-1"
	admitAt(t, store, identity, time.Now().Add(-time.Hour), "1")

	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	boundNodeURI, err := ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	if err != nil {
		t.Fatalf("ConsumeJoinToken for a lapsed admission = %v, want success", err)
	}
	if boundNodeURI != identity {
		t.Errorf("ConsumeJoinToken boundNodeURI = %q, want %q", boundNodeURI, identity)
	}
}

func TestConsumeJoinToken_RefusedByAnActiveAdmission(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identity := "spiffe://test.cluster/node/node-1"

	// Mint BEFORE admitting, exactly like
	// TestConsumeJoinToken_AlreadyAdmittedIdentityRefused: MintJoinToken
	// itself refuses once an identity is active, so the only way a valid
	// token can exist for one is for it to have been minted first.
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	admitAt(t, store, identity, time.Now().Add(time.Hour), "1")

	if _, err := ConsumeJoinToken(context.Background(), store, "test.cluster", token); err == nil {
		t.Fatal("ConsumeJoinToken against an active admission succeeded, want refusal")
	}
}

// ---------------------------------------------------------------------------
// Renewing a lapsed admission: ReserveJoinIdentity
// ---------------------------------------------------------------------------

func TestReserveJoinIdentity_ReclaimsALapsedAdmission(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	admitAt(t, store, "node-1", time.Now().Add(-time.Hour), "1")

	reserved, pending, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}
	if !reserved {
		t.Fatal("ReserveJoinIdentity did not reclaim a lapsed admission")
	}
	got, err := store.Get(context.Background(), joinIdentityPrefix+"node-1")
	if err != nil || string(got) != string(pending) {
		t.Fatalf("stored value after reclaim = %q, want the fresh pending value %q (err=%v)", got, pending, err)
	}
}

func TestReserveJoinIdentity_DoesNotReclaimAnActiveAdmission(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	admitAt(t, store, "node-1", time.Now().Add(time.Hour), "1")

	reserved, _, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}
	if reserved {
		t.Fatal("ReserveJoinIdentity reclaimed an ACTIVE admission")
	}
}

func TestReserveJoinIdentity_DoesNotReclaimARetiredIdentity(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	if err := RetireJoinIdentity(context.Background(), store, "test.cluster", "node-1", ""); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}

	reserved, _, err := ReserveJoinIdentity(context.Background(), store, "spiffe://test.cluster/node/node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}
	if reserved {
		t.Fatal("ReserveJoinIdentity reclaimed a RETIRED identity")
	}
}

// ---------------------------------------------------------------------------
// PromoteJoinIdentityAdmitted / MarkJoinIdentityAdmitted record NotAfter
// and serial, which is what makes renewal detectable later.
// ---------------------------------------------------------------------------

func TestPromoteJoinIdentityAdmitted_RecordsNotAfterAndSerial(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, pending, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}
	notAfter := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	if err := PromoteJoinIdentityAdmitted(context.Background(), store, "node-1", pending, notAfter, "42"); err != nil {
		t.Fatalf("PromoteJoinIdentityAdmitted: %v", err)
	}

	got, err := store.Get(context.Background(), joinIdentityPrefix+"node-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rec, ok := parseAdmitted(got)
	if !ok {
		t.Fatalf("stored record %q is not admitted-shaped", got)
	}
	if !rec.NotAfter.Equal(notAfter.UTC()) {
		t.Errorf("recorded NotAfter = %v, want %v", rec.NotAfter, notAfter.UTC())
	}
	if rec.Serial != "42" {
		t.Errorf("recorded Serial = %q, want %q", rec.Serial, "42")
	}
}

func TestMarkJoinIdentityAdmitted_RecordsNotAfterAndSerial(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	notAfter := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	if err := MarkJoinIdentityAdmitted(context.Background(), store, "spiffe://test.cluster/node/node-1", notAfter, "7"); err != nil {
		t.Fatalf("MarkJoinIdentityAdmitted: %v", err)
	}

	got, err := store.Get(context.Background(), joinIdentityPrefix+"spiffe://test.cluster/node/node-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rec, ok := parseAdmitted(got)
	if !ok {
		t.Fatalf("stored record %q is not admitted-shaped", got)
	}
	if !rec.NotAfter.Equal(notAfter.UTC()) {
		t.Errorf("recorded NotAfter = %v, want %v", rec.NotAfter, notAfter.UTC())
	}
	if rec.Serial != "7" {
		t.Errorf("recorded Serial = %q, want %q", rec.Serial, "7")
	}
}

// ---------------------------------------------------------------------------
// RetireJoinIdentity
// ---------------------------------------------------------------------------

func TestRetireJoinIdentity_InvalidIdentityRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	err := RetireJoinIdentity(context.Background(), store, "test.cluster", "", "")
	var invalid *InvalidJoinIdentityError
	if !errors.As(err, &invalid) {
		t.Fatalf("RetireJoinIdentity with an empty node id = %v, want *InvalidJoinIdentityError", err)
	}
}

func TestRetireJoinIdentity_StoreErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failPutOnPrefix: joinIdentityPrefix}
	err := RetireJoinIdentity(context.Background(), store, "test.cluster", "node-1", "")
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("RetireJoinIdentity with a failing Put = %v, want *JoinTokenStoreError", err)
	}
}

func TestRetireJoinIdentity_BlocksMintJoinTokenWithATypedError(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	if err := RetireJoinIdentity(context.Background(), store, "test.cluster", "node-1", "compromised key"); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}

	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var retired *RetiredJoinIdentityError
	if !errors.As(err, &retired) {
		t.Fatalf("MintJoinToken for a retired identity = %v, want *RetiredJoinIdentityError", err)
	}
	if retired.NodeURI != "spiffe://test.cluster/node/node-1" {
		t.Errorf("RetiredJoinIdentityError.NodeURI = %q, want %q", retired.NodeURI, "spiffe://test.cluster/node/node-1")
	}
}

func TestRetireJoinIdentity_BlocksConsumeJoinTokenGenerically(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	if err := RetireJoinIdentity(context.Background(), store, "test.cluster", "node-1", ""); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}

	// ConsumeJoinToken reports the SAME generic refusal every other cause
	// does -- no oracle distinguishing "retired" from "unknown token".
	_, err = ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	assertJoinRefused(t, err)
}

// TestRetireJoinIdentity_OverridesALapsedAdmission proves retirement beats
// a lapse: an identity that would otherwise be eligible to renew (its
// leaf's NotAfter has passed) must stay permanently refused once retired,
// exactly like one that never lapsed at all.
func TestRetireJoinIdentity_OverridesALapsedAdmission(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identity := "spiffe://test.cluster/node/node-1"
	admitAt(t, store, identity, time.Now().Add(-time.Hour), "1")
	if err := RetireJoinIdentity(context.Background(), store, "test.cluster", "node-1", ""); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}

	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var retired *RetiredJoinIdentityError
	if !errors.As(err, &retired) {
		t.Fatalf("MintJoinToken for a retired-and-lapsed identity = %v, want *RetiredJoinIdentityError (never a renewal)", err)
	}
}

// TestRetireJoinIdentity_IsUnconditional proves retirement succeeds
// regardless of the identity's current state: absent (pre-emptive ban),
// a live reservation, an active admission, or already retired.
func TestRetireJoinIdentity_IsUnconditional(t *testing.T) {
	ctx := context.Background()

	t.Run("absent", func(t *testing.T) {
		store := kvstore.NewMemoryKVStore()
		if err := RetireJoinIdentity(ctx, store, "test.cluster", "never-seen", ""); err != nil {
			t.Fatalf("RetireJoinIdentity on an absent identity: %v", err)
		}
	})

	t.Run("live reservation", func(t *testing.T) {
		store := kvstore.NewMemoryKVStore()
		if _, _, err := ReserveJoinIdentity(ctx, store, "spiffe://test.cluster/node/node-1", 0); err != nil {
			t.Fatalf("ReserveJoinIdentity: %v", err)
		}
		if err := RetireJoinIdentity(ctx, store, "test.cluster", "node-1", ""); err != nil {
			t.Fatalf("RetireJoinIdentity over a live reservation: %v", err)
		}
		got, err := store.Get(ctx, joinIdentityPrefix+"spiffe://test.cluster/node/node-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if classifyIdentityValue(got) != identityRetired {
			t.Fatalf("record after retiring over a reservation is not retired-shaped: %q", got)
		}
	})

	t.Run("active admission", func(t *testing.T) {
		store := kvstore.NewMemoryKVStore()
		admitAt(t, store, "spiffe://test.cluster/node/node-1", time.Now().Add(time.Hour), "1")
		if err := RetireJoinIdentity(ctx, store, "test.cluster", "node-1", ""); err != nil {
			t.Fatalf("RetireJoinIdentity over an active admission: %v", err)
		}
	})

	t.Run("already retired is idempotent", func(t *testing.T) {
		store := kvstore.NewMemoryKVStore()
		if err := RetireJoinIdentity(ctx, store, "test.cluster", "node-1", "first"); err != nil {
			t.Fatalf("RetireJoinIdentity (first): %v", err)
		}
		if err := RetireJoinIdentity(ctx, store, "test.cluster", "node-1", "second"); err != nil {
			t.Fatalf("RetireJoinIdentity (second): %v", err)
		}
		_, _, err := MintJoinToken(ctx, store, "test.cluster", "node-1", time.Hour)
		var retired *RetiredJoinIdentityError
		if !errors.As(err, &retired) {
			t.Fatalf("MintJoinToken after a double retire = %v, want *RetiredJoinIdentityError", err)
		}
	})
}

func TestRetiredJoinIdentityError_ErrorString(t *testing.T) {
	err := &RetiredJoinIdentityError{NodeURI: "spiffe://test.cluster/node/node-1"}
	if err.Error() == "" {
		t.Error("RetiredJoinIdentityError.Error() is empty")
	}
}
