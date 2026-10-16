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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newFactorSetService builds the service over kv. The factor-set record is
// kept only when kv also has the guarded writes.
func newFactorSetService(t *testing.T, kv AuthFactorKV) UserAuthFactorServicer {
	t.Helper()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: kv})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc
}

// noActive is the one-time-claim condition.
var noActive = auth.FactorAddCondition{NoActiveOfType: true}

// atVersion is the optimistic condition on version v.
func atVersion(v uint64) auth.FactorAddCondition {
	return auth.FactorAddCondition{ExpectedVersion: &v}
}

// assertConditionFailed requires err to be the typed condition refusal and
// returns it.
func assertConditionFailed(t *testing.T, err error) *auth.FactorConditionFailedError {
	t.Helper()
	if !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("err = %v, want ErrFactorConditionFailed", err)
	}
	cfe, ok := errors.AsType[*auth.FactorConditionFailedError](err)
	if !ok {
		t.Fatalf("err = %T, want *auth.FactorConditionFailedError", err)
	}
	return cfe
}

// assertNoPrimary requires that no primary record exists for id.
func assertNoPrimary(t *testing.T, kv AuthFactorKV, id string) {
	t.Helper()
	raw, err := kv.Get(context.Background(), primaryKey(id))
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	if len(raw) != 0 {
		t.Fatalf("primary record of refused factor %s was left behind", id)
	}
}

// assertSetVersion requires userID's factor set to be at want.
func assertSetVersion(t *testing.T, svc UserAuthFactorServicer, userID, want uint64) {
	t.Helper()
	got, err := svc.FactorSetVersion(context.Background(), userID)
	if err != nil {
		t.Fatalf("FactorSetVersion: %v", err)
	}
	if got != want {
		t.Fatalf("FactorSetVersion = %d, want %d", got, want)
	}
}

