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
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// go-qrdb#484: CreateTenant's tombstone check was a read; the commit was a
// plain put. A delete landing between the two left a live record for a
// permanently retired id. These tests drive that interleaving
// DETERMINISTICALLY: the store double lets every read through unchanged and,
// at the moment of the commit, first applies what the concurrent operation
// would have written -- through the real inner store -- then delegates the
// commit itself to the real inner store. Nothing about the commit's outcome is
// decided by the double.
// ---------------------------------------------------------------------------

// interleaveOnCommit wraps a real seal backend and runs `before` immediately
// ahead of the fenced create -- the commit CreateTenant makes last.
type interleaveOnCommit struct {
	*seal.MemoryBackend
	before func(ctx context.Context, store *seal.MemoryBackend)
	once   sync.Once
}

func (s *interleaveOnCommit) PutIfAllAbsent(ctx context.Context, key string, value []byte, fences ...string) (bool, error) {
	s.once.Do(func() { s.before(ctx, s.MemoryBackend) })
	return s.MemoryBackend.PutIfAllAbsent(ctx, key, value, fences...)
}

// retire writes the tombstone a concurrent DeleteTenant writes at its fence.
func retire(t *testing.T, id string) func(context.Context, *seal.MemoryBackend) {
	return func(ctx context.Context, store *seal.MemoryBackend) {
		ts, err := json.Marshal(&Tombstone{ID: id, DeletedAt: time.Now().UTC()})
		if err != nil {
			t.Fatalf("marshal tombstone: %v", err)
		}
		if err := store.Put(ctx, tombstoneKey(id), ts); err != nil {
			t.Fatalf("write tombstone: %v", err)
		}
	}
}

// assertNoResurrection states the invariant #484 is about: a retired id never
// has a live metadata record beside its tombstone.
func assertNoResurrection(t *testing.T, get func(context.Context, string) ([]byte, error), id string) {
	t.Helper()
	ctx := context.Background()
	ts, err := get(ctx, tombstoneKey(id))
	if err != nil && !errors.Is(err, seal.ErrSecretNotFound) {
		t.Fatalf("read tombstone: %v", err)
	}
	meta, err := get(ctx, metadataPrefix+id)
	if err != nil && !errors.Is(err, seal.ErrSecretNotFound) {
		t.Fatalf("read metadata: %v", err)
	}
	if ts != nil && meta != nil {
		t.Fatalf("id %q has BOTH a tombstone and a live metadata record: a retired id was resurrected", id)
	}
}

// TestCreateTenant_RefusedWhenADeleteRetiresTheIdMidCreate is the issue's
// interleaving on the seal-enabled service: the create passes its tombstone
// read, the id is retired, and the create then commits. The commit must be
// refused with the retirement, and no record may land.
func TestCreateTenant_RefusedWhenADeleteRetiresTheIdMidCreate(t *testing.T) {
	store := &interleaveOnCommit{MemoryBackend: seal.NewMemoryBackend(), before: retire(t, "acme")}
	svc, _ := newTestServiceWithStore(t, store)

	_, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix)
	var deleted *TenantDeletedError
	if !errors.As(err, &deleted) {
		t.Fatalf("CreateTenant = %v; want *TenantDeletedError", err)
	}
	assertNoResurrection(t, store.Get, "acme")
	if _, err := svc.GetTenant(context.Background(), "acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("GetTenant after the refused create = %v; want ErrTenantNotFound", err)
	}
}

// TestCreateTenant_RefusedWhenAnotherCreateWinsTheIdMidCreate is the older
// half: two creates of one id, the second committing after the first. The
// loser is refused and the winner's record is untouched.
func TestCreateTenant_RefusedWhenAnotherCreateWinsTheIdMidCreate(t *testing.T) {
	winner := []byte(`{"id":"acme","name":"Winner","isolation_type":"prefix"}`)
	store := &interleaveOnCommit{
		MemoryBackend: seal.NewMemoryBackend(),
		before: func(ctx context.Context, s *seal.MemoryBackend) {
			if err := s.Put(ctx, metadataPrefix+"acme", winner); err != nil {
				t.Fatalf("seed winner: %v", err)
			}
		},
	}
	svc, _ := newTestServiceWithStore(t, store)

	_, err := svc.CreateTenant(context.Background(), "acme", "Loser", IsolationPrefix)
	if !errors.Is(err, ErrTenantAlreadyExists) {
		t.Fatalf("CreateTenant = %v; want ErrTenantAlreadyExists", err)
	}
	got, err := svc.GetTenant(context.Background(), "acme")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Name != "Winner" {
		t.Fatalf("the loser overwrote the winner's record: name = %q", got.Name)
	}
}

