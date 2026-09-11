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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// discardLogger keeps migration output out of test logs without discarding
// the code path that produces it.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// ownersFor returns the PRODUCTION identity index over the harness resolver.
// The migration's whole refusal rule turns on what this index answers for an
// unclaimed id, so a stub here would be a test of the stub.
func ownersFor(t *testing.T, h *factorTenantHarness) tenant.IdentityIndex {
	t.Helper()
	owners, err := tenant.NewResolvedIdentityIndex(h.res)
	if err != nil {
		t.Fatalf("NewResolvedIdentityIndex: %v", err)
	}
	return owners
}

// claimUser records a user id as belonging to a tenant, which is what
// pkg/user's store does when it creates a user.
func claimUser(t *testing.T, owners tenant.IdentityIndex, userID uint64, tenantID string) {
	t.Helper()
	if _, err := owners.Record(context.Background(), tenant.IdentityUser, tenant.IdentityID(userID), tenantID); err != nil {
		t.Fatalf("Record(%d -> %s): %v", userID, tenantID, err)
	}
}

// writeLegacyFactor writes a factor's records at the flat, untenanted root —
// the exact layout every pre-routing build produced.
func writeLegacyFactor(t *testing.T, store kvstore.KVStore, f *auth.AuthFactor) {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, primaryKey(f.ID), data); err != nil {
		t.Fatalf("put primary: %v", err)
	}
	if err := store.Put(ctx, userIndexKey(f.UserID, f.ID), []byte(f.ID)); err != nil {
		t.Fatalf("put user index: %v", err)
	}
	if len(f.Credential) > 0 {
		if err := store.Put(ctx, lookupKeyFor(f.Type, f.Credential), []byte(f.ID)); err != nil {
			t.Fatalf("put lookup: %v", err)
		}
	}
	if len(f.SPKICredential) > 0 {
		if err := store.Put(ctx, lookupKeyFor(f.Type, f.SPKICredential), []byte(f.ID)); err != nil {
			t.Fatalf("put spki lookup: %v", err)
		}
	}
}

// flatKeyCount counts everything still sitting in the untenanted keyspace.
func flatKeyCount(t *testing.T, store kvstore.KVStore) int {
	t.Helper()
	total := 0
	for _, prefix := range []string{userFactorPrimaryPrefix, userFactorByUserPrefix, userFactorLookupPrefix} {
		keys, err := store.List(context.Background(), prefix)
		if err != nil {
			t.Fatalf("List(%s): %v", prefix, err)
		}
		total += len(keys)
	}
	return total
}

// ---------------------------------------------------------------------------
// The happy path
// ---------------------------------------------------------------------------

// TestMigrateFactorsPlacesRecordsInTheOwningTenant asserts a legacy record
// moves into the tenant its user belongs to, resolves there afterwards, does
// NOT resolve in another tenant, and leaves nothing behind at the root.
func TestMigrateFactorsPlacesRecordsInTheOwningTenant(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	owners := ownersFor(t, h)
	claimUser(t, owners, 1001, "acme")

	cred := []byte("legacy-credential")
	writeLegacyFactor(t, h.shared, mtlsFactor("legacy-1", 1001, cred))

	report, err := MigrateFactorsToTenants(context.Background(), h.shared, h.res, owners, discardLogger())
	if err != nil {
		t.Fatalf("MigrateFactorsToTenants: %v", err)
	}
	if report.Scanned != 1 || report.Migrated != 1 || len(report.Refusals) != 0 {
		t.Fatalf("report = %+v, want scanned 1 migrated 1 refused 0", report)
	}

	got, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, cred)
	if err != nil {
		t.Fatalf("acme cannot resolve its migrated factor: %v", err)
	}
	if got.ID != "legacy-1" {
		t.Fatalf("resolved %q, want legacy-1", got.ID)
	}
	if _, err := h.svc.LookupByCredentialKey(ctxFor("globex"), auth.AuthFactorMTLS, cred); err == nil {
		t.Fatal("globex resolved a factor migrated into acme")
	}
	if n := flatKeyCount(t, h.shared); n != 0 {
		t.Fatalf("%d legacy key(s) survived the migration", n)
	}
}