func TestAddConditional_noActiveOfType_holds(t *testing.T) {
	kv := kvstore.NewMemoryKVStore()
	svc := newFactorSetService(t, kv)
	ctx := context.Background()

	version, err := svc.AddConditional(ctx, 7, mtlsFactor("f-1", 7, []byte("k1")), noActive)
	if err != nil {
		t.Fatalf("AddConditional: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
	assertSetVersion(t, svc, 7, 1)
	if _, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k1")); err != nil {
		t.Fatalf("LookupByCredentialKey after conditional add: %v", err)
	}

	// A different type is not held, so the same condition holds for it.
	if _, err := svc.AddConditional(ctx, 7, fido2Factor("f-2", 7, []byte("k2")), noActive); err != nil {
		t.Fatalf("AddConditional(fido2): %v", err)
	}
	assertSetVersion(t, svc, 7, 2)
}

func TestAddConditional_noActiveOfType_fails(t *testing.T) {
	kv := kvstore.NewMemoryKVStore()
	svc := newFactorSetService(t, kv)
	ctx := context.Background()

	if err := svc.Add(ctx, 7, mtlsFactor("f-1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	_, err := svc.AddConditional(ctx, 7, mtlsFactor("f-2", 7, []byte("k2")), noActive)
	cfe := assertConditionFailed(t, err)
	if cfe.Condition != auth.FactorConditionNoActiveOfType || cfe.ExistingFactorID != "f-1" ||
		cfe.UserID != 7 || cfe.Type != auth.AuthFactorMTLS || cfe.CurrentVersion != 1 {
		t.Fatalf("refusal = %+v", cfe)
	}
	assertNoPrimary(t, kv, "f-2")
	assertSetVersion(t, svc, 7, 1)
	if _, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k2")); err == nil {
		t.Fatal("refused factor resolves by credential")
	}
	// The refused credential is not held: a later add of it succeeds.
	if err := svc.Remove(ctx, "f-1", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := svc.AddConditional(ctx, 7, mtlsFactor("f-3", 7, []byte("k2")), noActive); err != nil {
		t.Fatalf("AddConditional after revoke: %v", err)
	}
}

func TestAddConditional_revokedFactorDoesNotCount(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	if err := svc.Add(ctx, 7, mtlsFactor("f-1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Add(ctx, 7, fido2Factor("f-2", 7, []byte("k2"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Remove(ctx, "f-1", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := svc.AddConditional(ctx, 7, mtlsFactor("f-3", 7, []byte("k3")), noActive); err != nil {
		t.Fatalf("AddConditional with only a revoked mtls factor: %v", err)
	}
}

func TestAddConditional_otherUserDoesNotCount(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	if err := svc.Add(ctx, 8, mtlsFactor("f-1", 8, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := svc.AddConditional(ctx, 7, mtlsFactor("f-2", 7, []byte("k2")), noActive); err != nil {
		t.Fatalf("AddConditional for a user holding nothing: %v", err)
	}
}

func TestAddConditional_expectedVersion(t *testing.T) {
	kv := kvstore.NewMemoryKVStore()
	svc := newFactorSetService(t, kv)
	ctx := context.Background()

	version, err := svc.AddConditional(ctx, 7, mtlsFactor("f-1", 7, []byte("k1")), atVersion(0))
	if err != nil {
		t.Fatalf("AddConditional at version 0: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}

	_, err = svc.AddConditional(ctx, 7, fido2Factor("f-2", 7, []byte("k2")), atVersion(0))
	cfe := assertConditionFailed(t, err)
	if cfe.Condition != auth.FactorConditionExpectedVersion || cfe.ExpectedVersion != 0 || cfe.CurrentVersion != 1 {
		t.Fatalf("refusal = %+v", cfe)
	}
	assertNoPrimary(t, kv, "f-2")

	version, err = svc.AddConditional(ctx, 7, fido2Factor("f-2", 7, []byte("k2")), atVersion(1))
	if err != nil {
		t.Fatalf("AddConditional at version 1: %v", err)
	}
	if version != 2 {
		t.Fatalf("version = %d, want 2", version)
	}
}

func TestAddConditional_bothConditions(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	if err := svc.Add(ctx, 7, mtlsFactor("f-1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// The version is checked first: a stale caller learns the set moved.
	both := atVersion(0)
	both.NoActiveOfType = true
	_, err := svc.AddConditional(ctx, 7, mtlsFactor("f-2", 7, []byte("k2")), both)
	if cfe := assertConditionFailed(t, err); cfe.Condition != auth.FactorConditionExpectedVersion {
		t.Fatalf("condition = %q, want expected_version", cfe.Condition)
	}
	// At the right version the type check still refuses.
	both = atVersion(1)
	both.NoActiveOfType = true
	_, err = svc.AddConditional(ctx, 7, mtlsFactor("f-2", 7, []byte("k2")), both)
	if cfe := assertConditionFailed(t, err); cfe.Condition != auth.FactorConditionNoActiveOfType {
		t.Fatalf("condition = %q, want no_active_of_type", cfe.Condition)
	}
}

func TestAddConditional_invalidInput(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	cases := map[string]struct {
		userID uint64
		factor *auth.AuthFactor
		cond   auth.FactorAddCondition
	}{
		"empty condition": {userID: 7, factor: mtlsFactor("f-1", 7, []byte("k1"))},
		"zero user":       {userID: 0, factor: mtlsFactor("f-1", 0, []byte("k1")), cond: noActive},
		"nil factor":      {userID: 7, cond: noActive},
		"empty credential": {
			userID: 7, factor: mtlsFactor("f-1", 7, nil), cond: noActive,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := svc.AddConditional(ctx, tc.userID, tc.factor, tc.cond)
			if !errors.Is(err, auth.ErrInvalidFactor) {
				t.Fatalf("err = %v, want ErrInvalidFactor", err)
			}
		})
	}
	assertSetVersion(t, svc, 7, 0)
}

// plainFactorKV exposes only AuthFactorKV: a store with no guarded write.
type plainFactorKV struct {
	AuthFactorKV
}

func TestAddConditional_nonAtomicStoreRefused(t *testing.T) {
	kv := plainFactorKV{AuthFactorKV: kvstore.NewMemoryKVStore()}
	svc := newFactorSetService(t, kv)
	ctx := context.Background()

	_, err := svc.AddConditional(ctx, 7, mtlsFactor("f-1", 7, []byte("k1")), noActive)
	if !errors.Is(err, ErrUserAuthFactorAtomicStoreRequired) {
		t.Fatalf("err = %v, want ErrUserAuthFactorAtomicStoreRequired", err)
	}
	if raw, _ := kv.Get(ctx, primaryKey("f-1")); len(raw) != 0 {
		t.Fatal("refused conditional add wrote a primary record")
	}
	if v, err := svc.FactorSetVersion(ctx, 7); err != nil || v != 0 {
		t.Fatalf("FactorSetVersion = %d, %v; want 0, nil (no record is kept)", v, err)
	}
	// Plain membership changes keep working without the record.
	if err := svc.Add(ctx, 7, mtlsFactor("f-2", 7, []byte("k2"))); err != nil {
		t.Fatalf("Add on a non-atomic store: %v", err)
	}
	if err := svc.Rotate(ctx, "f-2", mtlsFactor("f-3", 7, []byte("k3"))); err != nil {
		t.Fatalf("Rotate on a non-atomic store: %v", err)
	}
	if err := svc.Remove(ctx, "f-3", true); err != nil {
		t.Fatalf("Remove on a non-atomic store: %v", err)
	}
	if v, err := svc.FactorSetVersion(ctx, 7); err != nil || v != 0 {
		t.Fatalf("FactorSetVersion after the changes = %d, %v; want 0, nil", v, err)
	}
}

// notFoundStore reports an absent key the way the replicated KVServicer
// does: as *KeyNotFoundError rather than (nil, nil).
type notFoundStore struct {
	*kvstore.MemoryKVStore
}

func (s *notFoundStore) Get(ctx context.Context, key string) ([]byte, error) {
	raw, err := s.MemoryKVStore.Get(ctx, key)
	if err == nil && raw == nil {
		return nil, &KeyNotFoundError{Key: key}
	}
	return raw, err
}

func TestAddConditional_keyNotFoundIsAbsence(t *testing.T) {
	svc := newFactorSetService(t, &notFoundStore{MemoryKVStore: kvstore.NewMemoryKVStore()})
	ctx := context.Background()

	assertSetVersion(t, svc, 7, 0)
	if _, err := svc.AddConditional(ctx, 7, mtlsFactor("f-1", 7, []byte("k1")), noActive); err != nil {
		t.Fatalf("AddConditional: %v", err)
	}
	_, err := svc.AddConditional(ctx, 7, mtlsFactor("f-2", 7, []byte("k2")), noActive)
	assertConditionFailed(t, err)
	assertSetVersion(t, svc, 7, 1)
}

func TestFactorSetVersion_zeroUser(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	if _, err := svc.FactorSetVersion(context.Background(), 0); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("err = %v, want ErrInvalidFactor", err)
	}
}

func TestFactorSetVersion_movesOnEveryMembershipChange(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	assertSetVersion(t, svc, 7, 0)
	if err := svc.Add(ctx, 7, mtlsFactor("f-1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	assertSetVersion(t, svc, 7, 1)
	if err := svc.(AttestationRecorder).AddAttested(ctx, 7, fido2Factor("f-2", 7, []byte("k2")), auth.Unattested()); err != nil {
		t.Fatalf("AddAttested: %v", err)
	}
	assertSetVersion(t, svc, 7, 2)
	if err := svc.Remove(ctx, "f-1", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	assertSetVersion(t, svc, 7, 3)
	if err := svc.Rotate(ctx, "f-2", fido2Factor("f-3", 0, []byte("k3"))); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	assertSetVersion(t, svc, 7, 4)
	// Relabel is not a membership change.
	if err := svc.Relabel(ctx, "f-3", "renamed"); err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	assertSetVersion(t, svc, 7, 4)
}

func TestAddConditional_rotatedFactorCounts(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	if err := svc.Add(ctx, 7, mtlsFactor("f-1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := svc.Rotate(ctx, "f-1", mtlsFactor("f-2", 0, []byte("k2"))); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	_, err := svc.AddConditional(ctx, 7, mtlsFactor("f-3", 7, []byte("k3")), noActive)
	if cfe := assertConditionFailed(t, err); cfe.ExistingFactorID != "f-2" {
		t.Fatalf("existing factor = %q, want the rotated-in f-2", cfe.ExistingFactorID)
	}
}

// writeUnrecordedFactor stores a factor the way a node that predates the
// factor-set record did: primary and user index, no record.
func writeUnrecordedFactor(t *testing.T, kv AuthFactorKV, f *auth.AuthFactor) {
	t.Helper()
	ctx := context.Background()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := kv.Put(ctx, primaryKey(f.ID), data); err != nil {
		t.Fatalf("Put primary: %v", err)
	}
	if err := kv.Put(ctx, userIndexKey(f.UserID, f.ID), []byte(f.ID)); err != nil {
		t.Fatalf("Put index: %v", err)
	}
}

func TestAddConditional_legacyFactorWithoutRecordCounts(t *testing.T) {
	kv := kvstore.NewMemoryKVStore()
	svc := newFactorSetService(t, kv)
	ctx := context.Background()

	writeUnrecordedFactor(t, kv, mtlsFactor("legacy", 7, []byte("k0")))
	assertSetVersion(t, svc, 7, 0)

	_, err := svc.AddConditional(ctx, 7, mtlsFactor("f-1", 7, []byte("k1")), noActive)
	cfe := assertConditionFailed(t, err)
	if cfe.ExistingFactorID != "legacy" || cfe.CurrentVersion != 0 {
		t.Fatalf("refusal = %+v, want the legacy factor at version 0", cfe)
	}
}

func TestAddConditional_indexEntryForeignOrMissingIgnored(t *testing.T) {
	kv := kvstore.NewMemoryKVStore()
	svc := newFactorSetService(t, kv)
	ctx := context.Background()

	// An index entry naming no primary, and one naming another user's
	// factor, are not active factors of user 7.
	if err := kv.Put(ctx, userIndexKey(7, "gone"), []byte("gone")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	foreign := mtlsFactor("foreign", 8, []byte("k8"))
	writeUnrecordedFactor(t, kv, foreign)
	if err := kv.Put(ctx, userIndexKey(7, "foreign"), []byte("foreign")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := svc.AddConditional(ctx, 7, mtlsFactor("f-1", 7, []byte("k1")), noActive); err != nil {
		t.Fatalf("AddConditional: %v", err)
	}
}

// contendedStore refuses every guarded create and swap of the factor-set
// record, as if another change always committed first.
type contendedStore struct {
	*kvstore.MemoryKVStore
}

func (s *contendedStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if strings.HasPrefix(key, userFactorSetPrefix) {
		return false, nil
	}
	return s.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func (s *contendedStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if strings.HasPrefix(key, userFactorSetPrefix) {
		return false, nil
	}
	return s.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

func TestAddConditional_contended(t *testing.T) {
	kv := &contendedStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	svc := newFactorSetService(t, kv)

	_, err := svc.AddConditional(context.Background(), 7, mtlsFactor("f-1", 7, []byte("k1")), noActive)
	if !errors.Is(err, ErrFactorSetContended) {
		t.Fatalf("err = %v, want ErrFactorSetContended", err)
	}
	contended, ok := errors.AsType[*FactorSetContendedError](err)
	if !ok || contended.UserID != 7 || contended.Attempts != factorSetCommitAttempts {
		t.Fatalf("err = %+v", err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(factorSetCommitAttempts)) {
		t.Fatalf("Error() = %q, want the attempt count", err.Error())
	}
	assertNoPrimary(t, kv, "f-1")

	// A plain Add over the same store is refused the same way: its
	// membership could not be committed.
	if err := svc.Add(context.Background(), 7, mtlsFactor("f-2", 7, []byte("k2"))); !errors.Is(err, ErrFactorSetContended) {
		t.Fatalf("Add err = %v, want ErrFactorSetContended", err)
	}
}

// faultStore fails the reads and guarded writes named by its fields.
type faultStore struct {
	*kvstore.MemoryKVStore
	getPrefix  string
	listPrefix string
	putPrefix  string
	casErr     error
}

func (s *faultStore) Put(ctx context.Context, key string, value []byte) error {
	if s.putPrefix != "" && strings.HasPrefix(key, s.putPrefix) {
		return errFaultInjected
	}
	return s.MemoryKVStore.Put(ctx, key, value)
}

var errFaultInjected = errors.New("fault injected")

func (s *faultStore) Get(ctx context.Context, key string) ([]byte, error) {
	if s.getPrefix != "" && strings.HasPrefix(key, s.getPrefix) {
		return nil, errFaultInjected
	}
	return s.MemoryKVStore.Get(ctx, key)
}

func (s *faultStore) List(ctx context.Context, prefix string) ([]string, error) {
	if s.listPrefix != "" && strings.HasPrefix(prefix, s.listPrefix) {
		return nil, errFaultInjected
	}
	return s.MemoryKVStore.List(ctx, prefix)
}

func (s *faultStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.casErr != nil && strings.HasPrefix(key, userFactorSetPrefix) {
		return false, s.casErr
	}
	if s.putPrefix != "" && strings.HasPrefix(key, s.putPrefix) {
		return false, errFaultInjected
	}
	return s.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func TestAddConditional_storeFaultsFailTheCall(t *testing.T) {
	cases := map[string]*faultStore{
		"record read":       {getPrefix: userFactorSetPrefix},
		"primary read":      {getPrefix: userFactorPrimaryPrefix},
		"user index list":   {listPrefix: userIndexPrefix(7)},
		"guarded write err": {casErr: errFaultInjected},
	}
	for name, kv := range cases {
		t.Run(name, func(t *testing.T) {
			kv.MemoryKVStore = kvstore.NewMemoryKVStore()
			ctx := context.Background()
			if kv.getPrefix == userFactorPrimaryPrefix {
				// The primary read only happens for an id the set names.
				writeUnrecordedFactor(t, kv.MemoryKVStore, mtlsFactor("legacy", 7, []byte("k0")))
			}
			svc := newFactorSetService(t, kv)
			_, err := svc.AddConditional(ctx, 7, mtlsFactor("f-1", 7, []byte("k1")), noActive)
			if !errors.Is(err, errFaultInjected) {
				t.Fatalf("err = %v, want the injected fault", err)
			}
		})
	}
}

// TestAddConditional_writeFaultsFailTheCall pins that a failed write on
// either side of the guarded commit -- the primary before it, the indexes
// after it -- fails the add rather than reporting a version.
func TestAddConditional_writeFaultsFailTheCall(t *testing.T) {
	spki := []byte("spki-1")
	cases := map[string]string{
		"primary write":      userFactorPrimaryPrefix,
		"user index write":   userIndexPrefix(7),
		"lookup index write": lookupKeyFor(auth.AuthFactorMTLS, []byte("k1")),
		"spki index write":   lookupKeyFor(auth.AuthFactorMTLS, spki),
	}
	for name, prefix := range cases {
		t.Run(name, func(t *testing.T) {
			kv := &faultStore{MemoryKVStore: kvstore.NewMemoryKVStore(), putPrefix: prefix}
			svc := newFactorSetService(t, kv)
			factor := mtlsFactor("f-1", 7, []byte("k1"))
			factor.SPKICredential = spki
			version, err := svc.AddConditional(context.Background(), 7, factor, noActive)
			if !errors.Is(err, errFaultInjected) || version != 0 {
				t.Fatalf("AddConditional = %d, %v; want 0 and the injected fault", version, err)
			}
		})
	}
}

// TestRemove_failedMembershipCommitFailsTheCall pins that a removal whose
// factor-set commit fails reports the failure rather than a success that
// left the version where it was.
func TestRemove_failedMembershipCommitFailsTheCall(t *testing.T) {
	kv := &faultStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	svc := newFactorSetService(t, kv)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("f-1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}

	kv.getPrefix = userFactorSetPrefix
	if err := svc.Remove(ctx, "f-1", true); !errors.Is(err, errFaultInjected) {
		t.Fatalf("Remove = %v, want the injected fault", err)
	}
	kv.getPrefix = ""
	if v, err := svc.FactorSetVersion(ctx, 7); err != nil || v != 1 {
		t.Fatalf("FactorSetVersion = %d, %v; want 1, the removal never committed", v, err)
	}
}

func TestFactorSetVersion_readFault(t *testing.T) {
	kv := &faultStore{MemoryKVStore: kvstore.NewMemoryKVStore(), getPrefix: userFactorSetPrefix}
	svc := newFactorSetService(t, kv)
	if _, err := svc.FactorSetVersion(context.Background(), 7); !errors.Is(err, errFaultInjected) {
		t.Fatalf("err = %v, want the injected fault", err)
	}
}

func TestAddConditional_undecodableRecordsFailClosed(t *testing.T) {
	cases := map[string]func(kv *kvstore.MemoryKVStore) string{
		"factor-set record": func(kv *kvstore.MemoryKVStore) string {
			_ = kv.Put(context.Background(), factorSetKey(7), []byte("{not json"))
			return factorSetKey(7)
		},
		"indexed primary": func(kv *kvstore.MemoryKVStore) string {
			_ = kv.Put(context.Background(), primaryKey("bad"), []byte("{not json"))
			_ = kv.Put(context.Background(), userIndexKey(7, "bad"), []byte("bad"))
			return primaryKey("bad")
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			kv := kvstore.NewMemoryKVStore()
			key := seed(kv)
			svc := newFactorSetService(t, kv)
			_, err := svc.AddConditional(context.Background(), 7, mtlsFactor("f-1", 7, []byte("k1")), noActive)
			decodeErr, ok := errors.AsType[*FactorSetDecodeError](err)
			if !ok {
				t.Fatalf("err = %v, want *FactorSetDecodeError", err)
			}
			if decodeErr.Key != key || decodeErr.Unwrap() == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("decode error = %+v", decodeErr)
			}
		})
	}
}

func TestFactorSetVersion_undecodableRecord(t *testing.T) {
	kv := kvstore.NewMemoryKVStore()
	if err := kv.Put(context.Background(), factorSetKey(7), []byte("[]")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	svc := newFactorSetService(t, kv)
	if _, err := svc.FactorSetVersion(context.Background(), 7); !errors.As(err, new(*FactorSetDecodeError)) {
		t.Fatalf("err = %v, want *FactorSetDecodeError", err)
	}
}

// raceConditionalAdds runs n conditional adds of one factor type for user 7,
// each through its OWN service instance over the shared store -- the shape of
// n nodes over one replicated store, where no in-process mutex orders them --
// and returns how many succeeded and the refusals.
func raceConditionalAdds(t *testing.T, kv AuthFactorKV, n int, cond auth.FactorAddCondition) (int, []error) {
	t.Helper()
	svcs := make([]UserAuthFactorServicer, n)
	for i := range svcs {
		svcs[i] = newFactorSetService(t, kv)
	}
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		errs  = make([]error, n)
	)
	for i := range n {
		wg.Go(func() {
			<-start
			id := "race-" + strconv.Itoa(i)
			_, errs[i] = svcs[i].AddConditional(context.Background(), 7, mtlsFactor(id, 7, []byte(id)), cond)
		})
	}
	close(start)
	wg.Wait()

	wins := 0
	var refusals []error
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		refusals = append(refusals, err)
	}
	return wins, refusals
}

func TestAddConditional_concurrentClaimsExactlyOneWins(t *testing.T) {
	const n = 32
	conds := map[string]auth.FactorAddCondition{
		"no active of type": noActive,
		"expected version":  atVersion(0),
	}
	for name, cond := range conds {
		t.Run(name, func(t *testing.T) {
			kv := kvstore.NewMemoryKVStore()
			wins, refusals := raceConditionalAdds(t, kv, n, cond)
			if wins != 1 {
				t.Fatalf("%d conditional adds succeeded, want exactly 1", wins)
			}
			for _, err := range refusals {
				if !errors.Is(err, auth.ErrFactorConditionFailed) {
					t.Fatalf("loser err = %v, want ErrFactorConditionFailed", err)
				}
			}

			svc := newFactorSetService(t, kv)
			factors, err := svc.List(context.Background(), 7)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(factors) != 1 {
				t.Fatalf("List = %d factors, want the one winner", len(factors))
			}
			// Every loser's primary was released.
			for i := range n {
				if id := "race-" + strconv.Itoa(i); id != factors[0].ID {
					assertNoPrimary(t, kv, id)
				}
			}
			assertSetVersion(t, svc, 7, 1)
		})
	}
}

func TestRoutingAddConditional(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	ctx := ctxFor("acme")

	version, err := h.svc.AddConditional(ctx, 7, mtlsFactor("f-1", 7, []byte("k1")), noActive)
	if err != nil {
		t.Fatalf("AddConditional: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
	got, err := h.svc.FactorSetVersion(ctx, 7)
	if err != nil || got != 1 {
		t.Fatalf("FactorSetVersion = %d, %v; want 1", got, err)
	}
	_, err = h.svc.AddConditional(ctx, 7, mtlsFactor("f-2", 7, []byte("k2")), noActive)
	assertConditionFailed(t, err)

	// The record lives in the tenant's keyspace, not at the shared root.
	if raw, _ := h.shared.Get(context.Background(), factorSetKey(7)); len(raw) != 0 {
		t.Fatal("factor-set record written at the shared root")
	}
}

func TestRoutingAddConditional_zeroUser(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	ctx := ctxFor("acme")

	if _, err := h.svc.AddConditional(ctx, 0, mtlsFactor("f-1", 0, []byte("k1")), noActive); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("AddConditional err = %v, want ErrInvalidFactor", err)
	}
	if _, err := h.svc.FactorSetVersion(ctx, 0); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("FactorSetVersion err = %v, want ErrInvalidFactor", err)
	}
}

// TestRoutingAddConditional_refusesAnotherTenantsUser pins that the
// conditional add and the version read route exactly as Add and List do: a
// caller not admitted to the owning tenant is refused the write and reads its
// own tenant, where the user holds nothing.
func TestRoutingAddConditional_refusesAnotherTenantsUser(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	owners, err := tenant.NewResolvedIdentityIndex(h.res)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	if _, err := owners.Record(context.Background(), tenant.IdentityUser, tenant.IdentityID(55), "acme"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := h.svc.AddConditional(ctxFor("acme"), 55, mtlsFactor("f-1", 55, []byte("k1")), noActive); err != nil {
		t.Fatalf("owning tenant AddConditional: %v", err)
	}

	_, err = h.svc.AddConditional(ctxFor("globex"), 55, mtlsFactor("f-2", 55, []byte("k2")), noActive)
	if _, ok := errors.AsType[*tenant.CrossTenantDeniedError](err); !ok {
		t.Fatalf("foreign AddConditional = %v, want *tenant.CrossTenantDeniedError", err)
	}
	if version, err := h.svc.FactorSetVersion(ctxFor("globex"), 55); err != nil || version != 0 {
		t.Fatalf("foreign FactorSetVersion = %d, %v; want 0 from its own tenant", version, err)
	}
	if version, err := h.svc.FactorSetVersion(ctxFor("acme"), 55); err != nil || version != 1 {
		t.Fatalf("owning FactorSetVersion = %d, %v; want 1, the refused add committed nothing", version, err)
	}
}

func TestAddAuthFactor_withoutConditionAddsPlainly(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	for i, key := range []string{"k1", "k2"} {
		id := "f-" + strconv.Itoa(i)
		version, err := AddAuthFactor(ctx, svc, 7, mtlsFactor(id, 7, []byte(key)), nil)
		if err != nil || version != 0 {
			t.Fatalf("AddAuthFactor(%s) = %d, %v; want 0, nil", id, version, err)
		}
	}
	// Two active mTLS factors: no condition was applied to the second.
	factors, err := svc.List(ctx, 7)
	if err != nil || len(factors) != 2 {
		t.Fatalf("List = %d factors, %v; want 2", len(factors), err)
	}
}

func TestAddAuthFactor_withConditionIsGuarded(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	version, err := AddAuthFactor(ctx, svc, 7, mtlsFactor("f-1", 7, []byte("k1")), &noActive)
	if err != nil || version != 1 {
		t.Fatalf("AddAuthFactor = %d, %v; want 1, nil", version, err)
	}
	_, err = AddAuthFactor(ctx, svc, 7, mtlsFactor("f-2", 7, []byte("k2")), &noActive)
	if cfe := assertConditionFailed(t, err); cfe.ExistingFactorID != "f-1" {
		t.Fatalf("refusal = %+v, want it to name f-1", cfe)
	}
	stale := atVersion(0)
	_, err = AddAuthFactor(ctx, svc, 7, fido2Factor("f-3", 7, []byte("k3")), &stale)
	if cfe := assertConditionFailed(t, err); cfe.ExpectedVersion != 0 || cfe.CurrentVersion != 1 {
		t.Fatalf("refusal = %+v, want expected 0 current 1", cfe)
	}
}

func TestListAuthFactors_returnsVersionWithFactors(t *testing.T) {
	svc := newFactorSetService(t, kvstore.NewMemoryKVStore())
	ctx := context.Background()

	factors, version, err := ListAuthFactors(ctx, svc, 7)
	if err != nil || len(factors) != 0 || version != 0 {
		t.Fatalf("ListAuthFactors(empty) = %d, %d, %v; want 0, 0, nil", len(factors), version, err)
	}
	if err := svc.Add(ctx, 7, mtlsFactor("f-1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	factors, version, err = ListAuthFactors(ctx, svc, 7)
	if err != nil || len(factors) != 1 || version != 1 {
		t.Fatalf("ListAuthFactors = %d, %d, %v; want 1, 1, nil", len(factors), version, err)
	}
	// The listed version is the one a guarded add expects.
	if _, err := svc.AddConditional(ctx, 7, fido2Factor("f-2", 7, []byte("k2")), atVersion(version)); err != nil {
		t.Fatalf("AddConditional at the listed version: %v", err)
	}
}

func TestListAuthFactors_nonAtomicStoreListsAtVersionZero(t *testing.T) {
	svc := newFactorSetService(t, plainFactorKV{AuthFactorKV: kvstore.NewMemoryKVStore()})
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("f-1", 7, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	factors, version, err := ListAuthFactors(ctx, svc, 7)
	if err != nil || len(factors) != 1 || version != 0 {
		t.Fatalf("ListAuthFactors = %d, %d, %v; want 1, 0, nil", len(factors), version, err)
	}
}

func TestListAuthFactors_errors(t *testing.T) {
	ctx := context.Background()
	if _, _, err := ListAuthFactors(ctx, newFactorSetService(t, kvstore.NewMemoryKVStore()), 0); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("zero user: err = %v, want ErrInvalidFactor", err)
	}
	faulty := newFactorSetService(t, &faultStore{MemoryKVStore: kvstore.NewMemoryKVStore(), getPrefix: userFactorSetPrefix})
	if _, _, err := ListAuthFactors(ctx, faulty, 7); !errors.Is(err, errFaultInjected) {
		t.Fatalf("version fault: err = %v, want the injected fault", err)
	}
	listFaulty := newFactorSetService(t, &faultStore{MemoryKVStore: kvstore.NewMemoryKVStore(), listPrefix: userIndexPrefix(7)})
	if _, _, err := ListAuthFactors(ctx, listFaulty, 7); !errors.Is(err, errFaultInjected) {
		t.Fatalf("list fault: err = %v, want the injected fault", err)
	}
}
