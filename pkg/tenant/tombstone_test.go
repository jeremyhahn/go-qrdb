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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// recordingReclaimer is a KeyspaceReclaimer that counts calls and can fail.
//
// It is deliberately the ONLY double in this file, and it stands in for a
// service several layers above this package. Everything the tombstone itself
// depends on -- the metadata store, the barrier registry, the tenant service
// -- is the real implementation.
type recordingReclaimer struct {
	calls []string
	err   error
}

func (r *recordingReclaimer) ReclaimTenantKeyspace(_ context.Context, tenantID string) error {
	r.calls = append(r.calls, tenantID)
	return r.err
}

// listingLister is a DatabaseLister returning a fixed answer.
type listingLister struct {
	names []string
	err   error
}

func (l *listingLister) TenantDatabases(_ context.Context, _ string) ([]string, error) {
	return l.names, l.err
}

// shredRecorder is a DEKShredder that records the tenants it shredded.
type shredRecorder struct {
	calls []string
	err   error
}

func (s *shredRecorder) DeleteTenantDEK(_ context.Context, tenantID string) error {
	s.calls = append(s.calls, tenantID)
	return s.err
}

// TestTombstone_PrefixDoesNotCollideWithMetadata is the property that lets
// ListTenants keep its prefix scan unchanged: the two prefixes are siblings,
// so neither scan sees the other's keys.
func TestTombstone_PrefixDoesNotCollideWithMetadata(t *testing.T) {
	if strings.HasPrefix(tombstonePrefix, metadataPrefix) {
		t.Errorf("tombstone prefix %q is INSIDE the metadata prefix %q; "+
			"ListTenants' scan would return tombstones", tombstonePrefix, metadataPrefix)
	}
	if strings.HasPrefix(metadataPrefix, tombstonePrefix) {
		t.Errorf("metadata prefix %q is inside the tombstone prefix %q",
			metadataPrefix, tombstonePrefix)
	}
	if !strings.HasPrefix(tombstonePrefix, kvstore.ReservedPrefixSystem) {
		t.Errorf("tombstone prefix %q must live under the reserved namespace %q "+
			"so the records are hidden from user-facing list and scan",
			tombstonePrefix, kvstore.ReservedPrefixSystem)
	}
}

// TestTombstone_DeletedTenantIsNotListed asserts the same property
// behaviourally, through the real ListTenants over the real store.
func TestTombstone_DeletedTenantIsNotListed(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	for _, id := range []string{"acme", "globex"} {
		if _, err := svc.CreateTenant(ctx, id, id, IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(tenants) != 1 || tenants[0].ID != "globex" {
		t.Errorf("ListTenants = %+v, want only globex", tenants)
	}
}

// TestTombstone_RecordsWhatTheTenantWas checks the fields an operator reads
// off a refusal come from the tenant rather than being invented.
func TestTombstone_RecordsWhatTheTenantWas(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, store)
	ctx := context.Background()

	created, err := svc.CreateTenant(ctx, "acme", "Acme Corp", IsolationPrefix)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	raw, err := store.Get(ctx, tombstoneKey("acme"))
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	var ts Tombstone
	if err := json.Unmarshal(raw, &ts); err != nil {
		t.Fatalf("decode tombstone: %v", err)
	}
	if ts.ID != "acme" || ts.Name != "Acme Corp" || ts.IsolationType != IsolationPrefix {
		t.Errorf("tombstone = %+v, want the deleted tenant's id, name and isolation", ts)
	}
	if !ts.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("tombstone CreatedAt = %v, want %v", ts.CreatedAt, created.CreatedAt)
	}
	if ts.DeletedAt.IsZero() {
		t.Error("tombstone must record when the id was retired")
	}
}

// TestTombstone_CorruptRecordStillRetiresTheId is the conservative reading: a
// record that will not decode still means the id was used and deleted, and
// releasing it because of a decode failure would be the one outcome the
// tombstone exists to prevent.
func TestTombstone_CorruptRecordStillRetiresTheId(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, store)
	ctx := context.Background()

	if err := store.Put(ctx, tombstoneKey("acme"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var deleted *TenantDeletedError
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); !errors.As(err, &deleted) {
		t.Fatalf("CreateTenant = %v, want a TenantDeletedError", err)
	}
	if !deleted.DeletedAt.IsZero() {
		t.Error("a corrupt tombstone has no timestamp to report")
	}
	if !strings.Contains(deleted.Error(), "cannot be reused") {
		t.Errorf("refusal message %q does not say the id cannot be reused", deleted.Error())
	}
}

// TestTombstone_UnreadableStoreRefusesTheCreate: creating a tenant is the
// operation whose whole safety property is that the id was never used, so an
// unanswerable "was it?" must not be read as "no".
func TestTombstone_UnreadableStoreRefusesTheCreate(t *testing.T) {
	store := &failingBackend{inner: seal.NewMemoryBackend(), failGet: true}
	svc, _ := newTestServiceWithStore(t, store)

	var se *StoreError
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", IsolationPrefix); !errors.As(err, &se) {
		t.Fatalf("CreateTenant over an unreadable store = %v, want a StoreError", err)
	}
	if se.Op != "tombstone-check" {
		t.Errorf("StoreError.Op = %q, want tombstone-check", se.Op)
	}
}