// TestMigrateFactorsMovesTheSPKIIndex asserts the renewal-stable secondary
// lookup key travels with the record. Losing it would silently break login
// for every certificate renewed with the same keypair — the exact failure the
// SPKI index exists to prevent.
func TestMigrateFactorsMovesTheSPKIIndex(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 7, "acme")

	f := mtlsFactor("legacy-spki", 7, []byte("cert-der"))
	f.SPKICredential = []byte("spki-digest")
	writeLegacyFactor(t, h.shared, f)

	if _, err := MigrateFactorsToTenants(context.Background(), h.shared, h.res, owners, discardLogger()); err != nil {
		t.Fatalf("MigrateFactorsToTenants: %v", err)
	}
	got, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("spki-digest"))
	if err != nil {
		t.Fatalf("SPKI lookup after migration: %v", err)
	}
	if got.ID != "legacy-spki" {
		t.Fatalf("resolved %q", got.ID)
	}
}

// TestMigrateFactorsPreservesUnknownFields asserts the record is copied
// verbatim rather than re-marshalled, so a field written by a newer build
// survives a migration run by an older one.
func TestMigrateFactorsPreservesUnknownFields(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 3, "acme")

	raw := []byte(`{"id":"verbatim","user_id":3,"type":"mtls","credential":"Y3JlZA==","future_field":"kept"}`)
	if err := h.shared.Put(context.Background(), primaryKey("verbatim"), raw); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := MigrateFactorsToTenants(context.Background(), h.shared, h.res, owners, discardLogger()); err != nil {
		t.Fatalf("MigrateFactorsToTenants: %v", err)
	}
	stored, err := h.shared.Get(context.Background(), tenant.IdentityNamespace("acme")+primaryKey("verbatim"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(stored, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["future_field"] != "kept" {
		t.Fatalf("unknown field dropped: %v", decoded)
	}
}

// TestMigrateFactorsIsResumable asserts a second run over an already-migrated
// store is a no-op rather than an error or a duplicate, which is what makes an
// interrupted run safe to finish.
func TestMigrateFactorsIsResumable(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 8, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("twice", 8, []byte("cred-twice")))

	if _, err := MigrateFactorsToTenants(context.Background(), h.shared, h.res, owners, discardLogger()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	report, err := MigrateFactorsToTenants(context.Background(), h.shared, h.res, owners, discardLogger())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if report.Scanned != 0 || report.Migrated != 0 || report.Pruned != 0 {
		t.Fatalf("second run was not a no-op: %+v", report)
	}
	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("cred-twice")); err != nil {
		t.Fatalf("record damaged by the second run: %v", err)
	}
}

// TestMigrateFactorsPrunesDanglingIndexEntries asserts a legacy index entry
// naming a record that no longer exists — the tail of an interrupted run — is
// removed rather than left as a credential-shaped key nothing reads.
func TestMigrateFactorsPrunesDanglingIndexEntries(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	ctx := context.Background()

	if err := h.shared.Put(ctx, lookupKeyFor(auth.AuthFactorMTLS, []byte("ghost")), []byte("gone")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := h.shared.Put(ctx, userIndexKey(99, "gone"), []byte("gone")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	report, err := MigrateFactorsToTenants(ctx, h.shared, h.res, owners, discardLogger())
	if err != nil {
		t.Fatalf("MigrateFactorsToTenants: %v", err)
	}
	if report.Pruned != 2 {
		t.Fatalf("pruned %d, want 2", report.Pruned)
	}
	if n := flatKeyCount(t, h.shared); n != 0 {
		t.Fatalf("%d dangling key(s) survived", n)
	}
}

// ---------------------------------------------------------------------------
// The refusal path — the part the ruling on go-qrdb#393 asked for by name
// ---------------------------------------------------------------------------

// TestMigrateFactorsRefusesAnUnindexedOwner asserts a record whose owning
// tenant cannot be established is REFUSED: named in a typed error, left
// exactly where it is, and placed in no tenant at all. Defaulting it into the
// caller's tenant, the system tenant, or the only tenant that exists is the
// silent mis-assignment this test exists to forbid.
func TestMigrateFactorsRefusesAnUnindexedOwner(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	cred := []byte("orphan-credential")
	writeLegacyFactor(t, h.shared, mtlsFactor("orphan", 4242, cred))

	report, err := MigrateFactorsToTenants(context.Background(), h.shared, h.res, owners, discardLogger())
	var refused *FactorMigrationRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want *FactorMigrationRefusedError, got %T: %v", err, err)
	}
	if len(refused.Refusals) != 1 {
		t.Fatalf("refusals = %d, want 1", len(refused.Refusals))
	}
	r := refused.Refusals[0]
	if !errors.Is(r.Err, ErrFactorOwnerUnindexed) {
		t.Fatalf("reason = %v, want ErrFactorOwnerUnindexed", r.Err)
	}
	if r.FactorID != "orphan" || r.UserID != 4242 {
		t.Fatalf("refusal does not name the record: %+v", r)
	}
	if report.Migrated != 0 {
		t.Fatalf("migrated %d records despite the refusal", report.Migrated)
	}

	// The record is still at the root, and nowhere else.
	if v, err := h.shared.Get(context.Background(), primaryKey("orphan")); err != nil || len(v) == 0 {
		t.Fatal("the refused record was removed from the legacy keyspace")
	}
	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, cred); err == nil {
		t.Fatal("a refused record was placed in a guessed tenant")
	}
}

// TestMigrateFactorsPlacesWhatItCanAlongsideARefusal asserts a refusal does
// not abandon the records whose owners ARE known: the run is per record, so
// one orphan cannot strand every other credential in the deployment.
func TestMigrateFactorsPlacesWhatItCanAlongsideARefusal(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 1, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("placeable", 1, []byte("good-cred")))
	writeLegacyFactor(t, h.shared, mtlsFactor("orphan", 4242, []byte("orphan-cred")))

	report, err := MigrateFactorsToTenants(context.Background(), h.shared, h.res, owners, discardLogger())
	var refused *FactorMigrationRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want *FactorMigrationRefusedError, got %v", err)
	}
	if report.Migrated != 1 || len(report.Refusals) != 1 {
		t.Fatalf("report = %+v, want migrated 1 refused 1", report)
	}
	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("good-cred")); err != nil {
		t.Fatalf("the placeable record was not placed: %v", err)
	}
}

