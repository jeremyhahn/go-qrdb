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

package clusterenroll

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// Typed error strings and Unwrap
// ---------------------------------------------------------------------------

func TestJoinTokenErrors_ErrorStrings(t *testing.T) {
	cause := errors.New("boom")
	tests := []error{
		&InvalidJoinIdentityError{Err: cause},
		&JoinTokenTTLError{TTL: time.Second},
		&JoinTokenGenerateError{Err: cause},
		&JoinTokenStoreError{Op: "get", Detail: "k", Err: cause},
		&JoinTokenCollisionError{},
		&JoinIdentityReservationLostError{NodeURI: "spiffe://qrdb.cluster/node/10.0.0.1:9000"},
		&ExistingMemberError{NodeURI: "spiffe://qrdb.cluster/node/10.0.0.1:9000"},
	}
	for _, err := range tests {
		if err.Error() == "" {
			t.Errorf("%T.Error() is empty", err)
		}
	}
}

func TestJoinTokenErrors_Unwrap(t *testing.T) {
	cause := errors.New("boom")
	tests := []struct {
		name string
		err  error
	}{
		{"InvalidJoinIdentityError", &InvalidJoinIdentityError{Err: cause}},
		{"JoinTokenGenerateError", &JoinTokenGenerateError{Err: cause}},
		{"JoinTokenStoreError", &JoinTokenStoreError{Op: "get", Detail: "k", Err: cause}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, cause) {
				t.Errorf("%T does not unwrap to its cause", tt.err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// MintJoinToken
// ---------------------------------------------------------------------------

func TestMintJoinToken_InvalidIdentityRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, _, err := MintJoinToken(context.Background(), store, "", "node-1", time.Hour)
	var identErr *InvalidJoinIdentityError
	if !errors.As(err, &identErr) {
		t.Fatalf("MintJoinToken with an empty trust domain = %v, want *InvalidJoinIdentityError", err)
	}
}

func TestMintJoinToken_TTLTooShortRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Second)
	var ttlErr *JoinTokenTTLError
	if !errors.As(err, &ttlErr) {
		t.Fatalf("MintJoinToken with a too-short TTL = %v, want *JoinTokenTTLError", err)
	}
}

func TestMintJoinToken_TTLTooLongRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", 7*24*time.Hour)
	var ttlErr *JoinTokenTTLError
	if !errors.As(err, &ttlErr) {
		t.Fatalf("MintJoinToken with a too-long TTL = %v, want *JoinTokenTTLError", err)
	}
}

func TestMintJoinToken_DefaultTTLAppliedWhenZero(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	before := time.Now()
	_, expiresAt, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", 0)
	if err != nil {
		t.Fatalf("MintJoinToken with ttl=0: %v", err)
	}
	want := before.Add(DefaultJoinTokenTTL)
	if expiresAt.Before(want.Add(-time.Minute)) || expiresAt.After(want.Add(time.Minute)) {
		t.Fatalf("expiresAt = %v, want close to %v (DefaultJoinTokenTTL)", expiresAt, want)
	}
}

func TestMintJoinToken_StorePutIfAbsentErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failPutIfAbsentOnPrefix: joinTokenPrefix}
	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("MintJoinToken with a failing store = %v, want *JoinTokenStoreError", err)
	}
}

func TestMintJoinToken_DigestCollisionRefused(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), forcePutIfAbsentFalseOnPrefix: joinTokenPrefix}
	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var collisionErr *JoinTokenCollisionError
	if !errors.As(err, &collisionErr) {
		t.Fatalf("MintJoinToken with a forced PutIfAbsent collision = %v, want *JoinTokenCollisionError", err)
	}
}

// ---------------------------------------------------------------------------
// ConsumeJoinToken
// ---------------------------------------------------------------------------

func TestConsumeJoinToken_EmptyTokenRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, err := ConsumeJoinToken(context.Background(), store, "test.cluster", "")
	assertJoinRefused(t, err)
}

func TestConsumeJoinToken_UnknownTokenRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, err := ConsumeJoinToken(context.Background(), store, "test.cluster", "never-minted")
	assertJoinRefused(t, err)
}

func TestConsumeJoinToken_GetErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failGetOnPrefix: joinTokenPrefix}
	_, err := ConsumeJoinToken(context.Background(), store, "test.cluster", "any-token")
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("ConsumeJoinToken with a failing Get = %v, want *JoinTokenStoreError", err)
	}
}