// TestCreateTenant_LosingToADeleteLeavesNoBarrierAfterRestart is the restart
// assertion: a volume tenant provisions a barrier and a durable tenant-root
// record BEFORE its commit. When the commit loses to a retirement, that
// residue must be destroyed, not merely forgotten -- the boot path restores a
// barrier from the record, so an unregister-only rollback brings the retired
// id's barrier back on every restart.
func TestCreateTenant_LosingToADeleteLeavesNoBarrierAfterRestart(t *testing.T) {
	ctx := context.Background()
	store := &interleaveOnCommit{MemoryBackend: seal.NewMemoryBackend(), before: retire(t, "vacme")}
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(volumeEntitlements()), WithVolumeAllocator(volumeAllocatorFor(t, reg)))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}

	_, err = svc.CreateTenant(ctx, "vacme", "Volume Acme", IsolationVolume)
	var deleted *TenantDeletedError
	if !errors.As(err, &deleted) {
		t.Fatalf("CreateTenant = %v; want *TenantDeletedError", err)
	}
	assertNoResurrection(t, store.Get, "vacme")
	if _, err := reg.GetTenant("vacme"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("barrier still registered after the refused create: %v", err)
	}

	// The restart: re-attach every barrier the durable records describe.
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	if _, err := reg.GetTenant("vacme"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("a restart restored the retired id's barrier (GetTenant = %v): the rollback left its durable record behind", err)
	}
}

// TestCreateTenant_ConcurrentCreatesHaveOneWinner races real goroutines at one
// id through the real in-memory store: exactly one create succeeds and every
// other one is refused with ErrTenantAlreadyExists.
func TestCreateTenant_ConcurrentCreatesHaveOneWinner(t *testing.T) {
	svc, _ := newTestService(t)
	const n = 16
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix)
			switch {
			case err == nil:
				mu.Lock()
				wins++
				mu.Unlock()
			case !errors.Is(err, ErrTenantAlreadyExists):
				t.Errorf("CreateTenant = %v; want nil or ErrTenantAlreadyExists", err)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent creates of one id succeeded; want exactly 1", wins)
	}
}

// TestCreateTenant_ConcurrentCreateAndDeleteNeverResurrect races a delete of a
// live tenant against a create of the same id, many times, and asserts the
// invariant at every quiescent point: never a tombstone beside a live record.
// Exactly one of the two outcomes the issue names must hold -- the id is
// retired, and the create was refused.
func TestCreateTenant_ConcurrentCreateAndDeleteNeverResurrect(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		store := seal.NewMemoryBackend()
		svc, _ := newTestServiceWithStore(t, store)
		if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("seed CreateTenant: %v", err)
		}
		var (
			wg        sync.WaitGroup
			createErr error
			deleteErr error
		)
		wg.Add(2)
		go func() { defer wg.Done(); deleteErr = svc.DeleteTenant(ctx, "acme") }()
		go func() { defer wg.Done(); _, createErr = svc.CreateTenant(ctx, "acme", "Acme again", IsolationPrefix) }()
		wg.Wait()

		if deleteErr != nil {
			t.Fatalf("iteration %d: DeleteTenant: %v", i, deleteErr)
		}
		var deleted *TenantDeletedError
		if createErr == nil || (!errors.Is(createErr, ErrTenantAlreadyExists) && !errors.As(createErr, &deleted)) {
			t.Fatalf("iteration %d: CreateTenant = %v; want a refusal", i, createErr)
		}
		assertNoResurrection(t, store.Get, "acme")
	}
}

