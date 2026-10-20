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
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// ListDeletedTenants
// ---------------------------------------------------------------------------

// retireIDs deletes a prefix tenant for each id through the real DeleteTenant,
// so every tombstone the listing reads was written by production code.
func retireIDs(t *testing.T, svc TenantServicer, ids ...string) {
	t.Helper()
	ctx := context.Background()
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, "Tenant "+id, IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
		if err := svc.DeleteTenant(ctx, id); err != nil {
			t.Fatalf("DeleteTenant(%s): %v", id, err)
		}
	}
}

// collectAll walks every page and returns the ids in the order listed.
func collectAll(t *testing.T, svc TenantServicer, pageSize int) (ids []string, pages int) {
	t.Helper()
	token := ""
	for {
		page, err := svc.ListDeletedTenants(context.Background(), token, pageSize)
		if err != nil {
			t.Fatalf("ListDeletedTenants: %v", err)
		}
		pages++
		for _, ts := range page.Tombstones {
			ids = append(ids, ts.ID)
		}
		if page.NextPageToken == "" {
			return ids, pages
		}
		token = page.NextPageToken
		if pages > 100 {
			t.Fatal("the listing never terminated")
		}
	}
}

func TestListDeletedTenants_EmptyWhenNothingIsRetired(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.CreateTenant(context.Background(), "live", "Live", IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	page, err := svc.ListDeletedTenants(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("ListDeletedTenants: %v", err)
	}
	if len(page.Tombstones) != 0 || page.NextPageToken != "" {
		t.Fatalf("page = %+v; want empty with no token -- a live tenant is not a tombstone", page)
	}
}

// TestListDeletedTenants_PagesInIdOrderWithDeletionRecords walks five
// retirements two at a time: every id exactly once, ascending, each carrying
// the deletion time and what the tenant was.
func TestListDeletedTenants_PagesInIdOrderWithDeletionRecords(t *testing.T) {
	svc, _ := newTestService(t)
	before := time.Now().UTC().Add(-time.Second)
	retireIDs(t, svc, "echo", "alpha", "delta", "charlie", "bravo")

	ids, pages := collectAll(t, svc, 2)
	want := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("listed %v; want %v", ids, want)
	}
	if pages != 3 {
		t.Fatalf("listed in %d pages; want 3 for five records at page size 2", pages)
	}

	page, err := svc.ListDeletedTenants(context.Background(), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	ts := page.Tombstones[0]
	if ts.ID != "alpha" || ts.Name != "Tenant alpha" || ts.IsolationType != IsolationPrefix {
		t.Fatalf("first record = %+v; want alpha with its name and isolation", ts)
	}
	if ts.DeletedAt.Before(before) {
		t.Fatalf("DeletedAt = %v; want the deletion time", ts.DeletedAt)
	}
}

func TestListDeletedTenants_PageSizeContract(t *testing.T) {
	svc, _ := newTestService(t)
	var pr *InvalidPageRequestError
	if _, err := svc.ListDeletedTenants(context.Background(), "", -1); !errors.As(err, &pr) || pr.Field != "page_size" {
		t.Fatalf("negative page size = %v; want *InvalidPageRequestError{page_size}", err)
	}
	for _, tc := range []struct{ in, want int }{
		{0, DefaultDeletedTenantsPageSize},
		{7, 7},
		{MaxDeletedTenantsPageSize + 1, MaxDeletedTenantsPageSize},
	} {
		got, err := resolvePageSize(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("resolvePageSize(%d) = %d, %v; want %d", tc.in, got, err, tc.want)
		}
	}
}

func TestListDeletedTenants_RefusesATokenItDidNotIssue(t *testing.T) {
	svc, _ := newTestService(t)
	var pr *InvalidPageRequestError
	if _, err := svc.ListDeletedTenants(context.Background(), "%%%not-base64", 10); !errors.As(err, &pr) || pr.Field != "page_token" {
		t.Fatalf("garbage token = %v; want *InvalidPageRequestError{page_token}", err)
	}
}

// TestListDeletedTenants_CorruptRecordIsStillListed: an undecodable tombstone
// still retires its id, so the listing must still show it.
func TestListDeletedTenants_CorruptRecordIsStillListed(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, store)
	if err := store.Put(context.Background(), tombstoneKey("mangled"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	page, err := svc.ListDeletedTenants(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Tombstones) != 1 || page.Tombstones[0].ID != "mangled" {
		t.Fatalf("page = %+v; want the corrupt record listed by its id", page)
	}
}

func TestListDeletedTenants_StoreFailureIsReported(t *testing.T) {
	store := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, &failingBackend{inner: store, failLst: true})
	var se *StoreError
	if _, err := svc.ListDeletedTenants(context.Background(), "", 0); !errors.As(err, &se) {
		t.Fatalf("ListDeletedTenants over an unlistable store = %v; want *StoreError", err)
	}
}

// TestKVListDeletedTenants_MatchesTheSealedService: the seal-disabled service
// answers the same question the same way.
func TestKVListDeletedTenants_MatchesTheSealedService(t *testing.T) {
	svc, err := NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	retireIDs(t, svc, "zeta", "eta", "theta")
	ids, pages := collectAll(t, svc, 2)
	if fmt.Sprint(ids) != fmt.Sprint([]string{"eta", "theta", "zeta"}) || pages != 2 {
		t.Fatalf("listed %v in %d pages; want [eta theta zeta] in 2", ids, pages)
	}
}

// ---------------------------------------------------------------------------
// ReconcileDeletedTenants
// ---------------------------------------------------------------------------

// newReconcileService builds a volume-capable service whose system barrier is
// initialized, so tenant-root records persist the way they do in production.
func newReconcileService(t *testing.T) (*TenantService, *seal.BarrierRegistry, *seal.MemoryBackend) {
	t.Helper()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(volumeEntitlements()), WithVolumeAllocator(volumeAllocatorFor(t, reg)))
	if err != nil {
		t.Fatal(err)
	}
	return svc, reg, store
}

