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

func TestConsumeJoinToken_IdentityAdmissionPutIfAbsentErrorSurfacesTyped(t *testing.T) {
	store := &faultyJoinStore{AtomicKVStore: kvstore.NewMemoryKVStore(), failPutIfAbsentOnPrefix: joinIdentityPrefix}
	token, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	_, err = ConsumeJoinToken(context.Background(), store, "test.cluster", token)
	var storeErr *JoinTokenStoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("ConsumeJoinToken with a failing identity PutIfAbsent = %v, want *JoinTokenStoreError", err)
	}
}

func TestConsumeJoinToken_AlreadyAdmittedIdentityRefused(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	tokenOne, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken (first): %v", err)
	}
	if _, err := ConsumeJoinToken(context.Background(), store, "test.cluster", tokenOne); err != nil {
		t.Fatalf("ConsumeJoinToken (first, should succeed): %v", err)
	}

	// A SECOND token minted for the SAME node identity: the identity is
	// already admitted, so this presentation must be refused even though
	// the token itself is freshly minted and otherwise valid.
	tokenTwo, _, err := MintJoinToken(context.Background(), store, "test.cluster", "node-1", time.Hour)
	if err != nil {
		t.Fatalf("MintJoinToken (second): %v", err)
	}
	_, err = ConsumeJoinToken(context.Background(), store, "test.cluster", tokenTwo)
	assertJoinRefused(t, err)
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