// TestMigrateFactorsRefusesRecordsItCannotRead covers the two remaining
// refusal reasons: a record naming no user, and a value that is not a factor.
func TestMigrateFactorsRefusesRecordsItCannotRead(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	ctx := context.Background()

	writeLegacyFactor(t, h.shared, mtlsFactor("no-owner", 0, []byte("c1")))
	if err := h.shared.Put(ctx, primaryKey("corrupt"), []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := MigrateFactorsToTenants(ctx, h.shared, h.res, owners, discardLogger())
	var refused *FactorMigrationRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want *FactorMigrationRefusedError, got %v", err)
	}
	reasons := map[string]error{}
	for _, r := range refused.Refusals {
		reasons[r.Key] = r.Err
	}
	if got := reasons[primaryKey("no-owner")]; !errors.Is(got, ErrFactorOwnerAbsent) {
		t.Errorf("no-owner reason = %v, want ErrFactorOwnerAbsent", got)
	}
	if got := reasons[primaryKey("corrupt")]; !errors.Is(got, ErrFactorRecordUnreadable) {
		t.Errorf("corrupt reason = %v, want ErrFactorRecordUnreadable", got)
	}
	if msg := refused.Error(); msg == "" || !strings.Contains(msg, "no-owner") {
		t.Errorf("the error does not name the refused records: %q", msg)
	}
}

