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
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// setWriteFailingStore is a store with multi-guarded batches whose write of a
// factor-set record fails once armed, however it is proposed: a guarded
// single-key write fails, and so does a batch carrying it, which -- being one
// all-or-nothing proposal -- then applies none of its writes.
type setWriteFailingStore struct {
	*kvstoretest.GuardedMemoryStore
	armed bool
	err   error
}

func (s *setWriteFailingStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.armed && strings.HasPrefix(key, userFactorSetPrefix) {
		return false, s.err
	}
	return s.GuardedMemoryStore.PutIfAbsent(ctx, key, value)
}

func (s *setWriteFailingStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if s.armed && strings.HasPrefix(key, userFactorSetPrefix) {
		return false, s.err
	}
	return s.GuardedMemoryStore.CompareAndSwap(ctx, key, expected, next)
}

func (s *setWriteFailingStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if s.armed {
		for _, w := range writes {
			if strings.HasPrefix(w.Key, userFactorSetPrefix) {
				return false, s.err
			}
		}
	}
	return s.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
}

// newSetWriteFailingService enrolls "keep" and "f" for user 7 and then arms
// the store, so the next factor-set write fails.
func newSetWriteFailingService(t *testing.T) (UserAuthFactorServicer, *setWriteFailingStore) {
	t.Helper()
	store := &setWriteFailingStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore(), err: errors.New("factor-set write failed")}
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: store})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	ctx := context.Background()
	for _, f := range []*auth.AuthFactor{mtlsFactor("keep", 7, []byte("k0")), mtlsFactor("f", 7, []byte("k1"))} {
		if err := svc.Add(ctx, 7, f); err != nil {
			t.Fatalf("Add %s: %v", f.ID, err)
		}
	}
	store.armed = true
	return svc, store
}

// hookedBatchStore runs before once, ahead of the first multi-guarded batch
// proposed after it is set: the write another node commits between a plan's
// reads and its batch.
type hookedBatchStore struct {
	*kvstoretest.GuardedMemoryStore
	before func(context.Context, *kvstoretest.GuardedMemoryStore)
}

func (s *hookedBatchStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if hook := s.before; hook != nil {
		s.before = nil
		hook(ctx, s.GuardedMemoryStore)
	}
	return s.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
}

func newHookedService(t *testing.T, users UserStateUpdater) (UserAuthFactorServicer, *hookedBatchStore) {
	t.Helper()
	store := &hookedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{KV: store, UserStateUpdater: users})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return svc, store
}

// ---------------------------------------------------------------------------
// Remove
// ---------------------------------------------------------------------------

