// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tenant

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// recordRace commits write once, just before the first write it sees to key:
// the commit that lands between an update's read of the record and its own
// write of it.
type recordRace struct {
	key   string
	write func()
}

func (r *recordRace) before(key string) {
	if key != r.key || r.write == nil {
		return
	}
	w := r.write
	r.write = nil
	w()
}

// racingSealStore is a seal.MemoryBackend whose tenant-record writes -- a
// blind Put or a compare-and-swap alike -- first let its race commit.
type racingSealStore struct {
	*seal.MemoryBackend
	race recordRace
}

func (s *racingSealStore) Put(ctx context.Context, key string, data []byte) error {
	s.race.before(key)
	return s.MemoryBackend.Put(ctx, key, data)
}

func (s *racingSealStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	s.race.before(key)
	return s.MemoryBackend.CompareAndSwap(ctx, key, expected, next)
}

// racingKVStore is racingSealStore for the no-seal service's KV store.
type racingKVStore struct {
	*memKVStore
	race recordRace
}

func (s *racingKVStore) Put(ctx context.Context, key string, value []byte) error {
	s.race.before(key)
	return s.memKVStore.Put(ctx, key, value)
}

func (s *racingKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	s.race.before(key)
	return s.memKVStore.CompareAndSwap(ctx, key, expected, next)
}

type racedService struct {
	svc     TenantServicer
	race    *recordRace
	auditor *fakeStatusAuditor
}

// racedServices returns both shipped tenant services, each holding tenant
// "acme" (active), wired to its own fake auditor, over a store whose race
// is armed by setting race.write.
func racedServices(t *testing.T) map[string]racedService {
	t.Helper()
	sealStore := &racingSealStore{MemoryBackend: seal.NewMemoryBackend()}
	barrierSvc, _ := newTestServiceWithStore(t, sealStore)
	kvStore := &racingKVStore{memKVStore: newMemKVStore()}
	kvSvc, err := NewKVTenantService(kvStore, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	out := map[string]racedService{}
	for name, c := range map[string]struct {
		svc  TenantServicer
		race *recordRace
	}{"barrier": {barrierSvc, &sealStore.race}, "kv": {kvSvc, &kvStore.race}} {
		if _, err := c.svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("%s CreateTenant: %v", name, err)
		}
		a := &fakeStatusAuditor{}
		c.svc.(StatusAuditWiring).SetStatusChangeAuditor(a)
		c.race.key = metadataPrefix + "acme"
		out[name] = racedService{svc: c.svc, race: c.race, auditor: a}
	}
	return out
}

// commitStatus arms c's race to commit a status change through the service
// itself, as a concurrent operator would.
func (c racedService) commitStatus(t *testing.T, s Status) {
	c.race.write = func() {
		if _, err := c.svc.UpdateTenant(context.Background(), "acme", statusPatch(s)); err != nil {
			t.Errorf("racing UpdateTenant(%s): %v", s, err)
		}
	}
}

// A descriptive patch that read the record before a suspension committed
// does not write the stale active status back over it: it commits against the
// record it read, loses, re-reads, and lands its rename on the suspended
// record.
func TestUpdateTenant_DescriptivePatchKeepsAConcurrentSuspension(t *testing.T) {
	for name, c := range racedServices(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c.commitStatus(t, StatusSuspended)

			got, err := c.svc.UpdateTenant(ctx, "acme", TenantPatch{Name: "Acme Corp"})
			if err != nil {
				t.Fatalf("UpdateTenant: %v", err)
			}
			if got.Status != StatusSuspended || got.Name != "Acme Corp" {
				t.Fatalf("UpdateTenant = {%s %q}, want {suspended \"Acme Corp\"}", got.Status, got.Name)
			}
			stored, err := c.svc.GetTenant(ctx, "acme")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if stored.Status != StatusSuspended || stored.Name != "Acme Corp" {
				t.Fatalf("stored = {%s %q}, want {suspended \"Acme Corp\"}", stored.Status, stored.Name)
			}
			want := statusChange{tenantID: "acme", from: StatusActive, to: StatusSuspended, settled: 1}
			if len(c.auditor.changes) != 1 || *c.auditor.changes[0] != want {
				t.Fatalf("audited %v, want only the suspension", c.auditor.changes)
			}
		})
	}
}