// TestMigrateFactorsRejectsMissingDependencies asserts the entry point refuses
// rather than silently doing nothing when a dependency is absent.
func TestMigrateFactorsRejectsMissingDependencies(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	ctx := context.Background()

	for name, call := range map[string]func() error{
		"nil store":    func() error { _, e := MigrateFactorsToTenants(ctx, nil, h.res, owners, nil); return e },
		"nil resolver": func() error { _, e := MigrateFactorsToTenants(ctx, h.shared, nil, owners, nil); return e },
		"nil owners":   func() error { _, e := MigrateFactorsToTenants(ctx, h.shared, h.res, nil, nil); return e },
	} {
		err := call()
		var invalid *auth.InvalidFactorError
		if !errors.As(err, &invalid) {
			t.Errorf("%s: want *auth.InvalidFactorError, got %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// The migration runs inside the service, before it serves anything
// ---------------------------------------------------------------------------

// newMigratingHarness builds a routed service carrying the legacy store, which
// is how every composition root wires it.
func newMigratingHarness(t *testing.T, tenantIDs ...string) *factorTenantHarness {
	t.Helper()
	h := newFactorTenantHarness(t, tenantIDs...)
	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{
		Resolver:    h.res,
		LegacyStore: h.shared,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}
	h.svc = svc
	return h
}

// TestRoutedServiceMigratesBeforeItServes asserts a credential enrolled before
// routing existed resolves through the routed service without any separate
// migration call — the ordering the composition roots must not be able to get
// wrong.
func TestRoutedServiceMigratesBeforeItServes(t *testing.T) {
	h := newMigratingHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 21, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("pre-routing", 21, []byte("old-cred")))

	got, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("old-cred"))
	if err != nil {
		t.Fatalf("a credential enrolled before routing no longer resolves: %v", err)
	}
	if got.ID != "pre-routing" {
		t.Fatalf("resolved %q", got.ID)
	}
	if n := flatKeyCount(t, h.shared); n != 0 {
		t.Fatalf("%d legacy key(s) survived", n)
	}
}

// TestRoutedServiceMigratesExactlyOnce asserts the migration is one-shot: a
// record written to the legacy keyspace AFTER the first call is not swept up,
// so the migration is not a standing background rewrite of the root keyspace.
func TestRoutedServiceMigratesExactlyOnce(t *testing.T) {
	h := newMigratingHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 22, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("first", 22, []byte("first-cred")))

	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("first-cred")); err != nil {
		t.Fatalf("first lookup: %v", err)
	}

	writeLegacyFactor(t, h.shared, mtlsFactor("second", 22, []byte("second-cred")))
	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("second-cred")); err == nil {
		t.Fatal("a record written to the legacy keyspace after the migration was still picked up")
	}
	if v, err := h.shared.Get(context.Background(), primaryKey("second")); err != nil || len(v) == 0 {
		t.Fatal("the later legacy record was moved by a second migration run")
	}
}

// TestRoutedServiceServesDespiteARefusal asserts a refused record is reported
// but does not withhold the surface: every other credential keeps working, and
// the refused record resolves for nobody.
func TestRoutedServiceServesDespiteARefusal(t *testing.T) {
	h := newMigratingHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 1, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("placeable", 1, []byte("good-cred")))
	writeLegacyFactor(t, h.shared, mtlsFactor("orphan", 4242, []byte("orphan-cred")))

	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("good-cred")); err != nil {
		t.Fatalf("placeable credential does not resolve: %v", err)
	}
	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("orphan-cred")); err == nil {
		t.Fatal("the refused record resolves; it must resolve for nobody")
	}
	if v, err := h.shared.Get(context.Background(), primaryKey("orphan")); err != nil || len(v) == 0 {
		t.Fatal("the refused record was removed from the legacy keyspace")
	}
}

// failingLegacyStore is a legacy store whose List fails the way a Raft read
// does on a shard with no leader yet, then succeeds.
type failingLegacyStore struct {
	kvstore.KVStore
	failures atomic.Int32
}

// ErrNoLeaderForTest stands in for the store's own not-leader error.
var ErrNoLeaderForTest = errors.New("service: not leader for shard 1, leader unknown")