func TestRemove_revokesAndBumpsTheSetTogether(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	for _, f := range []*auth.AuthFactor{mtlsFactor("keep", 7, []byte("k0")), mtlsFactor("f", 7, []byte("k1"))} {
		if err := h.svc.Add(ctx, 7, f); err != nil {
			t.Fatalf("Add %s: %v", f.ID, err)
		}
	}
	before := h.set(t, 7)

	if err := h.svc.Remove(ctx, "f", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	got, err := h.svc.Get(ctx, "f")
	if err != nil || got.RevokedAt == nil {
		t.Fatalf("Get after Remove: %+v, %v; want a revoked record", got, err)
	}
	after := h.set(t, 7)
	if after.Version != before.Version+1 {
		t.Errorf("factor-set version = %d, want %d", after.Version, before.Version+1)
	}
	if _, ok := after.Active["f"]; ok {
		t.Error("the revoked factor is still in the factor set")
	}
	if _, ok := after.Active["keep"]; !ok {
		t.Error("the sibling factor left the factor set")
	}
	if len(h.users.calls) != 0 {
		t.Fatalf("a non-last removal changed user state: %+v", h.users.calls)
	}
}

// TestRemove_failedSetWriteRevokesNothing is the split go-qrdb#681's review
// found: the revocation was one write and the factor-set version another, so
// a failed second write left the factor revoked under the version a
// conditional add had been decided against.
func TestRemove_failedSetWriteRevokesNothing(t *testing.T) {
	svc, store := newSetWriteFailingService(t)
	ctx := context.Background()
	before, err := svc.FactorSetVersion(ctx, 7)
	if err != nil {
		t.Fatalf("FactorSetVersion: %v", err)
	}

	if err := svc.Remove(ctx, "f", false); !errors.Is(err, store.err) {
		t.Fatalf("Remove = %v, want the factor-set write failure", err)
	}

	store.armed = false
	got, err := svc.Get(ctx, "f")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RevokedAt != nil {
		t.Fatal("the revocation landed without its factor-set version")
	}
	if v, err := svc.FactorSetVersion(ctx, 7); err != nil || v != before {
		t.Fatalf("FactorSetVersion = %d, %v; want %d", v, err, before)
	}
}

func TestRemove_lastFactorThroughTheBatch(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	if err := h.svc.Add(ctx, 7, mtlsFactor("only", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	var last *auth.LastFactorError
	if err := h.svc.Remove(ctx, "only", false); !errors.As(err, &last) || last.FactorID != "only" {
		t.Fatalf("Remove without force: want LastFactorError{only}, got %v", err)
	}
	if got, _ := h.svc.Get(ctx, "only"); got == nil || got.RevokedAt != nil {
		t.Fatal("a refused removal revoked the factor")
	}
	if err := h.svc.Remove(ctx, "only", true); err != nil {
		t.Fatalf("Remove force: %v", err)
	}
	if active, ok := h.users.calls[7]; !ok || active {
		t.Fatalf("expected SetUserActive(7, false), got calls=%+v", h.users.calls)
	}
	if len(h.set(t, 7).Active) != 0 {
		t.Fatal("factor set still lists a factor")
	}
}

func TestRemove_contendedFactorSetGivesUp(t *testing.T) {
	store := &scriptedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	svc := newScriptedDeleteService(t, store)
	err := svc.Remove(context.Background(), "f", true)
	var contended *FactorSetContendedError
	if !errors.As(err, &contended) || contended.UserID != 7 {
		t.Fatalf("want FactorSetContendedError{UserID: 7}, got %v", err)
	}
	if store.calls != factorSetCommitAttempts {
		t.Fatalf("attempts = %d, want %d", store.calls, factorSetCommitAttempts)
	}
}

func TestRemove_propagatesStoreError(t *testing.T) {
	boom := errors.New("proposal outcome unknown")
	store := &scriptedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore(), err: boom}
	svc := newScriptedDeleteService(t, store)
	if err := svc.Remove(context.Background(), "f", true); !errors.Is(err, boom) {
		t.Fatalf("want the store error, got %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("an unknown outcome was retried: %d calls", store.calls)
	}
}

// TestRemove_concurrentDeleteIsNotFound covers a hard Delete committed by
// another node between the removal's reads and its batch: the guard on the
// primary record refuses the batch, and the re-read finds nothing to revoke
// rather than writing the revoked record back.
func TestRemove_concurrentDeleteIsNotFound(t *testing.T) {
	svc, store := newHookedService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("f", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store.before = func(ctx context.Context, kv *kvstoretest.GuardedMemoryStore) {
		_ = kv.Delete(ctx, primaryKey("f"))
	}
	var nf *auth.FactorNotFoundError
	if err := svc.Remove(ctx, "f", true); !errors.As(err, &nf) || nf.FactorID != "f" {
		t.Fatalf("Remove = %v, want FactorNotFoundError{f}", err)
	}
	if v, _ := store.Get(ctx, primaryKey("f")); v != nil {
		t.Fatalf("the removal resurrected the deleted record: %q", v)
	}
}

// ---------------------------------------------------------------------------
// Rotate
// ---------------------------------------------------------------------------

func TestRotate_commitsTheWholeRotationTogether(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	if err := h.svc.Add(ctx, 7, spkiFactor("old", 7, "cert-old", "spki")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	before := h.set(t, 7)

	if err := h.svc.Rotate(ctx, "old", spkiFactor("new", 7, "cert-new", "spki")); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	old, err := h.svc.Get(ctx, "old")
	if err != nil || old.RevokedAt == nil {
		t.Fatalf("old factor: %+v, %v; want revoked", old, err)
	}
	after := h.set(t, 7)
	if after.Version != before.Version+1 {
		t.Errorf("factor-set version = %d, want %d", after.Version, before.Version+1)
	}
	if _, ok := after.Active["old"]; ok {
		t.Error("the rotated-out factor is still in the factor set")
	}
	if typ, ok := after.Active["new"]; !ok || typ != auth.AuthFactorMTLS {
		t.Errorf("the rotated-in factor is not in the factor set: %+v", after.Active)
	}
	for name, key := range map[string]string{
		"user index": userIndexKey(7, "new"),
		"credential": lookupKeyFor(auth.AuthFactorMTLS, []byte("cert-new")),
		"spki":       lookupKeyFor(auth.AuthFactorMTLS, []byte("spki")),
	} {
		if got := string(h.value(t, key)); got != "new" {
			t.Errorf("%s entry = %q, want new", name, got)
		}
	}
}

// TestRotate_failedSetWriteChangesNothing is Remove's split in Rotate: the new
// factor, its indexes and the revocation were written one at a time before
// the factor-set version, so a failed version write left the rotation applied
// under the old version.
func TestRotate_failedSetWriteChangesNothing(t *testing.T) {
	svc, store := newSetWriteFailingService(t)
	ctx := context.Background()
	before, err := svc.FactorSetVersion(ctx, 7)
	if err != nil {
		t.Fatalf("FactorSetVersion: %v", err)
	}

	if err := svc.Rotate(ctx, "f", mtlsFactor("g", 7, []byte("k2"))); !errors.Is(err, store.err) {
		t.Fatalf("Rotate = %v, want the factor-set write failure", err)
	}

	store.armed = false
	old, err := svc.Get(ctx, "f")
	if err != nil {
		t.Fatalf("Get old: %v", err)
	}
	if old.RevokedAt != nil {
		t.Fatal("the old factor was revoked without the factor-set version")
	}
	for name, key := range map[string]string{
		"primary":    primaryKey("g"),
		"user index": userIndexKey(7, "g"),
		"credential": lookupKeyFor(auth.AuthFactorMTLS, []byte("k2")),
	} {
		if v, _ := store.Get(ctx, key); v != nil {
			t.Errorf("the new factor's %s was written without the factor-set version: %q", name, v)
		}
	}
	if v, err := svc.FactorSetVersion(ctx, 7); err != nil || v != before {
		t.Fatalf("FactorSetVersion = %d, %v; want %d", v, err, before)
	}
}

func TestRotate_contendedFactorSetGivesUp(t *testing.T) {
	store := &scriptedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	svc := newScriptedDeleteService(t, store)
	err := svc.Rotate(context.Background(), "f", mtlsFactor("g", 7, []byte("k2")))
	var contended *FactorSetContendedError
	if !errors.As(err, &contended) || contended.UserID != 7 {
		t.Fatalf("want FactorSetContendedError{UserID: 7}, got %v", err)
	}
	if store.calls != factorSetCommitAttempts {
		t.Fatalf("attempts = %d, want %d", store.calls, factorSetCommitAttempts)
	}
}

func TestRotate_propagatesStoreError(t *testing.T) {
	boom := errors.New("proposal outcome unknown")
	store := &scriptedBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore(), err: boom}
	svc := newScriptedDeleteService(t, store)
	if err := svc.Rotate(context.Background(), "f", mtlsFactor("g", 7, []byte("k2"))); !errors.Is(err, boom) {
		t.Fatalf("want the store error, got %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("an unknown outcome was retried: %d calls", store.calls)
	}
}

// TestRotate_concurrentDeleteIsNotFound is the resurrection a split rotation
// allowed: a hard Delete of the old factor landing before the revocation
// write, which then wrote the deleted record back as revoked.
func TestRotate_concurrentDeleteIsNotFound(t *testing.T) {
	svc, store := newHookedService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("old", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store.before = func(ctx context.Context, kv *kvstoretest.GuardedMemoryStore) {
		_ = kv.Delete(ctx, primaryKey("old"))
	}
	var nf *auth.FactorNotFoundError
	if err := svc.Rotate(ctx, "old", mtlsFactor("new", 7, []byte("k2"))); !errors.As(err, &nf) || nf.FactorID != "old" {
		t.Fatalf("Rotate = %v, want FactorNotFoundError{old}", err)
	}
	for _, key := range []string{primaryKey("old"), primaryKey("new"), lookupKeyFor(auth.AuthFactorMTLS, []byte("k2"))} {
		if v, _ := store.Get(ctx, key); v != nil {
			t.Errorf("%s written by a refused rotation: %q", key, v)
		}
	}
}

// TestRotate_credentialClaimedMidRotation covers an enrollment of the new
// credential committed between the rotation's reads and its batch: the guard
// on the index entry refuses the batch and the re-read names the winner.
func TestRotate_credentialClaimedMidRotation(t *testing.T) {
	svc, store := newHookedService(t, nil)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("old", 7, []byte("k"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store.before = func(ctx context.Context, kv *kvstoretest.GuardedMemoryStore) {
		_ = kv.Put(ctx, lookupKeyFor(auth.AuthFactorMTLS, []byte("k2")), []byte("intruder"))
	}
	var exists *auth.FactorAlreadyExistsError
	if err := svc.Rotate(ctx, "old", mtlsFactor("new", 7, []byte("k2"))); !errors.As(err, &exists) || exists.FactorID != "intruder" {
		t.Fatalf("Rotate = %v, want FactorAlreadyExistsError{intruder}", err)
	}
	if old, err := svc.Get(ctx, "old"); err != nil || old.RevokedAt != nil {
		t.Fatalf("old factor after a refused rotation: %+v, %v", old, err)
	}
}

// TestRotate_refusesAnIDInUse pins that a caller-chosen id naming an
// existing factor is refused rather than overwriting that factor's record.
func TestRotate_refusesAnIDInUse(t *testing.T) {
	h := newDeleteHarness(t)
	ctx := context.Background()
	for _, f := range []*auth.AuthFactor{mtlsFactor("other", 7, []byte("k0")), mtlsFactor("old", 7, []byte("k1"))} {
		if err := h.svc.Add(ctx, 7, f); err != nil {
			t.Fatalf("Add %s: %v", f.ID, err)
		}
	}
	var exists *auth.FactorAlreadyExistsError
	if err := h.svc.Rotate(ctx, "old", mtlsFactor("other", 7, []byte("k2"))); !errors.As(err, &exists) || exists.FactorID != "other" {
		t.Fatalf("Rotate = %v, want FactorAlreadyExistsError{other}", err)
	}
	got, err := h.svc.Get(ctx, "other")
	if err != nil || string(got.Credential) != "k0" {
		t.Fatalf("the existing factor was overwritten: %+v, %v", got, err)
	}
}

// countingBatchStore counts the multi-guarded batches proposed through it.
type countingBatchStore struct {
	*kvstoretest.GuardedMemoryStore
	batches int
}

func (s *countingBatchStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	s.batches++
	return s.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
}

// TestRoutedRemoveAndRotate_commitThroughTheBatch drives Remove and Rotate
// through the routing service over a prefix tenant: its namespaced store must
// keep the multi-guarded batch, so each commits as one batch there too.
func TestRoutedRemoveAndRotate_commitThroughTheBatch(t *testing.T) {
	shared := &countingBatchStore{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	tenants, err := tenant.NewKVTenantService(shared, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	if _, err := tenants.CreateTenant(context.Background(), "acme", "acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	res, err := tenant.NewStoreResolver(shared, nil, tenants)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{Resolver: res})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}
	ctx := ctxFor("acme")
	for _, f := range []*auth.AuthFactor{mtlsFactor("keep", 1, []byte("k0")), mtlsFactor("old", 1, []byte("k1"))} {
		if err := svc.Add(ctx, 1, f); err != nil {
			t.Fatalf("Add %s: %v", f.ID, err)
		}
	}
	version := func() uint64 {
		t.Helper()
		v, err := svc.FactorSetVersion(ctx, 1)
		if err != nil {
			t.Fatalf("FactorSetVersion: %v", err)
		}
		return v
	}

	before, batches := version(), shared.batches
	if err := svc.Rotate(ctx, "old", mtlsFactor("new", 1, []byte("k2"))); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if shared.batches != batches+1 || version() != before+1 {
		t.Fatalf("Rotate: %d batches, version %d; want 1 batch and version %d", shared.batches-batches, version(), before+1)
	}

	before, batches = version(), shared.batches
	if err := svc.Remove(ctx, "new", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if shared.batches != batches+1 || version() != before+1 {
		t.Fatalf("Remove: %d batches, version %d; want 1 batch and version %d", shared.batches-batches, version(), before+1)
	}
}