// TestDeleteTenant_RefusesWhileDatabasesRemain covers the bottom-up refusal,
// including that the error names what to drop.
func TestDeleteTenant_RefusesWhileDatabasesRemain(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetDatabaseLister(&listingLister{names: []string{"ledger", "reports"}})

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	var has *TenantHasDatabasesError
	err := svc.DeleteTenant(ctx, "acme")
	if !errors.As(err, &has) {
		t.Fatalf("DeleteTenant = %v, want a TenantHasDatabasesError", err)
	}
	if len(has.Databases) != 2 {
		t.Errorf("refusal names %v, want both databases", has.Databases)
	}
	for _, name := range has.Databases {
		if !strings.Contains(has.Error(), name) {
			t.Errorf("refusal message %q does not name %q", has.Error(), name)
		}
	}

	// Nothing changed: the tenant is still there and the id is not retired.
	if _, err := svc.GetTenant(ctx, "acme"); err != nil {
		t.Errorf("a refused delete must leave the tenant: %v", err)
	}
	if _, tombstoned, _ := svc.deletedAt(ctx, "acme"); tombstoned {
		t.Error("a refused delete must not retire the id")
	}
}

// TestDeleteTenant_UnlistableDatabasesAbortTheDelete: "I could not find out"
// is not the same answer as "none".
func TestDeleteTenant_UnlistableDatabasesAbortTheDelete(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	svc.SetDatabaseLister(&listingLister{err: errStoreFault})

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	var le *DatabaseListError
	if err := svc.DeleteTenant(ctx, "acme"); !errors.As(err, &le) {
		t.Fatalf("DeleteTenant = %v, want a DatabaseListError", err)
	}
	if _, err := svc.GetTenant(ctx, "acme"); err != nil {
		t.Errorf("an aborted delete must leave the tenant: %v", err)
	}
}

// TestDeleteTenant_ShredsTheDEKOnlyAfterTheFence pins the ordering the ruling
// chose: the shred is the first step PAST the fence, so a shred failure finds
// the id already retired and the metadata already gone, and it says so.
//
// The inverse -- shred first -- is what go-qrdb#483 reports: a transient store
// failure after a successful shred left a live, listed, resolvable tenant
// whose data was permanently undecryptable, under an error that read as
// "nothing changed".
func TestDeleteTenant_ShredsTheDEKOnlyAfterTheFence(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	shred := &shredRecorder{err: errStoreFault}
	reclaim := &recordingReclaimer{}
	svc.SetDEKShredder(shred)
	svc.SetKeyspaceReclaimer(reclaim)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	err := svc.DeleteTenant(ctx, "acme")
	var de *DEKShredError
	if !errors.As(err, &de) {
		t.Fatalf("DeleteTenant = %v, want a DEKShredError", err)
	}
	var te *TeardownError
	if !errors.As(err, &te) {
		t.Fatalf("DeleteTenant = %v, want the shred failure wrapped in a TeardownError", err)
	}
	if _, gErr := svc.GetTenant(ctx, "acme"); !errors.Is(gErr, ErrTenantNotFound) {
		t.Errorf("the fence must have removed the metadata before the shred ran, got %v", gErr)
	}
	if _, tombstoned, _ := svc.deletedAt(ctx, "acme"); !tombstoned {
		t.Error("the fence must have retired the id before the shred ran")
	}
	if len(reclaim.calls) != 0 {
		t.Errorf("the reclamation ran after a failed shred: %v", reclaim.calls)
	}
}

// TestDeleteTenant_AFailedFenceLeavesTheTenantIntact is the property the
// reordering exists to provide, asserted on each half of the fence in turn.
//
// Neither fence write destroys anything, so a failure in either must leave a
// tenant that is still resolvable AND a DEK that was never touched -- which is
// what makes "put-tombstone failed" readable as "nothing changed". Under the
// shred-first ordering this test fails on the DEK: the key was already gone.
func TestDeleteTenant_AFailedFenceLeavesTheTenantIntact(t *testing.T) {
	tests := []struct {
		name   string
		fail   func(*failingBackend)
		wantOp string
	}{
		{name: "tombstone write fails", fail: func(f *failingBackend) { f.failPut = true }, wantOp: "put-tombstone"},
		{name: "metadata delete fails", fail: func(f *failingBackend) { f.failDel = true }, wantOp: "delete"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			backend := &failingBackend{inner: seal.NewMemoryBackend()}
			svc, _ := newTestServiceWithStore(t, backend)
			shred := &shredRecorder{}
			reclaim := &recordingReclaimer{}
			svc.SetDEKShredder(shred)
			svc.SetKeyspaceReclaimer(reclaim)

			if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
				t.Fatalf("CreateTenant: %v", err)
			}

			tc.fail(backend)
			err := svc.DeleteTenant(ctx, "acme")
			var se *StoreError
			if !errors.As(err, &se) {
				t.Fatalf("DeleteTenant = %v, want a StoreError", err)
			}
			if se.Op != tc.wantOp {
				t.Errorf("StoreError.Op = %q, want %q", se.Op, tc.wantOp)
			}

			// THE assertion: the irreversible step did not run.
			if len(shred.calls) != 0 {
				t.Errorf("the DEK was shredded by a delete that failed at the fence: %v", shred.calls)
			}
			if len(reclaim.calls) != 0 {
				t.Errorf("the keyspace was reclaimed by a delete that failed at the fence: %v", reclaim.calls)
			}

			// And the error does not claim a retirement that did not stick.
			var te *TeardownError
			if errors.As(err, &te) {
				t.Errorf("a fence failure must not report itself as a teardown past the fence: %v", err)
			}

			// The tenant is still there once the store recovers.
			backend.failPut, backend.failDel = false, false
			if _, gErr := svc.GetTenant(ctx, "acme"); gErr != nil {
				t.Errorf("a failed fence must leave the tenant: %v", gErr)
			}
		})
	}
}