func TestConsumeJoinToken_CorruptRecordRefusedAndDeleted(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	// Corrupt the stored record in place.
	digest := invite.TokenDigest(token)
	key := joinTokenPrefix + digest
	if err := store.Put(context.Background(), key, []byte("not json")); err != nil {
		t.Fatalf("Put (corrupt): %v", err)
	}

	_, err = ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	assertJoinRefused(t, err)

	// The corrupt record must not be left behind.
	data, getErr := store.Get(context.Background(), key)
	if getErr != nil {
		t.Fatalf("Get after corrupt-record consume: %v", getErr)
	}
	if data != nil {
		t.Fatal("corrupt record was not deleted")
	}
}

func TestConsumeJoinToken_CompareAndDeleteErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failCompareAndDeleteOnPrefix: joinTokenPrefix}
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	_, err = ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("ConsumeJoinToken with a failing CompareAndDelete = %v, want *JoinTokenStoreError", err)
	}
}

func TestConsumeJoinToken_LostRaceRefused(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), forceCompareAndDeleteFalseOnPrefix: joinTokenPrefix}
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	_, err = ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	assertJoinRefused(t, err)
}

func TestConsumeJoinToken_ExpiredRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", MinJoinTokenTTL)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	// Rewrite the record with an already-past expiry.
	digest := invite.TokenDigest(token)
	key := joinTokenPrefix + digest
	data, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var record joinTokenRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	record.ExpiresAt = time.Now().Add(-time.Hour)
	rewritten, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := store.Put(context.Background(), key, rewritten); err != nil {
		t.Fatalf("Put (rewritten): %v", err)
	}

	_, err = ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	assertJoinRefused(t, err)
}

func TestConsumeJoinToken_WrongTrustDomainRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	_, err = ConsumeJoinToken(context.Background(), store, "other.cluster", token)
	assertJoinRefused(t, err)
}

// TestConsumeJoinToken_IdentityAdmissionExistsErrorSurfacesTyped proves
// ConsumeJoinToken's read-only "already admitted or reserved" check
// (identityBlocks, Get-based since B1's TTL-aware reservations) surfaces a
// genuine store failure as *JoinTokenStoreError, distinct from a refusal.
func TestConsumeJoinToken_IdentityAdmissionExistsErrorSurfacesTyped(t *testing.T) {
	// Minted against the clean backing store: MintJoinToken now runs its
	// OWN identityBlocks check against the identity ledger too (see
	// TestMintJoinToken_IdentityAdmissionExistsErrorSurfacesTyped), so the
	// fault must be injected only for the Consume call below, not the mint
	// above.
	backing := kvstore.NewMemoryKVStore()
	token, _, err := MintJoinToken(context.Background(), backing, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	faulty := &faultyJoinStore{AtomicKVStore: backing, failGetOnPrefix: joinIdentityPrefix}
	_, err = ConsumeJoinToken(context.Background(), faulty, "test.cluster", token)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("ConsumeJoinToken with a failing identity Get check = %v, want *JoinTokenStoreError", err)
	}
}

// TestMintJoinToken_IdentityAdmissionExistsErrorSurfacesTyped proves
// MintJoinToken's own already-admitted-or-reserved check (run before any
// token is generated) surfaces a genuine store failure as
// *JoinTokenStoreError.
func TestMintJoinToken_IdentityAdmissionExistsErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failGetOnPrefix: joinIdentityPrefix}
	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("MintJoinToken with a failing identity Get check = %v, want *JoinTokenStoreError", err)
	}
}

// TestMintJoinToken_ExistingMemberRefused proves MintJoinToken itself
// refuses to mint a second token for a node identity already recorded in
// the join-identity ledger, with the typed *ExistingMemberError -- the
// coordinator's "existing member" rule: the cluster CA has already issued
// (or recorded) a certificate for this identity.
func TestMintJoinToken_ExistingMemberRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identity := spiffe.NodeIdentity{TrustDomain: "test.cluster", NodeID: "node-1"}
	if err := MarkJoinIdentityAdmitted(context.Background(), store, identity.String()); err != nil {
		t.Fatalf("MarkJoinIdentityAdmitted: %v", err)
	}

	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var existing *ExistingMemberError
	if !errors.As(err, &existing) {
		t.Fatalf("MintJoinToken for an already-admitted identity = %v, want *ExistingMemberError", err)
	}
}

