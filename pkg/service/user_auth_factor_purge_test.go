// Copyright (c) 2026 Jeremy Hahn
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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// The pending-user reaper purges through these.
var (
	_ user.FactorPurger = (*userAuthFactorService)(nil)
	_ user.FactorPurger = (*routingUserAuthFactorService)(nil)
)

func newPurger(t *testing.T) (UserAuthFactorServicer, UserAuthFactorPurger) {
	t.Helper()
	svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
		KV:               kvstoretest.NewGuardedMemoryStore(),
		UserStateUpdater: newStubUserStateUpdater(),
	})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	purger, ok := svc.(UserAuthFactorPurger)
	if !ok {
		t.Fatal("auth factor service does not implement UserAuthFactorPurger")
	}
	return svc, purger
}

// Purge removes a revoked factor and its credential index, which a normal
// Delete still refuses, along with the active ones; another user's factors
// stay. A second purge finds nothing to do.
func TestPurge_RemovesRevokedFactorsAndIndexes(t *testing.T) {
	svc, purger := newPurger(t)
	ctx := context.Background()
	for _, f := range []*auth.AuthFactor{
		mtlsFactor("primary", 1, []byte("k1")),
		fido2Factor("backup", 1, []byte("k2")),
		mtlsFactor("other", 2, []byte("k3")),
	} {
		if err := svc.Add(ctx, f.UserID, f); err != nil {
			t.Fatalf("Add %s: %v", f.ID, err)
		}
	}
	if err := svc.Remove(ctx, "backup", false); err != nil {
		t.Fatalf("Remove backup: %v", err)
	}
	if err := svc.Delete(ctx, "backup", true); !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("Delete of a revoked factor = %v, want ErrFactorRevoked", err)
	}

	n, err := purger.Purge(ctx, 1)
	if err != nil || n != 2 {
		t.Fatalf("Purge = %d, %v; want 2, nil", n, err)
	}
	if left, err := svc.List(ctx, 1); err != nil || len(left) != 0 {
		t.Fatalf("factors left after purge = %v, %v", left, err)
	}
	for typ, key := range map[auth.AuthFactorType]string{auth.AuthFactorMTLS: "k1", auth.AuthFactorFIDO2: "k2"} {
		if _, err := svc.LookupByCredentialKey(ctx, typ, []byte(key)); !errors.Is(err, auth.ErrFactorNotFound) {
			t.Fatalf("credential %s after purge = %v, want ErrFactorNotFound", key, err)
		}
	}
	if got, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k3")); err != nil || got.UserID != 2 {
		t.Fatalf("other user's factor after purge = %v, %v", got, err)
	}
	if n, err := purger.Purge(ctx, 1); err != nil || n != 0 {
		t.Fatalf("second Purge = %d, %v; want 0, nil", n, err)
	}
}

// An enrollment interrupted after the primary write but before the user
// index write leaves a primary no index entry names. Purge finds it from the
// canonical primaries and removes it with its credential reservation.
func TestPurge_RemovesPrimaryWithoutIndexEntry(t *testing.T) {
	svc, purger := newPurger(t)
	ctx := context.Background()
	kv := svc.(*userAuthFactorService).kv
	for _, f := range []*auth.AuthFactor{
		mtlsFactor("orphan", 1, []byte("k1")),
		mtlsFactor("other", 2, []byte("k3")),
	} {
		if err := svc.Add(ctx, f.UserID, f); err != nil {
			t.Fatalf("Add %s: %v", f.ID, err)
		}
	}
	deleter, ok := kv.(interface {
		Delete(context.Context, string) error
	})
	if !ok {
		t.Fatalf("test store %T cannot delete", kv)
	}
	if err := deleter.Delete(ctx, userIndexKey(1, "orphan")); err != nil {
		t.Fatalf("drop index entry: %v", err)
	}

	n, err := purger.Purge(ctx, 1)
	if err != nil || n != 1 {
		t.Fatalf("Purge = %d, %v; want 1, nil", n, err)
	}
	assertNoPrimary(t, kv, "orphan")
	if _, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k1")); !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("credential after purge = %v, want ErrFactorNotFound", err)
	}
	if got, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k3")); err != nil || got.UserID != 2 {
		t.Fatalf("other user's factor after purge = %v, %v", got, err)
	}
}