// TestDeleteTenant_ShredRunsForEveryIsolationType: a prefix tenant may never
// have had a DEK, but the shred is issued anyway and is expected to treat
// that as success -- the alternative is a teardown that depends on knowing
// which tenants were ever handed a key.
func TestDeleteTenant_ShredRunsForEveryIsolationType(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	shred := &shredRecorder{}
	svc.SetDEKShredder(shred)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if len(shred.calls) != 1 || shred.calls[0] != "acme" {
		t.Errorf("shred calls = %v, want one for acme", shred.calls)
	}
}

// TestDeleteTenant_ReclaimFailureLeavesTheIdRetired is the asymmetry the
// ordering buys: a reclamation failure surfaces AFTER the tombstone commits,
// so the security half has already taken effect.
func TestDeleteTenant_ReclaimFailureLeavesTheIdRetired(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	reclaim := &recordingReclaimer{err: errStoreFault}
	svc.SetKeyspaceReclaimer(reclaim)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	var ke *KeyspaceReclaimError
	if err := svc.DeleteTenant(ctx, "acme"); !errors.As(err, &ke) {
		t.Fatalf("DeleteTenant = %v, want a KeyspaceReclaimError", err)
	}
	var deleted *TenantDeletedError
	if _, err := svc.CreateTenant(ctx, "acme", "Acme Two", IsolationPrefix); !errors.As(err, &deleted) {
		t.Fatalf("the id must be retired despite the reclaim failure, got %v", err)
	}

	// Re-issuing resumes the reclamation rather than reporting not-found.
	reclaim.err = nil
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("re-issued DeleteTenant: %v", err)
	}
	if len(reclaim.calls) != 2 {
		t.Errorf("reclaim calls = %v, want the retry to have run it again", reclaim.calls)
	}
}

// TestDeleteTenant_TombstoneSurvivesAFreshService is the durability claim:
// the record lives in the metadata store, so a service rebuilt over the same
// store still refuses the id. In a cluster that store is the replicated
// system shard, which is what makes the refusal hold on every node.
func TestDeleteTenant_TombstoneSurvivesAFreshService(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, store)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}

	restarted, _ := newTestServiceWithStore(t, store)
	var deleted *TenantDeletedError
	if _, err := restarted.CreateTenant(ctx, "acme", "Acme Two", IsolationPrefix); !errors.As(err, &deleted) {
		t.Fatalf("a restarted service must still refuse the id, got %v", err)
	}
}

// TestDeletedAt_ReportsTheRetirement covers the read side used by the store
// resolver, on both services.
func TestDeletedAt_ReportsTheRetirement(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, _, err := svc.deletedAt(ctx, ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("deletedAt(\"\") = %v, want ErrEmptyTenantID", err)
	}
	if _, tombstoned, err := svc.deletedAt(ctx, "never-used"); err != nil || tombstoned {
		t.Errorf("deletedAt(unused) = (%v, %v), want (false, nil)", tombstoned, err)
	}

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	before := time.Now().UTC().Add(-time.Second)
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	at, tombstoned, err := svc.deletedAt(ctx, "acme")
	if err != nil || !tombstoned {
		t.Fatalf("deletedAt(deleted) = (%v, %v), want (true, nil)", tombstoned, err)
	}
	if at.Before(before) {
		t.Errorf("deletedAt = %v, want at or after %v", at, before)
	}
}

// TestKVTenantService_DeletedIdIsRetired runs the same sequence on the
// seal-disabled service. Keyspace isolation is not a property of encryption:
// a node with the seal subsystem off still has prefix tenants, and they are
// exactly the tenants whose data stays readable after a delete.
func TestKVTenantService_DeletedIdIsRetired(t *testing.T) {
	ctx := context.Background()
	svcI, err := NewKVTenantService(kvstore.NewMemoryKVStore(), testLogger())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	wiring, ok := svcI.(TeardownWiring)
	if !ok {
		t.Fatal("the KV tenant service must carry the teardown seams")
	}
	reclaim := &recordingReclaimer{}
	wiring.SetKeyspaceReclaimer(reclaim)

	if _, err := svcI.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := svcI.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if len(reclaim.calls) != 1 {
		t.Errorf("reclaim calls = %v, want one", reclaim.calls)
	}

	var deleted *TenantDeletedError
	if _, err := svcI.CreateTenant(ctx, "acme", "Acme Two", IsolationPrefix); !errors.As(err, &deleted) {
		t.Fatalf("re-creating a deleted id must be refused, got %v", err)
	}

	// And it is idempotent for the same reason the barrier-backed one is.
	if err := svcI.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("re-issued DeleteTenant: %v", err)
	}
	if err := svcI.DeleteTenant(ctx, "never-existed"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("DeleteTenant on an unknown id = %v, want ErrTenantNotFound", err)
	}
}