// TestMarkJoinIdentityAdmitted_PutIfAbsentErrorSurfacesTyped proves
// MarkJoinIdentityAdmitted -- called only after a successful issuance, per
// HandleJoinRequest -- surfaces a genuine store failure as
// *JoinTokenStoreError.
func TestMarkJoinIdentityAdmitted_PutIfAbsentErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failPutIfAbsentOnPrefix: joinIdentityPrefix}
	err := MarkJoinIdentityAdmitted(context.Background(), store, "spiffe://test.cluster/node/node-1")
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("MarkJoinIdentityAdmitted with a failing PutIfAbsent = %v, want *JoinTokenStoreError", err)
	}
}

func TestConsumeJoinToken_AlreadyAdmittedIdentityRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	// Both tokens are minted while node-1 is NOT YET admitted: MintJoinToken
	// now refuses outright once an identity is admitted (see
	// TestMintJoinToken_ExistingMemberRefused), so the only way a second,
	// otherwise-valid token can exist for an identity that becomes admitted
	// is for both to have been minted before either was consumed -- the
	// race ConsumeJoinToken's own check protects against.
	tokenOne, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken (first): %v", err)
	}
	tokenTwo, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken (second): %v", err)
	}

	boundNodeURI, err := ConsumeJoinToken(context.Background(), store, "test.cluster", tokenOne)
	if err != nil {
		t.Fatalf("ConsumeJoinToken (first, should succeed): %v", err)
	}
	// Mirrors HandleJoinRequest: the identity is marked admitted only
	// after issuance would have succeeded -- ConsumeJoinToken itself never
	// does this (see jointoken.go's doc comment).
	if err := MarkJoinIdentityAdmitted(context.Background(), store, boundNodeURI); err != nil {
		t.Fatalf("MarkJoinIdentityAdmitted: %v", err)
	}

	// The SECOND token, minted earlier for the SAME node identity before it
	// was admitted, must now be refused on presentation.
	_, err = ConsumeJoinToken(context.Background(), store, "test.cluster", tokenTwo)
	assertJoinRefused(t, err)
}

// TestConsumeJoinToken_SignFailureAfterConsumeLeavesIdentityRetryable
// proves the core guarantee HandleJoinRequest's reordering exists for: a
// token consumed by ConsumeJoinToken, with NO subsequent
// MarkJoinIdentityAdmitted call (standing in for a signing failure between
// the two), leaves the identity free for a freshly minted token to
// succeed -- the node ID is never retired by a transient post-consumption
// failure.
func TestConsumeJoinToken_SignFailureAfterConsumeLeavesIdentityRetryable(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	tokenOne, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken (first): %v", err)
	}
	if _, err := ConsumeJoinToken(context.Background(), store, "test.cluster", tokenOne); err != nil {
		t.Fatalf("ConsumeJoinToken (first): %v", err)
	}
	// Signing "fails" here: HandleJoinRequest would return without ever
	// calling MarkJoinIdentityAdmitted.

	tokenTwo, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken (retry): %v", err)
	}
	if _, err := ConsumeJoinToken(context.Background(), store, "test.cluster", tokenTwo); err != nil {
		t.Fatalf("ConsumeJoinToken (retry) = %v, want success: the identity must not be retired by an unmarked consume", err)
	}
}

// ---------------------------------------------------------------------------
// ReserveJoinIdentity / PromoteJoinIdentityAdmitted / ReleaseJoinIdentityReservation
// ---------------------------------------------------------------------------

func TestReserveJoinIdentity_FirstCallReserves(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	reserved, pending, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}
	if !reserved {
		t.Fatal("ReserveJoinIdentity = false on a fresh identity, want true")
	}
	if len(pending) == 0 {
		t.Fatal("ReserveJoinIdentity returned no pending value alongside reserved=true")
	}
}

func TestReserveJoinIdentity_SecondCallRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	if _, _, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0); err != nil {
		t.Fatalf("ReserveJoinIdentity (first): %v", err)
	}
	reserved, _, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity (second): %v", err)
	}
	if reserved {
		t.Fatal("ReserveJoinIdentity (second) = true, want false: the identity is already reserved")
	}
}

func TestReserveJoinIdentity_StoreErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failPutIfAbsentOnPrefix: joinIdentityPrefix}
	_, _, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("ReserveJoinIdentity (store error) = %v, want *JoinTokenStoreError", err)
	}
}