// A status change is validated against the record it commits over: a suspend
// that read an active tenant, then lost its swap to a disable, re-reads the
// disabled record and is refused (disabled -> suspended is no transition),
// and the attempt it had begun auditing is settled as not committed.
func TestUpdateTenant_StatusTransitionRevalidatedAgainstCommittedRecord(t *testing.T) {
	for name, c := range racedServices(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			c.commitStatus(t, StatusDisabled)

			_, err := c.svc.UpdateTenant(ctx, "acme", statusPatch(StatusSuspended))
			var te *StatusTransitionError
			if !errors.As(err, &te) || te.From != StatusDisabled || te.To != StatusSuspended {
				t.Fatalf("UpdateTenant = %v, want StatusTransitionError{disabled -> suspended}", err)
			}
			stored, err := c.svc.GetTenant(ctx, "acme")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if stored.Status != StatusDisabled {
				t.Fatalf("stored status = %s, want disabled", stored.Status)
			}
			if len(c.auditor.changes) != 2 {
				t.Fatalf("audited %d changes, want the lost suspend and the disable", len(c.auditor.changes))
			}
			lost, disable := c.auditor.changes[0], c.auditor.changes[1]
			var conflict *ConcurrentUpdateError
			if lost.from != StatusActive || lost.to != StatusSuspended || lost.settled != 1 || !errors.As(lost.err, &conflict) {
				t.Fatalf("lost suspend audited %+v, want active -> suspended settled once with a *ConcurrentUpdateError", *lost)
			}
			if disable.from != StatusActive || disable.to != StatusDisabled || disable.settled != 1 || disable.err != nil {
				t.Fatalf("disable audited %+v, want active -> disabled settled once, committed", *disable)
			}
		})
	}
}

// losingSealStore and losingKVStore lose every compare-and-swap, as a record
// rewritten between every read and every swap would.
type losingSealStore struct{ *seal.MemoryBackend }

func (losingSealStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, nil
}

type losingKVStore struct{ *memKVStore }

func (losingKVStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, nil
}

// An update that loses its swap on every attempt gives up after a bounded
// number of attempts with *ConcurrentUpdateError and leaves the record as it
// was.
func TestUpdateTenant_ContentionBeyondTheBoundIsConcurrentUpdate(t *testing.T) {
	barrierSvc, _ := newTestServiceWithStore(t, losingSealStore{seal.NewMemoryBackend()})
	kvSvc, err := NewKVTenantService(losingKVStore{newMemKVStore()}, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	for name, svc := range map[string]TenantServicer{"barrier": barrierSvc, "kv": kvSvc} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}
			_, err := svc.UpdateTenant(ctx, "acme", TenantPatch{Name: "Acme Corp"})
			var conflict *ConcurrentUpdateError
			if !errors.As(err, &conflict) || conflict.TenantID != "acme" || conflict.Attempts != maxTenantUpdateAttempts {
				t.Fatalf("UpdateTenant = %v, want *ConcurrentUpdateError{acme, %d attempts}", err, maxTenantUpdateAttempts)
			}
			stored, err := svc.GetTenant(ctx, "acme")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if stored.Name != "Acme" {
				t.Fatalf("stored name = %q, want the unchanged \"Acme\"", stored.Name)
			}
		})
	}
}

// contendedSealStore loses its first lose compare-and-swaps, as a record
// rewritten that many times under an update would, then swaps for real;
// onLoss, when set, runs on every lost swap.
type contendedSealStore struct {
	*seal.MemoryBackend
	lose   int
	swaps  int
	onLoss func()
}

func (s *contendedSealStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	s.swaps++
	if s.swaps <= s.lose {
		if s.onLoss != nil {
			s.onLoss()
		}
		return false, nil
	}
	return s.MemoryBackend.CompareAndSwap(ctx, key, expected, next)
}

// An update that loses every attempt but the last commits on the last, and
// each lost attempt's status audit is settled as not committed.
func TestUpdateTenant_RetriesThenCommits(t *testing.T) {
	ctx := context.Background()
	store := &contendedSealStore{MemoryBackend: seal.NewMemoryBackend()}
	svc, _ := newTestServiceWithStore(t, store)
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	a := &fakeStatusAuditor{}
	svc.SetStatusChangeAuditor(a)
	store.lose, store.swaps = maxTenantUpdateAttempts-1, 0

	got, err := svc.UpdateTenant(ctx, "acme", statusPatch(StatusSuspended))
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if got.Status != StatusSuspended || store.swaps != maxTenantUpdateAttempts {
		t.Fatalf("UpdateTenant = %s after %d swaps, want suspended after %d", got.Status, store.swaps, maxTenantUpdateAttempts)
	}
	if len(a.changes) != maxTenantUpdateAttempts {
		t.Fatalf("audited %d attempts, want %d", len(a.changes), maxTenantUpdateAttempts)
	}
	for i, c := range a.changes[:maxTenantUpdateAttempts-1] {
		var conflict *ConcurrentUpdateError
		if c.settled != 1 || !errors.As(c.err, &conflict) || conflict.Attempts != i+1 {
			t.Fatalf("lost attempt %d audited %+v, want settled once with *ConcurrentUpdateError{Attempts: %d}", i+1, *c, i+1)
		}
	}
	if last := a.changes[maxTenantUpdateAttempts-1]; last.settled != 1 || last.err != nil {
		t.Fatalf("committed attempt audited %+v, want settled once, committed", *last)
	}
}