// TestKVTenantService_RefusesWhileDatabasesRemain: the bottom-up refusal is
// not gated on encryption either.
func TestKVTenantService_RefusesWhileDatabasesRemain(t *testing.T) {
	ctx := context.Background()
	svcI, err := NewKVTenantService(kvstore.NewMemoryKVStore(), testLogger())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	svcI.(TeardownWiring).SetDatabaseLister(&listingLister{names: []string{"ledger"}}) //nolint:errcheck // asserted in the sibling test

	if _, err := svcI.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	var has *TenantHasDatabasesError
	if err := svcI.DeleteTenant(ctx, "acme"); !errors.As(err, &has) {
		t.Fatalf("DeleteTenant = %v, want a TenantHasDatabasesError", err)
	}
}

// TestTeardownOptions_WireTheSameSeamsAsTheSetters: the With* constructor
// options and the TeardownWiring setters are two spellings of one wiring, and
// a divergence would leave a composition that used the option with no
// teardown at all.
func TestTeardownOptions_WireTheSameSeamsAsTheSetters(t *testing.T) {
	reclaim := &recordingReclaimer{}
	lister := &listingLister{names: []string{"ledger"}}
	shred := &shredRecorder{}

	svc, err := NewTenantService(seal.NewMemoryBackend(), newTestBarrierRegistry(t), testLogger(),
		WithDatabaseLister(lister),
		WithDEKShredder(shred),
		WithKeyspaceReclaimer(reclaim),
	)
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	if svc.KeyspaceReclaimer() != reclaim {
		t.Error("WithKeyspaceReclaimer did not install the reclaimer")
	}

	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// The lister is live: the delete is refused for the database it names.
	var has *TenantHasDatabasesError
	if err := svc.DeleteTenant(ctx, "acme"); !errors.As(err, &has) {
		t.Fatalf("DeleteTenant = %v, want a TenantHasDatabasesError", err)
	}
	// And so are the other two, once the databases are gone.
	lister.names = nil
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if len(shred.calls) != 1 || len(reclaim.calls) != 1 {
		t.Errorf("shred=%v reclaim=%v, want one call each", shred.calls, reclaim.calls)
	}
}

// TestTeardown_NoSeamsWiredStillRetiresTheId states the fallback contract:
// the tombstone is the security half and does not depend on any seam.
func TestTeardown_NoSeamsWiredStillRetiresTheId(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if svc.KeyspaceReclaimer() != nil {
		t.Fatal("the fixture wired a reclaimer; this test is about none being wired")
	}
	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	var deleted *TenantDeletedError
	if _, err := svc.CreateTenant(ctx, "acme", "Acme Two", IsolationPrefix); !errors.As(err, &deleted) {
		t.Fatalf("the id must be retired with no seams wired, got %v", err)
	}
}

// TestDeletedAt_StoreFailureIsReported: the read side must not report "not
// deleted" when it could not read, because the store resolver falls through
// on "not deleted".
func TestDeletedAt_StoreFailureIsReported(t *testing.T) {
	backend := &failingBackend{inner: seal.NewMemoryBackend()}
	svc, _ := newTestServiceWithStore(t, backend)

	backend.failGet = true
	var se *StoreError
	if _, _, err := svc.deletedAt(context.Background(), "acme"); !errors.As(err, &se) {
		t.Fatalf("deletedAt over an unreadable store = %v, want a StoreError", err)
	}
	if se.Op != "tombstone-get" {
		t.Errorf("StoreError.Op = %q, want tombstone-get", se.Op)
	}
}