// retireElsewhere writes exactly what ANOTHER node's DeleteTenant replicates
// -- the tombstone, then the metadata delete -- and nothing this node's own
// teardown would do. It is the state of a replica that was down, or not
// hosting the tenant, when the delete ran.
func retireElsewhere(t *testing.T, store *seal.MemoryBackend, info *TenantInfo) {
	t.Helper()
	ctx := context.Background()
	ts, err := json.Marshal(newTombstone(info, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, tombstoneKey(info.ID), ts); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, metadataPrefix+info.ID); err != nil {
		t.Fatal(err)
	}
}

func dirExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", path, err)
	}
	return false
}

// recordingCloser records the ids it was asked to close and whether their
// directory still existed at that moment.
type recordingCloser struct {
	calls      []string
	dirAtClose map[string]bool
	dirOf      map[string]string
	t          *testing.T
	closeErr   error
}

func (c *recordingCloser) CloseTenantRuntime(_ context.Context, id string) error {
	c.calls = append(c.calls, id)
	if p, ok := c.dirOf[id]; ok {
		c.dirAtClose[id] = dirExists(c.t, p)
	}
	return c.closeErr
}

// TestReconcileDeletedTenants_ReclaimsAReplicaThatMissedTheDelete is #469's
// verification: a node that held a volume tenant when another node deleted it
// reclaims the tenant's directory, and a restart afterwards restores nothing.
func TestReconcileDeletedTenants_ReclaimsAReplicaThatMissedTheDelete(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	info, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vgone")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot: %v", err)
	}
	if !dirExists(t, dataRoot) {
		t.Fatalf("precondition: DataRoot %s not created", dataRoot)
	}
	retireElsewhere(t, store, info)

	closer := &recordingCloser{t: t, dirAtClose: map[string]bool{}, dirOf: map[string]string{"vgone": dataRoot}}
	svc.SetTenantRuntimeCloser(closer)

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatalf("ReconcileDeletedTenants: %v", err)
	}
	if report.Inspected != 1 || len(report.Reclaimed) != 1 || report.Reclaimed[0].Path != filepath.Clean(dataRoot) {
		t.Fatalf("report = %+v; want the DataRoot reclaimed", report)
	}
	if dirExists(t, dataRoot) {
		t.Fatal("the retired tenant's DataRoot survived the reconcile")
	}
	if len(closer.calls) != 1 || closer.calls[0] != "vgone" || !closer.dirAtClose["vgone"] {
		t.Fatalf("runtime closer calls = %v (dir present at close: %v); want vgone closed BEFORE its directory went", closer.calls, closer.dirAtClose)
	}
	if _, err := reg.GetTenant("vgone"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("barrier still registered: %v", err)
	}
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.GetTenant("vgone"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Fatalf("a restart restored the retired tenant's barrier: %v", err)
	}

	again, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil || len(again.Reclaimed) != 0 {
		t.Fatalf("second pass = %+v, %v; want nothing left to reclaim", again, err)
	}
}