// TestReserveJoinIdentity_ReclaimsExpiredReservation proves B1: a PENDING
// record whose deadline has already passed -- however it got left behind
// (a crash between Reserve and Promote/Release, a failed Promote, a
// Release that could not run) -- does not wedge the node id forever.
// ReserveJoinIdentity reclaims it by CompareAndSwap from the exact stale
// value, not a blind overwrite.
func TestReserveJoinIdentity_ReclaimsExpiredReservation(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identityKey := joinIdentityPrefix + "node-1"
	stale := pendingValue(time.Now().Add(-time.Minute))
	if err := store.Put(context.Background(), identityKey, stale); err != nil {
		t.Fatalf("Put (stale pending): %v", err)
	}

	reserved, pending, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity (reclaim): %v", err)
	}
	if !reserved {
		t.Fatal("ReserveJoinIdentity did not reclaim an expired reservation")
	}
	if string(pending) == string(stale) {
		t.Fatal("ReserveJoinIdentity's reclaimed value is identical to the stale one it was supposed to replace")
	}
	deadline, ok := parsePendingDeadline(pending)
	if !ok || !deadline.After(time.Now()) {
		t.Fatalf("reclaimed pending value = %q, want a live (future) deadline", pending)
	}
}

// TestReserveJoinIdentity_DoesNotReclaimALiveReservation proves the
// reclaim is scoped to EXPIRED reservations only: one still within its
// TTL is not touched.
func TestReserveJoinIdentity_DoesNotReclaimALiveReservation(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, first, err := ReserveJoinIdentity(context.Background(), store, "node-1", time.Hour)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity (first): %v", err)
	}
	reserved, _, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity (second): %v", err)
	}
	if reserved {
		t.Fatal("ReserveJoinIdentity reclaimed a reservation still within its TTL")
	}
	got, err := store.Get(context.Background(), joinIdentityPrefix+"node-1")
	if err != nil || string(got) != string(first) {
		t.Fatalf("the live reservation's value changed: got %q, want the original %q", got, first)
	}
}

func TestPromoteJoinIdentityAdmitted_SwapsAReservation(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, pending, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}
	if err := PromoteJoinIdentityAdmitted(context.Background(), store, "node-1", pending); err != nil {
		t.Fatalf("PromoteJoinIdentityAdmitted: %v", err)
	}
	// A second reservation attempt must now be refused: the identity is
	// admitted, not merely pending.
	reserved, _, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity (after promotion): %v", err)
	}
	if reserved {
		t.Fatal("ReserveJoinIdentity after promotion = true, want false: the identity is already admitted")
	}
}

func TestPromoteJoinIdentityAdmitted_LostReservationRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	// No ReserveJoinIdentity call: the PENDING record this promotion
	// expects was never written (or was already released/promoted by
	// someone else). The exact bytes passed do not matter -- the key
	// itself is absent, so any CompareAndSwap against it fails.
	err := PromoteJoinIdentityAdmitted(context.Background(), store, "node-1", pendingValue(time.Now().Add(time.Minute)))
	var lostErr *JoinIdentityReservationLostError
	if !errors.As(err, &lostErr) {
		t.Fatalf("PromoteJoinIdentityAdmitted (no reservation) = %v, want *JoinIdentityReservationLostError", err)
	}
	if lostErr.NodeURI != "node-1" {
		t.Fatalf("JoinIdentityReservationLostError.NodeURI = %q, want %q", lostErr.NodeURI, "node-1")
	}
}

func TestPromoteJoinIdentityAdmitted_StoreErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failCompareAndSwapOnPrefix: joinIdentityPrefix}
	err := PromoteJoinIdentityAdmitted(context.Background(), store, "node-1", pendingValue(time.Now().Add(time.Minute)))
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("PromoteJoinIdentityAdmitted (store error) = %v, want *JoinTokenStoreError", err)
	}
}