// A primary record that cannot be decoded fails the purge instead of being
// skipped, since a skipped record could be the user's and survive.
func TestPurge_RefusesUndecodablePrimary(t *testing.T) {
	svc, purger := newPurger(t)
	ctx := context.Background()
	kv := svc.(*userAuthFactorService).kv
	if err := svc.Add(ctx, 1, mtlsFactor("primary", 1, []byte("k1"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := kv.Put(ctx, primaryKey("garbled"), []byte("{not json")); err != nil {
		t.Fatalf("Put garbled primary: %v", err)
	}
	var decodeErr *FactorSetDecodeError
	if _, err := purger.Purge(ctx, 1); !errors.As(err, &decodeErr) {
		t.Fatalf("Purge over an undecodable primary = %v, want FactorSetDecodeError", err)
	}
}

// Purge seals the user's factor set first, so an enrollment that arrives
// after it began -- the reaper's claim-to-purge window -- is refused rather
// than leaving a factor behind for a deleted account.
func TestPurge_SealRefusesLaterEnrollment(t *testing.T) {
	svc, purger := newPurger(t)
	ctx := context.Background()
	if err := svc.Add(ctx, 7, mtlsFactor("first", 7, []byte("k7"))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := purger.Purge(ctx, 7); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	err := svc.Add(ctx, 7, mtlsFactor("late", 7, []byte("k8")))
	if !errors.Is(err, ErrFactorSetSealed) {
		t.Fatalf("enrollment after purge = %v, want ErrFactorSetSealed", err)
	}
	if _, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorMTLS, []byte("k8")); !errors.Is(err, auth.ErrFactorNotFound) {
		t.Fatalf("refused enrollment left its credential index: %v", err)
	}

	// A user that never enrolled is sealed too.
	if _, err := purger.Purge(ctx, 8); err != nil {
		t.Fatalf("Purge of a user without factors: %v", err)
	}
	if err := svc.Add(ctx, 8, mtlsFactor("fresh", 8, []byte("k9"))); !errors.Is(err, ErrFactorSetSealed) {
		t.Fatalf("first enrollment after purge = %v, want ErrFactorSetSealed", err)
	}
}

// TestPurge_ReaperEventsCarryTheRoutedTenant purges as the pending reaper
// does: no caller identity, a context routed to the account's tenant. Each
// factor deletion must name that tenant, so a tenant-filtered audit query
// finds it.
func TestPurge_ReaperEventsCarryTheRoutedTenant(t *testing.T) {
	h := newDeleteHarness(t)
	purger, ok := h.svc.(UserAuthFactorPurger)
	if !ok {
		t.Fatal("auth factor service does not implement UserAuthFactorPurger")
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	for _, f := range []*auth.AuthFactor{
		mtlsFactor("primary", 1, []byte("k1")),
		fido2Factor("backup", 1, []byte("k2")),
	} {
		if err := h.svc.Add(ctx, f.UserID, f); err != nil {
			t.Fatalf("Add %s: %v", f.ID, err)
		}
	}
	if n, err := purger.Purge(ctx, 1); err != nil || n != 2 {
		t.Fatalf("Purge = %d, %v; want 2, nil", n, err)
	}
	page, err := h.trail.Query(context.Background(), audit.Filter{TenantID: "acme"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	deleted := map[string]bool{}
	for _, ev := range page.Events {
		if ev.Action == "delete" {
			if ev.TenantID != "acme" {
				t.Fatalf("event %+v outside the tenant filter", ev)
			}
			deleted[ev.Resource] = true
		}
	}
	if !deleted["authfactor:primary"] || !deleted["authfactor:backup"] {
		t.Fatalf("tenant-filtered delete events = %v, want both factors", deleted)
	}
}

// sealRaceKV runs beforeReserve once, just before a factor primary's
// reservation, standing in for a purge on another node that lands while an
// enrollment is delayed before its reservation. From then on every attempt
// to remove that primary fails, standing in for a rollback that never runs.
type sealRaceKV struct {
	*kvstoretest.GuardedMemoryStore
	primary       string
	beforeReserve func()
	failRelease   bool
}

var errReleaseLost = errors.New("rollback lost")

func (s *sealRaceKV) reserving(key string) {
	if hook := s.beforeReserve; hook != nil && key == s.primary {
		s.beforeReserve = nil
		hook()
		s.failRelease = true
	}
}

func (s *sealRaceKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	s.reserving(key)
	return s.GuardedMemoryStore.PutIfAbsent(ctx, key, value)
}

func (s *sealRaceKV) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if isPrimaryReservation(guards, writes) {
		s.reserving(writes[0].Key)
		return s.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
	}
	if s.failRelease {
		for _, g := range guards {
			if g.Key == s.primary {
				return false, errReleaseLost
			}
		}
	}
	return s.GuardedMemoryStore.PutAllIfAllUnchanged(ctx, guards, writes)
}

func (s *sealRaceKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if s.failRelease && key == s.primary {
		return false, errReleaseLost
	}
	return s.GuardedMemoryStore.CompareAndDelete(ctx, key, expected)
}

func (s *sealRaceKV) Delete(ctx context.Context, key string) error {
	if s.failRelease && key == s.primary {
		return errReleaseLost
	}
	return s.GuardedMemoryStore.Delete(ctx, key)
}

func TestPurge_EnrollmentDelayedPastTheSealLeavesNoPrimary(t *testing.T) {
	ctx := context.Background()
	kv := &sealRaceKV{GuardedMemoryStore: kvstoretest.NewGuardedMemoryStore()}
	newNode := func() UserAuthFactorServicer {
		svc, err := NewUserAuthFactorService(UserAuthFactorServiceConfig{
			KV:               kv,
			UserStateUpdater: newStubUserStateUpdater(),
			AuditLogger:      audit.NewRingBuffer(16),
		})
		if err != nil {
			t.Fatalf("NewUserAuthFactorService: %v", err)
		}
		return svc
	}
	enroller, reaper := newNode(), newNode()
	purger, ok := reaper.(UserAuthFactorPurger)
	if !ok {
		t.Fatal("auth factor service does not implement UserAuthFactorPurger")
	}

	f := mtlsFactor("late", 1, []byte("k1"))
	kv.primary = primaryKey(f.ID)
	var purged int
	var purgeErr error
	kv.beforeReserve = func() { purged, purgeErr = purger.Purge(ctx, 1) }

	if err := enroller.Add(ctx, f.UserID, f); err == nil {
		t.Fatal("enrollment delayed past the purge seal succeeded")
	}
	// The purge may or may not have found the reservation; either way it
	// must succeed and no primary may outlive it.
	if purgeErr != nil {
		t.Fatalf("Purge = %d, %v; want no error", purged, purgeErr)
	}
	raw, err := kv.Get(ctx, primaryKey(f.ID))
	if err != nil {
		t.Fatalf("Get primary: %v", err)
	}
	if raw != nil {
		t.Fatal("a delayed enrollment left a primary record after the purge")
	}
}