// TestReconcileDeletedTenants_NeverTouchesALiveTenant is the ruling's
// requirement: with a live volume tenant beside a retired one, the live
// tenant's directory and barrier are untouched.
func TestReconcileDeletedTenants_NeverTouchesALiveTenant(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	_, err := svc.CreateTenant(ctx, "vlive", "Live", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, gone)

	liveDataRoot, err := reg.LocalTenantDataRoot(ctx, "vlive")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vlive): %v", err)
	}
	goneDataRoot, err := reg.LocalTenantDataRoot(ctx, "vgone")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vgone): %v", err)
	}

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range report.Reclaimed {
		if r.TenantID != "vgone" {
			t.Fatalf("reclaimed %+v, which is not the retired tenant's", r)
		}
	}
	if !dirExists(t, liveDataRoot) {
		t.Fatal("the LIVE tenant's DataRoot was removed")
	}
	if _, err := reg.GetTenant("vlive"); err != nil {
		t.Fatalf("the live tenant's barrier was touched: %v", err)
	}
	if dirExists(t, goneDataRoot) {
		t.Fatal("the retired tenant's DataRoot survived")
	}
}

// TestReconcileDeletedTenants_StaleAssignmentNamingALiveDirectoryIsSkipped
// covers the dangerous case: a node that missed the delete still holds the
// retired tenant's catalog Assignment, and that Assignment's path happens to
// collide with a LIVE tenant's directory (e.g. a peer create racing the
// delete and landing at the same relative path through a direct Reserve).
// The directory must be left alone and the skip reported.
func TestReconcileDeletedTenants_StaleAssignmentNamingALiveDirectoryIsSkipped(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	if _, err := svc.CreateTenant(ctx, "vlive", "Live", IsolationVolume); err != nil {
		t.Fatal(err)
	}
	liveDataRoot, err := reg.LocalTenantDataRoot(ctx, "vlive")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vlive): %v", err)
	}

	// A retired id ("vold") whose Assignment names the SAME tenant-relative
	// path as the live tenant above -- the state of a node that reserved
	// "vold" at "vlive"'s path before the two ever collided, and never
	// replayed vlive's own create.
	allocator := volumeAllocatorFor(t, reg)
	if _, err := allocator.Reserve(ctx, "vold", "default", "vlive", 0); err != nil {
		t.Fatalf("Reserve(vold): %v", err)
	}
	retireElsewhere(t, store, &TenantInfo{ID: "vold", Name: "Old", IsolationType: IsolationVolume})

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatalf("ReconcileDeletedTenants = %v; want the overlap skipped, not an abort", err)
	}
	if !dirExists(t, liveDataRoot) {
		t.Fatal("a stale assignment for a retired id removed a LIVE tenant's DataRoot")
	}
	found := false
	for _, s := range report.Skipped {
		if s.TenantID == "vold" && s.Path == filepath.Clean(liveDataRoot) && s.Reason == SkipOverlapsLiveTenant {
			found = true
		}
	}
	if !found {
		t.Fatalf("report = %+v; want the live directory reported as skipped (%s)", report, SkipOverlapsLiveTenant)
	}
}