// TestPromoteJoinIdentityAdmitted_RecoversWhenTheSwapCommittedDespiteAnError
// is the B1 residual review item: a CompareAndSwap error means the swap's
// OUTCOME is unknown, not that it failed. A timeout arriving after the
// write actually landed looks identical, from here, to one before it did
// -- and treating every error as "did not happen" leaves the record
// admitted forever (no later Reserve/Mint/Consume can ever reclaim an
// ADMITTED value the way it reclaims an expired PENDING one) while the
// caller, told the promotion failed, refuses to return the leaf it
// already signed. The fix re-reads the key on error and reports success
// when it finds exactly the admitted value just attempted.
func TestPromoteJoinIdentityAdmitted_RecoversWhenTheSwapCommittedDespiteAnError(t *testing.T) {
	ctx := context.Background()
	store := &faultyJoinStore{
		AtomicKVStore:                         kvstore.NewMemoryKVStore(),
		failCompareAndSwapAfterCommitOnPrefix: joinIdentityPrefix,
	}
	const nodeURI = "node-ambiguous-promote"

	reserved, pending, err := ReserveJoinIdentity(ctx, store, nodeURI, 0)
	if err != nil || !reserved {
		t.Fatalf("ReserveJoinIdentity: reserved=%v, err=%v", reserved, err)
	}

	if err := PromoteJoinIdentityAdmitted(ctx, store, nodeURI, pending); err != nil {
		t.Fatalf("PromoteJoinIdentityAdmitted = %v, want nil: the swap committed despite the injected error, so this must recover and report success", err)
	}

	// The record is genuinely admitted now, not merely treated as if: a
	// second reservation attempt for the same identity must see it as
	// admitted (identityBlocks' non-pending branch), and the stored value
	// must not still parse as a live PENDING record.
	identityKey := joinIdentityPrefix + nodeURI
	got, getErr := store.Get(ctx, identityKey)
	if getErr != nil {
		t.Fatalf("Get after recovery: %v", getErr)
	}
	if _, isPending := parsePendingDeadline(got); isPending {
		t.Fatalf("identity record is still PENDING after a recovered promotion: %q", got)
	}
	if reserved, _, err := ReserveJoinIdentity(ctx, store, nodeURI, 0); err != nil || reserved {
		t.Fatalf("ReserveJoinIdentity after recovered promotion: reserved=%v, err=%v, want false, nil", reserved, err)
	}
}

// TestPromoteJoinIdentityAdmitted_StillFailsWhenTheSwapNeverCommitted is
// the companion case: when the re-read does NOT find the admitted value
// (the swap genuinely never landed), the original error must still
// surface -- the recovery path must not mask a real failure.
func TestPromoteJoinIdentityAdmitted_StillFailsWhenTheSwapNeverCommitted(t *testing.T) {
	ctx := context.Background()
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failCompareAndSwapOnPrefix: joinIdentityPrefix}
	const nodeURI = "node-genuine-promote-failure"

	reserved, pending, err := ReserveJoinIdentity(ctx, store, nodeURI, 0)
	if err != nil || !reserved {
		t.Fatalf("ReserveJoinIdentity: reserved=%v, err=%v", reserved, err)
	}

	err = PromoteJoinIdentityAdmitted(ctx, store, nodeURI, pending)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("PromoteJoinIdentityAdmitted (swap never committed) = %v, want *JoinTokenStoreError", err)
	}
}

// TestPromoteJoinIdentityAdmitted_SurvivesACanceledCallerContext proves B1
// item 3: Promote finalizes a signing success that already happened, so a
// caller ctx canceled at exactly this moment (the client gave up, or the
// request's own deadline passed while IssueLeafForKey was signing) must
// not also abort the write that keeps the ledger consistent.
func TestPromoteJoinIdentityAdmitted_SurvivesACanceledCallerContext(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, pending, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := PromoteJoinIdentityAdmitted(canceled, store, "node-1", pending); err != nil {
		t.Fatalf("PromoteJoinIdentityAdmitted with a canceled caller ctx: %v", err)
	}
}

func TestReleaseJoinIdentityReservation_ReleasesAReservation(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, pending, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}
	released, err := ReleaseJoinIdentityReservation(context.Background(), store, "node-1", pending)
	if err != nil {
		t.Fatalf("ReleaseJoinIdentityReservation: %v", err)
	}
	if !released {
		t.Fatal("ReleaseJoinIdentityReservation = false, want true: a PENDING reservation was there to release")
	}
	// A fresh reservation attempt must now succeed again.
	reserved, _, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity (after release): %v", err)
	}
	if !reserved {
		t.Fatal("ReserveJoinIdentity after release = false, want true: the reservation should have been cleared")
	}
}