func (s *failingLegacyStore) List(ctx context.Context, prefix string) ([]string, error) {
	if s.failures.Add(-1) >= 0 {
		return nil, ErrNoLeaderForTest
	}
	return s.KVStore.List(ctx, prefix)
}

// TestRoutedServiceRetriesAFailedMigration asserts a store failure does not
// consume the one-shot: the caller is told, and the next call tries again.
//
// This is the case that made the migration lazy. A composition root builds its
// services while the shard is still electing, so a linearizable read at
// construction fails on a node that is otherwise healthy; consuming the attempt
// there would leave every legacy record stranded for the life of the process.
func TestRoutedServiceRetriesAFailedMigration(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 23, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("retried", 23, []byte("retried-cred")))

	legacy := &failingLegacyStore{KVStore: h.shared}
	legacy.failures.Store(1)
	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{
		Resolver:    h.res,
		LegacyStore: legacy,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}

	_, err = svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("retried-cred"))
	if !errors.Is(err, ErrNoLeaderForTest) {
		t.Fatalf("the store failure was not surfaced: %v", err)
	}
	got, err := svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("retried-cred"))
	if err != nil {
		t.Fatalf("the retry did not run the migration: %v", err)
	}
	if got.ID != "retried" {
		t.Fatalf("resolved %q", got.ID)
	}
}

// TestRoutedServiceWithoutALegacyStoreSkipsTheMigration asserts a nil legacy
// store is simply no migration, not an error and not a scan of the root.
func TestRoutedServiceWithoutALegacyStoreSkipsTheMigration(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 24, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("untouched", 24, []byte("untouched-cred")))

	// h.svc was built with no LegacyStore.
	if _, err := h.svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("untouched-cred")); err == nil {
		t.Fatal("a service with no legacy store resolved a legacy record")
	}
	if v, err := h.shared.Get(context.Background(), primaryKey("untouched")); err != nil || len(v) == 0 {
		t.Fatal("a service with no legacy store moved a legacy record")
	}
}

// ---------------------------------------------------------------------------
// A destination that is merely unreachable
//
// Every type below is the PRODUCTION one, including the sealed state: a
// dedicated-isolation tenant created through the real tenant service, resolved
// by the real StoreResolver with no barrier registry, fails closed with
// tenant.ErrTenantSealed. That is the same answer a self-managed barrier tenant
// gives from creation until its admin unseals it, and the same answer a node
// that does not host a tenant's barrier gives until key delivery arrives. No
// double is involved, so the scenario cannot be truer than the code.
// ---------------------------------------------------------------------------

// sealedTenant provisions a dedicated-isolation tenant whose store the harness
// resolver cannot reach, and returns its id.
func sealedTenant(t *testing.T, h *factorTenantHarness, id string) string {
	t.Helper()
	if _, err := h.tenants.CreateTenant(context.Background(), id, id, tenant.IsolationVolume); err != nil {
		t.Fatalf("CreateTenant(%s): %v", id, err)
	}
	if _, err := h.res.Resolve(context.Background(), id); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("the harness resolver reaches %s (%v); the test would not exercise an unreachable destination", id, err)
	}
	return id
}