// TestReconcileDeletedTenants_LiveRecordBesideATombstoneIsSkippedWhole: an id
// that somehow has BOTH is not reclaimed at all.
func TestReconcileDeletedTenants_LiveRecordBesideATombstoneIsSkippedWhole(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	_, err := svc.CreateTenant(ctx, "vboth", "Both", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	ts, _ := json.Marshal(&Tombstone{ID: "vboth", DeletedAt: time.Now().UTC()})
	if err := store.Put(ctx, tombstoneKey("vboth"), ts); err != nil {
		t.Fatal(err)
	}
	liveDataRoot, err := reg.LocalTenantDataRoot(ctx, "vboth")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vboth): %v", err)
	}

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != SkipLiveTenantRecord || len(report.Reclaimed) != 0 {
		t.Fatalf("report = %+v; want the id skipped whole as %s", report, SkipLiveTenantRecord)
	}
	if !dirExists(t, liveDataRoot) {
		t.Fatal("the directory of an id with a live record was removed")
	}
	if _, err := reg.GetTenant("vboth"); err != nil {
		t.Fatalf("the barrier of an id with a live record was forgotten: %v", err)
	}
}

// Note: the old-model equivalent of "a record naming a volume-pool base
// itself is never removed" (TestReconcileDeletedTenants_ReservedRootIsSkipped)
// is gone because its mechanism no longer exists: a catalog Assignment's
// Path is always validated, at Reserve time, as exactly one clean path
// component (volume.ValidateAssignmentPath), so RequireInsideLocalPool can
// never resolve a tenant directly to a pool's base -- only to
// {poolRoot}/{onePathComponent}. That refusal is covered directly in
// pkg/volume's own tests (decision D9).

func TestReconcileDeletedTenants_RuntimeCloseFailureStopsBeforeRemoving(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	info, err := svc.CreateTenant(ctx, "vbusy", "Busy", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vbusy")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vbusy): %v", err)
	}
	retireElsewhere(t, store, info)
	boom := errors.New("shard will not stop")
	svc.SetTenantRuntimeCloser(&recordingCloser{t: t, dirAtClose: map[string]bool{}, closeErr: boom})

	_, err = svc.ReconcileDeletedTenants(ctx)
	var re *ResidueReconcileError
	if !errors.As(err, &re) || re.Op != "close-runtime" || !errors.Is(err, boom) {
		t.Fatalf("ReconcileDeletedTenants = %v; want *ResidueReconcileError{close-runtime}", err)
	}
	if !dirExists(t, dataRoot) {
		t.Fatal("a directory was removed while its runtime could not be stopped")
	}
}

func TestReconcileDeletedTenants_ListFailureIsReported(t *testing.T) {
	svc, _ := newTestServiceWithStore(t, &failingBackend{inner: seal.NewMemoryBackend(), failLst: true})
	var re *ResidueReconcileError
	if _, err := svc.ReconcileDeletedTenants(context.Background()); !errors.As(err, &re) || re.Op != "list-tombstones" {
		t.Fatalf("ReconcileDeletedTenants = %v; want *ResidueReconcileError{list-tombstones}", err)
	}
}

func TestKVReconcileDeletedTenants_ReportsWhatItInspected(t *testing.T) {
	svc, err := NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	retireIDs(t, svc, "one", "two")
	report, err := svc.ReconcileDeletedTenants(context.Background())
	if err != nil || report.Inspected != 2 || len(report.Reclaimed) != 0 {
		t.Fatalf("report = %+v, %v; want 2 inspected, nothing reclaimed", report, err)
	}
}

func TestResidueReconcileError_Message(t *testing.T) {
	err := &ResidueReconcileError{TenantID: "acme", Op: "remove", Path: "/data/acme", Err: errors.New("busy")}
	if got := err.Error(); got != "tenant: reconcile deleted tenant acme: remove /data/acme: busy" {
		t.Fatalf("Error() = %q", got)
	}
	if got := (&InvalidPageRequestError{Field: "page_size", Reason: "must not be negative"}).Error(); got != "tenant: invalid page_size: must not be negative" {
		t.Fatalf("Error() = %q", got)
	}
}