func TestReleaseJoinIdentityReservation_AlreadyGoneLogsAndReportsFalse(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	// No reservation was ever made for this identity.
	released, err := ReleaseJoinIdentityReservation(context.Background(), store, "node-1", pendingValue(time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatalf("ReleaseJoinIdentityReservation: %v", err)
	}
	if released {
		t.Fatal("ReleaseJoinIdentityReservation = true, want false: there was nothing to release")
	}
}

func TestReleaseJoinIdentityReservation_StoreErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failCompareAndDeleteOnPrefix: joinIdentityPrefix}
	_, err := ReleaseJoinIdentityReservation(context.Background(), store, "node-1", pendingValue(time.Now().Add(time.Minute)))
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("ReleaseJoinIdentityReservation (store error) = %v, want *JoinTokenStoreError", err)
	}
}

// TestReleaseJoinIdentityReservation_SurvivesACanceledCallerContext proves
// B1 item 3 for Release: it finalizes a signing FAILURE that already
// happened, so a caller ctx canceled at exactly this moment must not also
// abort the cleanup that keeps the node id from wedging.
func TestReleaseJoinIdentityReservation_SurvivesACanceledCallerContext(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	_, pending, err := ReserveJoinIdentity(context.Background(), store, "node-1", 0)
	if err != nil {
		t.Fatalf("ReserveJoinIdentity: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	released, err := ReleaseJoinIdentityReservation(canceled, store, "node-1", pending)
	if err != nil {
		t.Fatalf("ReleaseJoinIdentityReservation with a canceled caller ctx: %v", err)
	}
	if !released {
		t.Fatal("ReleaseJoinIdentityReservation with a canceled caller ctx = false, want true")
	}
}

// TestMintJoinToken_TreatsExpiredPendingReservationAsAbsent and
// TestConsumeJoinToken_TreatsExpiredPendingReservationAsAbsent prove B1's
// reclaim rule from the other two entry points that read the same ledger:
// an expired PENDING record must not refuse a fresh mint or consume any
// more than ReserveJoinIdentity itself refuses to be blocked by one.
func TestMintJoinToken_TreatsExpiredPendingReservationAsAbsent(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identity := "spiffe://test.cluster/node/node-1"
	if err := store.Put(context.Background(), joinIdentityPrefix+identity, pendingValue(time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("Put (stale pending): %v", err)
	}
	if _, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour); err != nil {
		t.Fatalf("MintJoinToken with an expired pending reservation: %v", err)
	}
}

func TestConsumeJoinToken_TreatsExpiredPendingReservationAsAbsent(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	identity := "spiffe://test.cluster/node/node-1"
	if err := store.Put(context.Background(), joinIdentityPrefix+identity, pendingValue(time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("Put (stale pending): %v", err)
	}
	if _, err := ConsumeJoinToken(context.Background(), store, "test.cluster", token); err != nil {
		t.Fatalf("ConsumeJoinToken with an expired pending reservation: %v", err)
	}
}

// TestMintJoinToken_RefusedByALivePendingReservation and
// TestConsumeJoinToken_RefusedByALivePendingReservation prove the
// complement: a reservation still within its TTL blocks a fresh mint or
// consume exactly as an admitted record does.
func TestMintJoinToken_RefusedByALivePendingReservation(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	identity := "spiffe://test.cluster/node/node-1"
	if err := store.Put(context.Background(), joinIdentityPrefix+identity, pendingValue(time.Now().Add(time.Hour))); err != nil {
		t.Fatalf("Put (live pending): %v", err)
	}
	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var existing *ExistingMemberError
	if !errors.As(err, &existing) {
		t.Fatalf("MintJoinToken with a live pending reservation = %v, want *ExistingMemberError", err)
	}
}

func TestConsumeJoinToken_RefusedByALivePendingReservation(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	identity := "spiffe://test.cluster/node/node-1"
	if err := store.Put(context.Background(), joinIdentityPrefix+identity, pendingValue(time.Now().Add(time.Hour))); err != nil {
		t.Fatalf("Put (live pending): %v", err)
	}
	if _, err := ConsumeJoinToken(context.Background(), store, "test.cluster", token); err == nil {
		t.Fatal("ConsumeJoinToken with a live pending reservation succeeded, want refusal")
	}
}

// TestParsePendingDeadline covers pendingValue's encoding directly: a
// value this package wrote round-trips, an admitted RFC 3339 timestamp is
// NOT pending-shaped, and a malformed or foreign value is NOT
// pending-shaped either (fail closed: treated as blocking, never as
// absent).
func TestParsePendingDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute).Truncate(time.Second)
	got, ok := parsePendingDeadline(pendingValue(deadline))
	if !ok || !got.Equal(deadline) {
		t.Fatalf("parsePendingDeadline(pendingValue(%v)) = %v, %v; want %v, true", deadline, got, ok, deadline)
	}

	if _, ok := parsePendingDeadline([]byte(time.Now().UTC().Format(time.RFC3339))); ok {
		t.Fatal("parsePendingDeadline treated an admitted RFC 3339 timestamp as pending-shaped")
	}
	if _, ok := parsePendingDeadline([]byte("pending:not-a-number")); ok {
		t.Fatal("parsePendingDeadline treated a malformed pending value as pending-shaped")
	}
	if _, ok := parsePendingDeadline([]byte("garbage")); ok {
		t.Fatal("parsePendingDeadline treated an unrelated value as pending-shaped")
	}
}

// assertJoinRefused fails the test unless err is *JoinRefusedError.
func assertJoinRefused(t *testing.T, err error) {
	t.Helper()
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("got %v, want *JoinRefusedError", err)
	}
}