// ---------------------------------------------------------------------------
// The seal-disabled service carries the same guarantee.
// ---------------------------------------------------------------------------

// kvInterleaveOnCommit is interleaveOnCommit for the kvstore.KVStore the
// seal-disabled tenant service runs on.
type kvInterleaveOnCommit struct {
	*kvstore.MemoryKVStore
	before func(ctx context.Context, store *kvstore.MemoryKVStore)
	once   sync.Once
}

func (s *kvInterleaveOnCommit) PutIfAllAbsent(ctx context.Context, key string, value []byte, fences ...string) (bool, error) {
	s.once.Do(func() { s.before(ctx, s.MemoryKVStore) })
	return s.MemoryKVStore.PutIfAllAbsent(ctx, key, value, fences...)
}

func TestKVCreateTenant_RefusedWhenADeleteRetiresTheIdMidCreate(t *testing.T) {
	store := &kvInterleaveOnCommit{
		MemoryKVStore: kvstore.NewMemoryKVStore(),
		before: func(ctx context.Context, s *kvstore.MemoryKVStore) {
			if err := s.Put(ctx, tombstoneKey("acme"), []byte(`{"id":"acme"}`)); err != nil {
				t.Fatalf("write tombstone: %v", err)
			}
		},
	}
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	_, err = svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix)
	var deleted *TenantDeletedError
	if !errors.As(err, &deleted) {
		t.Fatalf("CreateTenant = %v; want *TenantDeletedError", err)
	}
	meta, err := store.Get(context.Background(), metadataPrefix+"acme")
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	if meta != nil {
		t.Fatal("a live metadata record landed beside the tombstone")
	}
}

// ---------------------------------------------------------------------------
// A store without the capability is refused before anything is provisioned.
// ---------------------------------------------------------------------------

// unfencedBackend is a real backend with the fenced create hidden: every
// method it exposes is the inner store's own.
type unfencedBackend struct{ seal.StorageBackend }

func TestCreateTenant_RefusesAStoreThatCannotCommitAtomically(t *testing.T) {
	inner := seal.NewMemoryBackend()
	svc, reg := newTestServiceWithStore(t, unfencedBackend{inner})

	_, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationBarrier)
	if !errors.Is(err, ErrTenantStoreNotAtomic) {
		t.Fatalf("CreateTenant = %v; want ErrTenantStoreNotAtomic", err)
	}
	if _, gerr := reg.GetTenant("acme"); !errors.Is(gerr, seal.ErrTenantNotFound) {
		t.Fatalf("a barrier was provisioned before the refusal: %v", gerr)
	}
	keys, lerr := inner.List(context.Background(), "")
	if lerr != nil {
		t.Fatalf("List: %v", lerr)
	}
	if len(keys) != 0 {
		t.Fatalf("the refused create wrote %v", keys)
	}
}

// unfencedKV hides the fenced create on the kvstore side.
type unfencedKV struct{ kvstore.KVStore }

func TestKVCreateTenant_RefusesAStoreThatCannotCommitAtomically(t *testing.T) {
	svc, err := NewKVTenantService(unfencedKV{kvstore.NewMemoryKVStore()}, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix); !errors.Is(err, ErrTenantStoreNotAtomic) {
		t.Fatalf("CreateTenant = %v; want ErrTenantStoreNotAtomic", err)
	}
}

// TestCommitCreate_PropagatesAnUnknownOutcome pins that a store error on the
// commit is reported as a StoreError -- an unknown outcome -- rather than
// read as either refusal.
func TestCommitCreate_PropagatesAnUnknownOutcome(t *testing.T) {
	boom := errors.New("proposal dropped")
	err := commitCreate(context.Background(), erroringFenced{err: boom}, seal.NewMemoryBackend().Get, "acme", []byte("{}"), nil)
	var se *StoreError
	if !errors.As(err, &se) || !errors.Is(err, boom) || se.Op != "put" {
		t.Fatalf("commitCreate = %v; want *StoreError{Op: put} wrapping the store error", err)
	}
}

type erroringFenced struct{ err error }

func (e erroringFenced) PutIfAllAbsent(context.Context, string, []byte, ...string) (bool, error) {
	return false, e.err
}