// TestMigrateFactorsRefusesAnUnreachableTenantAndCompletesTheRun asserts a
// destination that cannot be reached is a per-record REFUSAL rather than a
// failure of the run.
//
// It is the defect this case exists for: classified as a store failure, one
// record owned by a sealed tenant aborted the run, and because the run gates
// the routed service it took every OTHER tenant's login down with it —
// permanently, since nothing deletes the record and every retry reaches it
// again.
func TestMigrateFactorsRefusesAnUnreachableTenantAndCompletesTheRun(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	sealed := sealedTenant(t, h, "sealedco")
	owners := ownersFor(t, h)
	claimUser(t, owners, 900, sealed)
	claimUser(t, owners, 1, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("sealed-owned", 900, []byte("sealed-cred")))
	writeLegacyFactor(t, h.shared, mtlsFactor("healthy", 1, []byte("healthy-cred")))

	report, err := MigrateFactorsToTenants(context.Background(), h.shared, h.res, owners, discardLogger())
	var refused *FactorMigrationRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("want *FactorMigrationRefusedError, got %T: %v", err, err)
	}
	if errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatal("the sealed destination was reported as a run failure")
	}

	// The run finished, and the reachable record was placed.
	if report.Migrated != 1 || len(report.Refusals) != 1 {
		t.Fatalf("report = %+v, want migrated 1 refused 1", report)
	}
	r := refused.Refusals[0]
	if !errors.Is(r.Err, ErrFactorOwnerTenantUnreachable) {
		t.Fatalf("reason = %v, want ErrFactorOwnerTenantUnreachable", r.Err)
	}
	if !r.Transient || !refused.Transient() {
		t.Fatalf("an unreachable destination must be classified transient: %+v", r)
	}
	if r.FactorID != "sealed-owned" || r.UserID != 900 {
		t.Fatalf("the refusal does not name the record: %+v", r)
	}

	// Refused, not lost.
	if v, gErr := h.shared.Get(context.Background(), primaryKey("sealed-owned")); gErr != nil || len(v) == 0 {
		t.Fatal("the refused record was removed from the legacy keyspace")
	}
}

// TestRoutedServiceServesEveryOtherTenantWhenOneIsUnreachable is the same
// scenario through the service every transport holds: a healthy tenant must
// resolve its own credentials while another tenant's store is unreachable.
func TestRoutedServiceServesEveryOtherTenantWhenOneIsUnreachable(t *testing.T) {
	h := newFactorTenantHarness(t, "acme", "globex")
	sealed := sealedTenant(t, h, "sealedco")
	owners := ownersFor(t, h)
	claimUser(t, owners, 900, sealed)
	claimUser(t, owners, 1, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("sealed-owned", 900, []byte("sealed-cred")))
	writeLegacyFactor(t, h.shared, mtlsFactor("healthy", 1, []byte("healthy-cred")))

	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{
		Resolver: h.res, LegacyStore: h.shared, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}

	got, err := svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("healthy-cred"))
	if err != nil {
		t.Fatalf("a healthy tenant was taken down by another tenant's unreachable store: %v", err)
	}
	if got.ID != "healthy" {
		t.Fatalf("acme resolved %q, want healthy", got.ID)
	}

	// A tenant with nothing to migrate is served too, and is not told about
	// somebody else's barrier.
	if err := svc.Add(ctxFor("globex"), 2, mtlsFactor("globex-factor", 2, []byte("globex-cred"))); err != nil {
		t.Fatalf("an unrelated tenant could not enrol: %v", err)
	}
	if _, err := svc.LookupByCredentialKey(ctxFor("globex"), auth.AuthFactorMTLS, []byte("globex-cred")); err != nil {
		t.Fatalf("an unrelated tenant could not resolve: %v", err)
	}

	// The sealed tenant's own call still fails closed — that is its state, and
	// it is the only caller entitled to hear about it.
	if _, err := svc.LookupByCredentialKey(ctxFor(sealed), auth.AuthFactorMTLS, []byte("sealed-cred")); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Fatalf("the unreachable tenant's own call = %v, want tenant.ErrTenantSealed", err)
	}
}