// ---------------------------------------------------------------------------
// faultyJoinStore: a kvstore.AtomicKVStore that fails or forces a specific
// outcome for keys under a given prefix, for exercising ConsumeJoinToken's
// and MintJoinToken's own store-failure branches without a real backend
// fault.
// ---------------------------------------------------------------------------

type faultyJoinStore struct {
	kvstore.AtomicKVStore

	failGetOnPrefix              string
	failExistsOnPrefix           string
	failPutIfAbsentOnPrefix      string
	failCompareAndDeleteOnPrefix string
	failCompareAndSwapOnPrefix   string

	forcePutIfAbsentFalseOnPrefix      string
	forceCompareAndDeleteFalseOnPrefix string
	forceCompareAndSwapFalseOnPrefix   string

	// failCompareAndSwapAfterCommitOnPrefix simulates the ambiguous-outcome
	// case failCompareAndSwapOnPrefix does not: the swap is performed for
	// real against the embedded store (so it DOES commit when the compare
	// matches) and an error is returned anyway, the shape of a timeout
	// that arrives after the write already landed.
	failCompareAndSwapAfterCommitOnPrefix string
}

func (f *faultyJoinStore) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failGetOnPrefix != "" && strings.HasPrefix(key, f.failGetOnPrefix) {
		return nil, errInjectedFault
	}
	return f.AtomicKVStore.Get(ctx, key)
}

func (f *faultyJoinStore) Exists(ctx context.Context, key string) (bool, error) {
	if f.failExistsOnPrefix != "" && strings.HasPrefix(key, f.failExistsOnPrefix) {
		return false, errInjectedFault
	}
	return f.AtomicKVStore.Exists(ctx, key)
}

func (f *faultyJoinStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.failPutIfAbsentOnPrefix != "" && strings.HasPrefix(key, f.failPutIfAbsentOnPrefix) {
		return false, errInjectedFault
	}
	if f.forcePutIfAbsentFalseOnPrefix != "" && strings.HasPrefix(key, f.forcePutIfAbsentFalseOnPrefix) {
		return false, nil
	}
	return f.AtomicKVStore.PutIfAbsent(ctx, key, value)
}

func (f *faultyJoinStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if f.failCompareAndDeleteOnPrefix != "" && strings.HasPrefix(key, f.failCompareAndDeleteOnPrefix) {
		return false, errInjectedFault
	}
	if f.forceCompareAndDeleteFalseOnPrefix != "" && strings.HasPrefix(key, f.forceCompareAndDeleteFalseOnPrefix) {
		return false, nil
	}
	return f.AtomicKVStore.CompareAndDelete(ctx, key, expected)
}

func (f *faultyJoinStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if f.failCompareAndSwapOnPrefix != "" && strings.HasPrefix(key, f.failCompareAndSwapOnPrefix) {
		return false, errInjectedFault
	}
	if f.forceCompareAndSwapFalseOnPrefix != "" && strings.HasPrefix(key, f.forceCompareAndSwapFalseOnPrefix) {
		return false, nil
	}
	if f.failCompareAndSwapAfterCommitOnPrefix != "" && strings.HasPrefix(key, f.failCompareAndSwapAfterCommitOnPrefix) {
		swapped, err := f.AtomicKVStore.CompareAndSwap(ctx, key, expected, next)
		if err != nil {
			return swapped, err
		}
		return swapped, errInjectedFault
	}
	return f.AtomicKVStore.CompareAndSwap(ctx, key, expected, next)
}
