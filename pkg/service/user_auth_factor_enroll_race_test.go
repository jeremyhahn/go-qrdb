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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// interleavingStore runs hook once, right after the first successful write of
// trigger, so a second node's call lands at that exact point of the first
// node's add. The second node uses the bare store and never fires the hook.
type interleavingStore struct {
	*kvstoretest.GuardedMemoryStore
	trigger string
	hook    func()
	fired   bool
}

func (s *interleavingStore) wrote(key string) {
	if key == s.trigger && !s.fired {
		s.fired = true
		s.hook()
	}
}

func (s *interleavingStore) Put(ctx context.Context, key string, value []byte) error {
	if err := s.GuardedMemoryStore.Put(ctx, key, value); err != nil {
		return err
	}
	s.wrote(key)
	return nil
}

func (s *interleavingStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	stored, err := s.GuardedMemoryStore.PutIfAbsent(ctx, key, value)
	if stored {
		s.wrote(key)
	}
	return stored, err
}

func (s *interleavingStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	swapped, err := s.GuardedMemoryStore.CompareAndSwap(ctx, key, expected, next)
	if swapped {
		s.wrote(key)
	}
	return swapped, err
}

func (s *interleavingStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	applied, err := s.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
	if applied {
		for _, w := range writes {
			s.wrote(w.Key)
		}
	}
	return applied, err
}

// raceHarness is two nodes' factor services over one store: enrolling adds
// through the interleaving store, other acts from inside its hook.
type raceHarness struct {
	kv        *kvstoretest.GuardedMemoryStore
	store     *interleavingStore
	enrolling UserAuthFactorServicer
	other     UserAuthFactorServicer
}

func newRaceHarness(t *testing.T, trigger string) *raceHarness {
	t.Helper()
	h := &raceHarness{kv: kvstoretest.NewGuardedMemoryStore()}
	h.store = &interleavingStore{GuardedMemoryStore: h.kv, trigger: trigger}
	var err error
	if h.enrolling, err = NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: h.store}); err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	if h.other, err = NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: h.kv}); err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return h
}

// requireGone fails unless f has no primary record, no index entry and no
// factor-set membership left, and its credential can be enrolled again.
func (h *raceHarness) requireGone(t *testing.T, f *auth.AuthFactor) {
	t.Helper()
	ctx := context.Background()
	for _, key := range append(factorIndexKeys(f), primaryKey(f.ID)) {
		if v, err := h.kv.Get(ctx, key); err != nil || v != nil {
			t.Errorf("%s = %q, %v after the delete; want it gone", key, v, err)
		}
	}
	var set factorSet
	raw, err := h.kv.Get(ctx, factorSetKey(f.UserID))
	if err != nil {
		t.Fatalf("Get factor set: %v", err)
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &set); err != nil {
			t.Fatalf("decode factor set: %v", err)
		}
	}
	if _, ok := set.Active[f.ID]; ok {
		t.Errorf("deleted factor %s is still in the factor set", f.ID)
	}
	if err := h.other.Add(ctx, f.UserID, mtlsFactor("rebound", f.UserID, f.Credential)); err != nil {
		t.Errorf("credential still reserved after the delete: %v", err)
	}
}

// TestAddDeleteRace_deleteAfterFactorSetCommit lands a hard Delete right after
// the add's factor-set entry commits. The add's index entries must not be
// written after it: they would name a deleted factor and keep its credential
// reserved.
func TestAddDeleteRace_deleteAfterFactorSetCommit(t *testing.T) {
	h := newRaceHarness(t, factorSetKey(7))
	ctx := context.Background()
	f := mtlsFactor("f", 7, []byte("cred"))
	var deleteErr error
	h.store.hook = func() { deleteErr = h.other.Delete(ctx, "f", true) }

	if err := h.enrolling.Add(ctx, 7, f); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !h.store.fired {
		t.Fatal("the add never committed the factor set")
	}
	if deleteErr != nil {
		t.Fatalf("Delete: %v", deleteErr)
	}
	h.requireGone(t, f)
}

// TestAddDeleteRace_deleteBeforeFactorSetCommit lands a hard Delete between
// the add's primary record and its factor-set commit. The add must be refused
// rather than enrol a factor whose record is gone.
func TestAddDeleteRace_deleteBeforeFactorSetCommit(t *testing.T) {
	h := newRaceHarness(t, primaryKey("f"))
	ctx := context.Background()
	f := mtlsFactor("f", 7, []byte("cred"))
	var deleteErr error
	h.store.hook = func() { deleteErr = h.other.Delete(ctx, "f", true) }

	var nf *auth.FactorNotFoundError
	if err := h.enrolling.Add(ctx, 7, f); !errors.As(err, &nf) || nf.FactorID != "f" {
		t.Fatalf("Add: want FactorNotFoundError{f}, got %v", err)
	}
	if deleteErr != nil {
		t.Fatalf("Delete: %v", deleteErr)
	}
	h.requireGone(t, f)
}

// TestAddRace_credentialClaimedBeforeCommit enrols the same credential on
// another node between the add's duplicate check and its commit. The add must
// be refused and leave the other node's index entry alone.
func TestAddRace_credentialClaimedBeforeCommit(t *testing.T) {
	h := newRaceHarness(t, primaryKey("f"))
	ctx := context.Background()
	var otherErr error
	h.store.hook = func() { otherErr = h.other.Add(ctx, 8, mtlsFactor("g", 8, []byte("cred"))) }

	var exists *auth.FactorAlreadyExistsError
	if err := h.enrolling.Add(ctx, 7, mtlsFactor("f", 7, []byte("cred"))); !errors.As(err, &exists) || exists.FactorID != "g" {
		t.Fatalf("Add: want FactorAlreadyExistsError{g}, got %v", err)
	}
	if otherErr != nil {
		t.Fatalf("other Add: %v", otherErr)
	}
	if v, _ := h.kv.Get(ctx, primaryKey("f")); v != nil {
		t.Error("the refused add's primary record was not released")
	}
	got, err := h.other.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("cred"))
	if err != nil || got.ID != "g" {
		t.Fatalf("lookup = %v, %v; want the other node's factor g", got, err)
	}
}

func TestAdd_contendedEnrollmentGivesUp(t *testing.T) {
	store := &scriptedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore(), armed: true}
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: store})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	var contended *FactorSetContendedError
	if err := svc.Add(context.Background(), 7, mtlsFactor("f", 7, []byte("k"))); !errors.As(err, &contended) || contended.UserID != 7 {
		t.Fatalf("want FactorSetContendedError{UserID: 7}, got %v", err)
	}
	if store.calls != factorSetCommitAttempts {
		t.Fatalf("attempts = %d, want %d", store.calls, factorSetCommitAttempts)
	}
	if v, _ := store.Get(context.Background(), primaryKey("f")); v != nil {
		t.Fatal("a refused add left its primary record")
	}
}

func TestAdd_enrollmentStoreErrorKeepsPrimary(t *testing.T) {
	boom := errors.New("proposal outcome unknown")
	store := &scriptedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore(), armed: true, err: boom}
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: store})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	if err := svc.Add(context.Background(), 7, mtlsFactor("f", 7, []byte("k"))); !errors.Is(err, boom) {
		t.Fatalf("want the store error, got %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("an unknown outcome was retried: %d calls", store.calls)
	}
	// The batch may have applied, so the record it guards on stays.
	if v, _ := store.Get(context.Background(), primaryKey("f")); v == nil {
		t.Fatal("an add with an unknown outcome released its primary record")
	}
}