// TestRoutedServiceRetriesAfterATransientRefusal asserts a transient refusal
// does NOT mark the migration done: once the destination becomes reachable, a
// later call places the record.
//
// Without this the resumability the migration documents would be false —
// a barrier sealed for ten minutes, or key delivery that had not yet reached
// this node, would strand the record for the life of the process.
func TestRoutedServiceRetriesAfterATransientRefusal(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	sealed := sealedTenant(t, h, "sealedco")
	owners := ownersFor(t, h)
	claimUser(t, owners, 900, sealed)
	writeLegacyFactor(t, h.shared, mtlsFactor("sealed-owned", 900, []byte("sealed-cred")))

	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{
		Resolver: h.res, LegacyStore: h.shared, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}

	// First call: the record cannot be placed, and the service still answers.
	if _, err := svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("absent")); err == nil {
		t.Fatal("expected a not-found for a credential nobody enrolled")
	}
	if v, gErr := h.shared.Get(context.Background(), primaryKey("sealed-owned")); gErr != nil || len(v) == 0 {
		t.Fatal("the record was moved while its tenant was unreachable")
	}

	// The tenant becomes reachable. The production resolver reads the tenant's
	// isolation on every call rather than caching the decision, so changing it
	// is enough — no double stands in for the transition.
	if _, uErr := h.tenants.UpdateTenant(context.Background(), sealed,
		tenant.TenantPatch{IsolationType: tenant.IsolationPrefix}); uErr != nil {
		t.Fatalf("UpdateTenant: %v", uErr)
	}
	if _, rErr := h.res.Resolve(context.Background(), sealed); rErr != nil {
		t.Fatalf("the tenant is still unreachable after the transition: %v", rErr)
	}
	if _, err := svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("absent")); err == nil {
		t.Fatal("expected a not-found for a credential nobody enrolled")
	}
	moved, err := h.shared.Get(context.Background(), tenant.IdentityNamespace(sealed)+primaryKey("sealed-owned"))
	if err != nil || len(moved) == 0 {
		t.Fatalf("the record was not placed after its tenant became reachable (err=%v)", err)
	}
	if v, gErr := h.shared.Get(context.Background(), primaryKey("sealed-owned")); gErr == nil && len(v) > 0 {
		t.Fatal("the record was copied rather than moved")
	}
}

// TestRoutedServiceLatchesOnATerminalRefusal asserts the other half of the
// distinction: a refusal nothing can resolve consumes the one-shot, so the
// legacy keyspace is not re-scanned for ever on every factor call.
func TestRoutedServiceLatchesOnATerminalRefusal(t *testing.T) {
	h := newFactorTenantHarness(t, "acme")
	owners := ownersFor(t, h)
	claimUser(t, owners, 1, "acme")
	writeLegacyFactor(t, h.shared, mtlsFactor("placeable", 1, []byte("good-cred")))
	writeLegacyFactor(t, h.shared, mtlsFactor("orphan", 4242, []byte("orphan-cred")))

	svc, err := NewRoutingUserAuthFactorService(RoutingUserAuthFactorServiceConfig{
		Resolver: h.res, LegacyStore: h.shared, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewRoutingUserAuthFactorService: %v", err)
	}
	if _, err := svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("good-cred")); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Claiming the orphan's owner now would let a re-run place it. The
	// migration has latched, so it must not.
	claimUser(t, owners, 4242, "acme")
	if _, err := svc.LookupByCredentialKey(ctxFor("acme"), auth.AuthFactorMTLS, []byte("orphan-cred")); err == nil {
		t.Fatal("a terminal refusal did not consume the one-shot; the legacy keyspace is re-scanned for ever")
	}
}

// TestRefusalClassificationCoversEveryReason asserts every refusal reason is
// classified, because an unlisted one would take a default and defaulting wrong
// either strands a record for ever or re-scans the keyspace for ever.
func TestRefusalClassificationCoversEveryReason(t *testing.T) {
	for reason, wantTransient := range map[error]bool{
		ErrFactorOwnerUnindexed:         false,
		ErrFactorOwnerAbsent:            false,
		ErrFactorRecordUnreadable:       false,
		ErrFactorOwnerTenantUnreachable: true,
	} {
		got, ok := refusalIsTransient[reason]
		if !ok {
			t.Errorf("%v is not classified", reason)
			continue
		}
		if got != wantTransient {
			t.Errorf("%v classified transient=%v, want %v", reason, got, wantTransient)
		}
		if r := newRefusal("k", "f", 1, reason); r.Transient != wantTransient {
			t.Errorf("newRefusal(%v).Transient = %v, want %v", reason, r.Transient, wantTransient)
		}
	}
	if n := len(refusalIsTransient); n != 4 {
		t.Errorf("refusalIsTransient holds %d reasons; a new one must be classified here too", n)
	}
	empty := &FactorMigrationRefusedError{}
	if empty.Transient() {
		t.Error("an error with no refusals reported transient")
	}
}
