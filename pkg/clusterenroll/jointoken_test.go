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
// ConsumeJoinToken's read-only "already admitted" check surfaces a genuine
// store failure as *JoinTokenStoreError, distinct from a refusal.
func TestConsumeJoinToken_IdentityAdmissionExistsErrorSurfacesTyped(t *testing.T) {
	// Minted against the clean backing store: MintJoinToken now runs its
	// OWN Exists check against the identity ledger too (see
	// TestMintJoinToken_IdentityAdmissionExistsErrorSurfacesTyped), so the
	// fault must be injected only for the Consume call below, not the mint
	// above.
	backing := kvstore.NewMemoryKVStore()
	token, _, err := MintJoinToken(context.Background(), backing, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	faulty := &faultyJoinStore{AtomicKVStore: backing, failExistsOnPrefix: joinIdentityPrefix}
	_, err = ConsumeJoinToken(context.Background(), faulty, "test.cluster", token)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("ConsumeJoinToken with a failing identity Exists check = %v, want *JoinTokenStoreError", err)
	}
}

// TestMintJoinToken_IdentityAdmissionExistsErrorSurfacesTyped proves
// MintJoinToken's own already-admitted check (run before any token is
// generated) surfaces a genuine store failure as *JoinTokenStoreError.
func TestMintJoinToken_IdentityAdmissionExistsErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failExistsOnPrefix: joinIdentityPrefix}
	_, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("MintJoinToken with a failing identity Exists check = %v, want *JoinTokenStoreError", err)
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

	forcePutIfAbsentFalseOnPrefix      string
	forceCompareAndDeleteFalseOnPrefix string
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