// TestDeletedAt_CorruptRecordReportsExistence: the record exists even when it
// does not decode, and existence is what the refusal turns on.
func TestDeletedAt_CorruptRecordReportsExistence(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, store)
	ctx := context.Background()

	if err := store.Put(ctx, tombstoneKey("acme"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	at, tombstoned, err := svc.deletedAt(ctx, "acme")
	if err != nil || !tombstoned {
		t.Fatalf("deletedAt = (%v, %v), want (true, nil)", tombstoned, err)
	}
	if !at.IsZero() {
		t.Errorf("deletedAt = %v, want the zero time for an undecodable record", at)
	}
}

// TestKVTenantService_DeletedAtPaths covers the same two edges on the
// seal-disabled service, whose store reports an absent key as (nil, nil).
func TestKVTenantService_DeletedAtPaths(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	svcI, err := NewKVTenantService(store, testLogger())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	svc, ok := svcI.(*kvTenantService)
	if !ok {
		t.Fatal("NewKVTenantService returned an unexpected type")
	}

	if _, _, err := svc.deletedAt(ctx, ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("deletedAt(\"\") = %v, want ErrEmptyTenantID", err)
	}
	if _, tombstoned, err := svc.deletedAt(ctx, "never-used"); err != nil || tombstoned {
		t.Errorf("deletedAt(unused) = (%v, %v), want (false, nil)", tombstoned, err)
	}
	if err := store.Put(ctx, tombstoneKey("acme"), []byte("not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, tombstoned, err := svc.deletedAt(ctx, "acme"); err != nil || !tombstoned {
		t.Errorf("deletedAt(corrupt) = (%v, %v), want (true, nil)", tombstoned, err)
	}
}

// TestStoreResolver_TombstoneReadFailureFailsClosed: the resolver's
// fall-through is what a deleted id must not get, so an unanswerable question
// fails the resolution rather than taking it.
func TestStoreResolver_TombstoneReadFailureFailsClosed(t *testing.T) {
	backend := &failingBackend{inner: seal.NewMemoryBackend()}
	svc, _ := newTestServiceWithStore(t, backend)
	resolver, err := NewStoreResolver(kvstore.NewMemoryKVStore(), nil, svc)
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}

	backend.failGet = true
	if _, err := resolver.Resolve(context.Background(), "acme"); err == nil {
		t.Error("Resolve must fail when the tombstone cannot be read")
	}
}

// TestStoreResolver_ForeignTenantServicerSkipsTheTombstoneCheck states what a
// composition supplying its own TenantServicer gets: metadata-only
// resolution, relying on CreateTenant's refusal alone. The tombstone method
// is unexported, so a foreign implementation cannot satisfy it.
func TestStoreResolver_ForeignTenantServicerSkipsTheTombstoneCheck(t *testing.T) {
	resolver, err := NewStoreResolver(kvstore.NewMemoryKVStore(), nil, foreignTenantServicer{})
	if err != nil {
		t.Fatalf("NewStoreResolver: %v", err)
	}
	if _, err := resolver.Resolve(context.Background(), "acme"); err != nil {
		t.Errorf("Resolve with a foreign servicer = %v, want the shared-store fall-through", err)
	}
}

// foreignTenantServicer is a TenantServicer implemented OUTSIDE this package's
// two services -- the shape a composition supplies when it has its own tenant
// registry. It cannot satisfy tombstoneLookup, whose method is unexported, and
// the resolver's documented behaviour for it is the subject of the test above.
//
// Every method other than GetTenant is unreachable from that test; they exist
// because the interface requires them.
type foreignTenantServicer struct{}

func (foreignTenantServicer) CreateTenant(context.Context, string, string, IsolationType, ...CreateTenantOption) (*TenantInfo, error) {
	return nil, ErrTenantNotFound
}
func (foreignTenantServicer) UpdateTenant(context.Context, string, TenantPatch) (*TenantInfo, error) {
	return nil, ErrTenantNotFound
}
func (foreignTenantServicer) GetTenant(context.Context, string) (*TenantInfo, error) {
	return nil, ErrTenantNotFound
}
func (foreignTenantServicer) ListTenants(context.Context) ([]TenantInfo, error) { return nil, nil }
func (foreignTenantServicer) DeleteTenant(context.Context, string) error        { return ErrTenantNotFound }
func (foreignTenantServicer) UpdateQuotas(context.Context, string, *quota.Quotas) (*TenantInfo, error) {
	return nil, ErrTenantNotFound
}
func (foreignTenantServicer) UpdateEntitlements(context.Context, string, *quota.Entitlements) (*TenantInfo, error) {
	return nil, ErrTenantNotFound
}
func (foreignTenantServicer) CheckIsolation(context.Context, string, string) error { return nil }
func (foreignTenantServicer) CheckFeature(context.Context, string, string) error   { return nil }
func (foreignTenantServicer) CheckBarrierMode(context.Context, string, seal.StrategyID) error {
	return nil
}
func (foreignTenantServicer) IncrementQuotaUsage(context.Context, string, quota.Resource, int64) (*TenantInfo, error) {
	return nil, ErrTenantNotFound
}
func (foreignTenantServicer) DecrementQuotaUsage(context.Context, string, quota.Resource, int64) (*TenantInfo, error) {
	return nil, ErrTenantNotFound
}
func (foreignTenantServicer) SetQuotaUsage(context.Context, string, quota.Resource, int64) (*TenantInfo, error) {
	return nil, ErrTenantNotFound
}
func (foreignTenantServicer) GetQuotaUsage(context.Context, string, quota.Resource) (int64, error) {
	return 0, nil
}
func (foreignTenantServicer) TenantBarrierInit(context.Context, string, []byte, int, int) (*TenantBarrierInitResult, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierInitWithStrategy(context.Context, string, []byte, int, int, seal.StrategyID) (*TenantBarrierInitResult, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierUnseal(context.Context, string, []byte) error {
	return ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierUnsealShare(context.Context, string, string) (*TenantBarrierUnsealProgress, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierSeal(context.Context, string) error {
	return ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierStatus(context.Context, string) (*TenantBarrierStatus, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierOfficerRegister(context.Context, string, *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierOfficerList(context.Context, string) ([]*ceremony.OfficerStatus, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierShareClaimBegin(context.Context, string, string, string) (*ceremony.ChallengeResponse, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierShareClaimComplete(context.Context, string, string, string, []byte, string) (*ceremony.ShareClaimResult, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierOfficerClaimStatus(context.Context, string) ([]*ceremony.OfficerStatus, error) {
	return nil, ErrBarrierNotEnabled
}
func (foreignTenantServicer) TenantBarrierShamirSealVote(context.Context, string, string) (*TenantBarrierUnsealProgress, error) {
	return nil, ErrBarrierNotEnabled
}

// Compile-time check: it really is a TenantServicer, and really is not a
// tombstoneLookup.
var _ TenantServicer = foreignTenantServicer{}

// TestKVTenantService_DeleteFailureModes covers the no-seal service's error
// paths: a corrupt metadata record still retires the id, a shred failure
// aborts with nothing changed, and a reclaim failure leaves the id retired.
func TestKVTenantService_DeleteFailureModes(t *testing.T) {
	ctx := context.Background()

	t.Run("corrupt metadata still retires the id", func(t *testing.T) {
		store := kvstore.NewMemoryKVStore()
		svcI, err := NewKVTenantService(store, testLogger())
		if err != nil {
			t.Fatalf("NewKVTenantService: %v", err)
		}
		if err := store.Put(ctx, metadataPrefix+"acme", []byte("not json")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := svcI.DeleteTenant(ctx, "acme"); err != nil {
			t.Fatalf("DeleteTenant: %v", err)
		}
		var deleted *TenantDeletedError
		if _, err := svcI.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); !errors.As(err, &deleted) {
			t.Fatalf("the id must be retired, got %v", err)
		}
	})

	t.Run("shred failure finds the id already retired", func(t *testing.T) {
		store := kvstore.NewMemoryKVStore()
		svcI, err := NewKVTenantService(store, testLogger())
		if err != nil {
			t.Fatalf("NewKVTenantService: %v", err)
		}
		shred := &shredRecorder{err: errStoreFault}
		svcI.(TeardownWiring).SetDEKShredder(shred) //nolint:errcheck // asserted in TestKVTenantService_DeletedIdIsRetired
		if _, err := svcI.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant: %v", err)
		}
		dErr := svcI.DeleteTenant(ctx, "acme")
		var de *DEKShredError
		if !errors.As(dErr, &de) {
			t.Fatalf("DeleteTenant = %v, want a DEKShredError", dErr)
		}
		var te *TeardownError
		if !errors.As(dErr, &te) {
			t.Fatalf("DeleteTenant = %v, want the shred failure wrapped in a TeardownError", dErr)
		}
		if _, gErr := svcI.GetTenant(ctx, "acme"); !errors.Is(gErr, ErrTenantNotFound) {
			t.Errorf("the no-seal service must fence before it shreds too, got %v", gErr)
		}

		// Re-issuing resumes and completes once the shredder recovers -- the
		// resume path reaches the shred, which is now past the fence.
		shred.err = nil
		if rErr := svcI.DeleteTenant(ctx, "acme"); rErr != nil {
			t.Fatalf("re-issued DeleteTenant: %v", rErr)
		}
		if len(shred.calls) != 2 {
			t.Errorf("shred calls = %v, want the resume to have re-run it", shred.calls)
		}
	})

	t.Run("reclaim failure leaves the id retired", func(t *testing.T) {
		store := kvstore.NewMemoryKVStore()
		svcI, err := NewKVTenantService(store, testLogger())
		if err != nil {
			t.Fatalf("NewKVTenantService: %v", err)
		}
		reclaim := &recordingReclaimer{err: errStoreFault}
		svcI.(TeardownWiring).SetKeyspaceReclaimer(reclaim) //nolint:errcheck // asserted in TestKVTenantService_DeletedIdIsRetired
		if _, err := svcI.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant: %v", err)
		}
		var ke *KeyspaceReclaimError
		if err := svcI.DeleteTenant(ctx, "acme"); !errors.As(err, &ke) {
			t.Fatalf("DeleteTenant = %v, want a KeyspaceReclaimError", err)
		}
		var deleted *TenantDeletedError
		if _, err := svcI.CreateTenant(ctx, "acme", "Acme Two", IsolationPrefix); !errors.As(err, &deleted) {
			t.Fatalf("the id must be retired despite the reclaim failure, got %v", err)
		}
		// Re-issuing resumes it.
		reclaim.err = nil
		if err := svcI.DeleteTenant(ctx, "acme"); err != nil {
			t.Fatalf("re-issued DeleteTenant: %v", err)
		}
		if len(reclaim.calls) != 2 {
			t.Errorf("reclaim calls = %v, want the retry to have run it again", reclaim.calls)
		}
	})
}

// TestDeleteTenant_ResumeReachesTheBarrierDestroy pins the sequencing of a
// re-issued delete.
//
// A teardown that committed the tombstone and the metadata delete and then
// failed at the barrier destroy leaves a tenant with no metadata, a retired
// id and a live barrier. Re-issuing must reach the destroy -- a resume that
// only re-ran the reclamation could never finish, because the destroy is
// precisely the step whose failure produced that state.
//
// The state is reached by editing the store directly rather than by injecting
// a failure, because the barrier registry is a concrete type with no seam to
// fail at. What the destroy DOES once it is reached is asserted against a
// real pooled registry in pkg/server; this asserts that it is reached.
func TestDeleteTenant_ResumeReachesTheBarrierDestroy(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, reg := newTestServiceWithStore(t, store)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := reg.GetTenant("acme"); err != nil {
		t.Fatalf("the fixture did not register a barrier: %v", err)
	}

	// Simulate a teardown that committed the tombstone and the metadata
	// delete and then failed at the barrier destroy.
	tombstone, err := json.Marshal(newTombstone(&TenantInfo{ID: "acme"}, time.Now().UTC()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Put(ctx, tombstoneKey("acme"), tombstone); err != nil {
		t.Fatalf("Put tombstone: %v", err)
	}
	if err := store.Delete(ctx, metadataPrefix+"acme"); err != nil {
		t.Fatalf("Delete metadata: %v", err)
	}

	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("re-issued DeleteTenant: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Errorf("the resume did not destroy the barrier: %v", err)
	}
}

// ---------------------------------------------------------------------------
// A reserved id is not deletable (go-qrdb#413 review)
//
// The reserved-id rule was applied only on CREATE, so the ids this package
// declares un-creatable were freely DELETABLE: DeleteTenant ran the
// irreversible teardown against a caller-supplied "system" and reported
// success. The reclamation already refused the system tenant, but that guards
// the LAST step; the three irreversible control-plane mutations precede it.
//
// Every test below asserts what was NOT committed, not merely what was
// returned. An assertion on the error alone would pass even if the DEK had
// already been shredded.
// ---------------------------------------------------------------------------

// reservedIDs is the full set this package declares, one id per form, so a
// guard that catches "system" and misses the prefixes fails here.
var reservedIDs = []struct {
	name string
	id   string
}{
	{"system tenant", SystemTenantID},
	{"legacy default", legacyDefaultTenantID},
	{"underscore prefix", "_internal"},
	{"colon compound", "acme:eu"},
}

// assertNothingCommitted fails when any step of the teardown left a trace.
func assertNothingCommitted(
	t *testing.T,
	ctx context.Context,
	store seal.StorageBackend,
	id string,
	shred *shredRecorder,
	reclaim *recordingReclaimer,
) {
	t.Helper()
	if _, err := store.Get(ctx, tombstoneKey(id)); !errors.Is(err, seal.ErrSecretNotFound) {
		t.Errorf("a refused delete of %q wrote a tombstone (get err %v)", id, err)
	}
	if _, err := store.Get(ctx, metadataPrefix+id); err != nil {
		t.Errorf("a refused delete of %q removed the metadata record: %v", id, err)
	}
	if len(shred.calls) != 0 {
		t.Errorf("a refused delete of %q shredded a DEK: %v", id, shred.calls)
	}
	if len(reclaim.calls) != 0 {
		t.Errorf("a refused delete of %q ran the reclamation: %v", id, reclaim.calls)
	}
}

// TestDeleteTenant_ReservedIdsAreRefusedWithNothingCommitted covers the whole
// reserved set on the barrier-backed service, under two wirings.
//
// The "no database lister" subtest is the configuration the bottom-up refusal
// CANNOT fire in -- refuseIfDatabasesRemain returns nil when no lister is
// wired, which is what BuildSealServicesOnly produces -- so it is the one that
// proves the new guard does not lean on the old one. The "permissive lister"
// subtest reports zero databases, which is the other way the old refusal lets
// a call through.
func TestDeleteTenant_ReservedIdsAreRefusedWithNothingCommitted(t *testing.T) {
	for _, wiring := range []struct {
		name   string
		lister DatabaseLister
	}{
		{"no database lister", nil},
		{"permissive database lister", &listingLister{}},
	} {
		t.Run(wiring.name, func(t *testing.T) {
			for _, tc := range reservedIDs {
				t.Run(tc.name, func(t *testing.T) {
					ctx := context.Background()
					store := seal.NewMemoryBackend()
					svc, _ := newTestServiceWithStore(t, store)
					shred := &shredRecorder{}
					reclaim := &recordingReclaimer{}
					svc.SetDEKShredder(shred)
					svc.SetKeyspaceReclaimer(reclaim)
					if wiring.lister != nil {
						svc.SetDatabaseLister(wiring.lister)
					}
					if svc.databases != nil && wiring.lister == nil {
						t.Fatal("the fixture wired a lister; this subtest is about none being wired")
					}

					// Seed a metadata record so "the record survived" is a real
					// assertion rather than a vacuous one. Reserved ids cannot be
					// created through CreateTenant, which is the whole point.
					seedTenantRecord(t, ctx, store, tc.id)

					var re *ReservedTenantIDError
					if err := svc.DeleteTenant(ctx, tc.id); !errors.As(err, &re) {
						t.Fatalf("DeleteTenant(%q) = %v, want a ReservedTenantIDError", tc.id, err)
					}
					if re.TenantID != tc.id {
						t.Errorf("refusal names %q, want %q", re.TenantID, tc.id)
					}
					assertNothingCommitted(t, ctx, store, tc.id, shred, reclaim)
				})
			}
		})
	}
}

// TestDeleteTenant_ReservedGuardDoesNotRefuseOrdinaryIds is the other half:
// the guard must not have made every delete a refusal.
func TestDeleteTenant_ReservedGuardDoesNotRefuseOrdinaryIds(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, store)
	shred := &shredRecorder{}
	svc.SetDEKShredder(shred)

	if _, err := svc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if err := svc.DeleteTenant(ctx, "acme"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
	if len(shred.calls) != 1 {
		t.Errorf("shred calls = %v, want one for acme", shred.calls)
	}
}

// TestDeleteTenant_MalformedIdsAreRefusedBeforeAnyCommit: the shape check
// binds the delete path too, so an id that would write a tombstone outside
// its own key is refused rather than reaching the store.
func TestDeleteTenant_MalformedIdsAreRefusedBeforeAnyCommit(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, store)

	var ce *InvalidTenantIDCharError
	if err := svc.DeleteTenant(ctx, "acme/evil"); !errors.As(err, &ce) {
		t.Fatalf("DeleteTenant on a path-bearing id = %v, want an InvalidTenantIDCharError", err)
	}
	if _, err := store.Get(ctx, tombstoneKey("acme/evil")); !errors.Is(err, seal.ErrSecretNotFound) {
		t.Errorf("a refused delete wrote a tombstone (get err %v)", err)
	}

	var ie *InvalidTenantIDError
	if err := svc.DeleteTenant(ctx, ".."); !errors.As(err, &ie) {
		t.Errorf("DeleteTenant(\"..\") = %v, want an InvalidTenantIDError", err)
	}
}

// TestKVTenantService_ReservedIdsAreRefusedWithNothingCommitted is the same
// table on the no-seal service, which has neither a barrier nor (by default) a
// reclaimer to refuse on its behalf -- so before the guard, "system" was the
// easiest tenant in such a deployment to destroy.
func TestKVTenantService_ReservedIdsAreRefusedWithNothingCommitted(t *testing.T) {
	for _, tc := range reservedIDs {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := kvstore.NewMemoryKVStore()
			svcI, err := NewKVTenantService(store, testLogger())
			if err != nil {
				t.Fatalf("NewKVTenantService: %v", err)
			}
			shred := &shredRecorder{}
			reclaim := &recordingReclaimer{}
			wiring, ok := svcI.(TeardownWiring)
			if !ok {
				t.Fatal("the KV tenant service must carry the teardown seams")
			}
			wiring.SetDEKShredder(shred)
			wiring.SetKeyspaceReclaimer(reclaim)
			// No DatabaseLister: the bottom-up refusal cannot fire here.

			if err := store.Put(ctx, metadataPrefix+tc.id, []byte(`{"id":"`+tc.id+`"}`)); err != nil {
				t.Fatalf("seed metadata: %v", err)
			}

			var re *ReservedTenantIDError
			if err := svcI.DeleteTenant(ctx, tc.id); !errors.As(err, &re) {
				t.Fatalf("DeleteTenant(%q) = %v, want a ReservedTenantIDError", tc.id, err)
			}
			if data, gErr := store.Get(ctx, tombstoneKey(tc.id)); gErr != nil || data != nil {
				t.Errorf("a refused delete of %q wrote a tombstone (%v, %v)", tc.id, data, gErr)
			}
			if data, gErr := store.Get(ctx, metadataPrefix+tc.id); gErr != nil || data == nil {
				t.Errorf("a refused delete of %q removed the metadata record (%v, %v)", tc.id, data, gErr)
			}
			if len(shred.calls) != 0 || len(reclaim.calls) != 0 {
				t.Errorf("a refused delete of %q ran shred=%v reclaim=%v", tc.id, shred.calls, reclaim.calls)
			}
		})
	}
}

// TestReservedTenantIDs_CreateAndDeleteShareOnePredicate is the anti-drift
// check. The reserved SET is declared once; creation and deletion are two
// call sites with one documented difference, the first-boot bypass. This
// asserts both halves: the same ids are refused by both, and the bypass
// applies to creation only.
func TestReservedTenantIDs_CreateAndDeleteShareOnePredicate(t *testing.T) {
	for _, tc := range reservedIDs {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			var createErr, deleteErr *ReservedTenantIDError
			if !errors.As(validateReservedTenantID(ctx, tc.id), &createErr) {
				t.Fatalf("create must refuse %q", tc.id)
			}
			if !errors.As(validateDeletableTenantID(tc.id), &deleteErr) {
				t.Fatalf("delete must refuse %q", tc.id)
			}
			if createErr.Reason != deleteErr.Reason {
				t.Errorf("create says %q, delete says %q; the set has drifted",
					createErr.Reason, deleteErr.Reason)
			}

			// The ONE documented difference: bootstrap may create a reserved
			// tenant, and nothing may delete one.
			internal := WithInternalCall(ctx)
			if err := validateReservedTenantID(internal, tc.id); err != nil {
				t.Errorf("the first-boot bypass must let create through: %v", err)
			}
			if err := validateDeletableTenantID(tc.id); err == nil {
				t.Error("the delete path must have no bypass")
			}
		})
	}
}

// TestReservedTenantIDs_OrdinaryIdsPassBothGuards keeps the set from growing
// by accident.
func TestReservedTenantIDs_OrdinaryIdsPassBothGuards(t *testing.T) {
	ctx := context.Background()
	for _, id := range []string{"acme", "globex", "a", "tenant-with-dashes", "under_score_inside"} {
		if err := validateReservedTenantID(ctx, id); err != nil {
			t.Errorf("create refused the ordinary id %q: %v", id, err)
		}
		if err := validateDeletableTenantID(id); err != nil {
			t.Errorf("delete refused the ordinary id %q: %v", id, err)
		}
	}
}

// seedTenantRecord writes a minimal metadata record directly, for reserved
// ids that CreateTenant will not produce.
func seedTenantRecord(t *testing.T, ctx context.Context, store seal.StorageBackend, id string) {
	t.Helper()
	data, err := json.Marshal(&TenantInfo{ID: id, Name: id, IsolationType: IsolationPrefix})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Put(ctx, metadataPrefix+id, data); err != nil {
		t.Fatalf("seed metadata for %q: %v", id, err)
	}
}