// The request context bounds the retry: an update whose context ends while
// it is contending stops with the context's error, not another attempt.
func TestUpdateTenant_ContextEndsTheRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &contendedSealStore{MemoryBackend: seal.NewMemoryBackend()}
	svc, _ := newTestServiceWithStore(t, store)
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	store.lose, store.swaps, store.onLoss = maxTenantUpdateAttempts, 0, cancel

	if _, err := svc.UpdateTenant(ctx, "acme", TenantPatch{Name: "Acme Corp"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("UpdateTenant = %v, want context.Canceled", err)
	}
	if store.swaps != 1 {
		t.Fatalf("swapped %d times, want 1: the retry must stop once the context ends", store.swaps)
	}
}

// faultingSwapStore fails every compare-and-swap with an error, so its
// outcome is unknown.
type faultingSwapStore struct{ *seal.MemoryBackend }

func (faultingSwapStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, errStoreFault
}

// A swap that fails is a *StoreError, and the status audit it had begun is
// settled with that failure.
func TestUpdateTenant_SwapFailureIsStoreError(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestServiceWithStore(t, faultingSwapStore{seal.NewMemoryBackend()})
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	a := &fakeStatusAuditor{}
	svc.SetStatusChangeAuditor(a)

	_, err := svc.UpdateTenant(ctx, "acme", statusPatch(StatusSuspended))
	var se *StoreError
	if !errors.As(err, &se) || !errors.Is(err, errStoreFault) {
		t.Fatalf("UpdateTenant = %v, want *StoreError wrapping the fault", err)
	}
	if len(a.changes) != 1 || a.changes[0].settled != 1 || !errors.Is(a.changes[0].err, errStoreFault) {
		t.Fatalf("audited %v, want one change settled with the fault", a.changes)
	}
}

// plainSealView exposes only seal.StorageBackend's methods, withholding the
// compare-and-swap an update commits through.
type plainSealView struct{ seal.StorageBackend }

// unguardedSealStore declares a compare-and-swap it cannot honour, as a
// seal.StorageBarrier over a base without one does.
type unguardedSealStore struct{ *seal.MemoryBackend }

func (unguardedSealStore) CompareAndSwap(context.Context, string, []byte, []byte) (bool, error) {
	return false, seal.ErrGuardedSwapUnsupported
}

// A store that cannot commit against the record that was read refuses every
// update with ErrTenantUpdateNotAtomic rather than falling back to a blind
// write, and leaves the record as it was.
func TestUpdateTenant_StoreWithoutSwapRefused(t *testing.T) {
	ctx := context.Background()
	unguarded := unguardedSealStore{seal.NewMemoryBackend()}
	unguardedSvc, _ := newTestServiceWithStore(t, unguarded)
	if _, err := unguardedSvc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	plainSvc := &TenantService{store: plainSealView{unguarded}, logger: slog.Default()}
	kvInner := newMemKVStore()
	kvSvc, err := NewKVTenantService(kvInner, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	if _, err := kvSvc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	plainKV := &kvTenantService{store: plainKVStoreView{kvInner}, logger: slog.Default()}

	// Each case updates through one service and reads the record back through
	// a fully built one over the same store.
	for name, c := range map[string]struct{ update, read TenantServicer }{
		"barrier-unguarded": {unguardedSvc, unguardedSvc},
		"barrier-plain":     {plainSvc, unguardedSvc},
		"kv-plain":          {plainKV, kvSvc},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.update.UpdateTenant(ctx, "acme", statusPatch(StatusSuspended)); !errors.Is(err, ErrTenantUpdateNotAtomic) {
				t.Fatalf("UpdateTenant = %v, want ErrTenantUpdateNotAtomic", err)
			}
			stored, err := c.read.GetTenant(ctx, "acme")
			if err != nil {
				t.Fatalf("GetTenant: %v", err)
			}
			if stored.Status != StatusActive {
				t.Fatalf("stored status = %s, want the unchanged active", stored.Status)
			}
		})
	}
}

func TestConcurrentUpdateError_NamesTenantAndAttempts(t *testing.T) {
	err := &ConcurrentUpdateError{TenantID: "acme", Attempts: 3}
	if got, want := err.Error(), `tenant: update of "acme" lost to concurrent writers after 3 attempts`; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, ErrConcurrentUpdate) {
		t.Fatal("ConcurrentUpdateError must match ErrConcurrentUpdate")
	}
	if errors.Is(err, ErrTenantNotFound) {
		t.Fatal("ConcurrentUpdateError must match only ErrConcurrentUpdate")
	}
}
