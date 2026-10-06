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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// The guards on the destructive half of the deleted-tenant reconcile. Every
// test here drives the real TenantService over a real pooled registry; the
// only seam is the directory removal itself, in the one test that needs a
// removal to fail.

// assignmentExists reports whether the registry's catalog still holds an
// Assignment for id -- the durable record DropTenantRecords removes, and
// whose survival after a failed removal is what lets the next pass retry.
func assignmentExists(t *testing.T, reg *seal.BarrierRegistry, id string) bool {
	t.Helper()
	_, err := reg.LocalTenantDataRoot(context.Background(), id)
	return err == nil
}

// TestReconcileDeletedTenants_CorruptLiveRecordAbortsThePass is B1. A live
// tenant whose record cannot be decoded has an unknown DataRoot -- it may be
// the very directory a retired tenant's stale record names. A scan that skips
// undecodable records would read "no live tenant claims it" and remove a live
// tenant's data; the reconcile must fail closed instead.
func TestReconcileDeletedTenants_CorruptLiveRecordAbortsThePass(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vgone")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vgone): %v", err)
	}
	retireElsewhere(t, store, gone)
	// A live tenant whose record is unreadable and whose DataRoot is, in
	// truth, the retired tenant's directory.
	if err := store.Put(ctx, metadataPrefix+"vlive", []byte(`{"id":"vlive","data_root":`)); err != nil {
		t.Fatal(err)
	}

	_, err = svc.ReconcileDeletedTenants(ctx)
	var re *ResidueReconcileError
	if !errors.As(err, &re) || re.Op != "list-tenants" {
		t.Fatalf("ReconcileDeletedTenants = %v; want *ResidueReconcileError{list-tenants}", err)
	}
	if !dirExists(t, dataRoot) {
		t.Fatal("the reconcile removed a directory while a live tenant's record was unreadable")
	}
}

// TestReconcileDeletedTenants_UndecodableRecordForTheRetiredIdIsStillLive: the
// live-record check is an existence read of the id's own key, so even a record
// that will not decode stops the reclamation of that id.
func TestReconcileDeletedTenants_UndecodableRecordForTheRetiredIdIsStillLive(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	_, err := svc.CreateTenant(ctx, "vboth", "Both", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vboth")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vboth): %v", err)
	}
	ts, _ := json.Marshal(&Tombstone{ID: "vboth", DeletedAt: time.Now().UTC()})
	if err := store.Put(ctx, tombstoneKey("vboth"), ts); err != nil {
		t.Fatal(err)
	}
	// The id's own record is corrupt. A strict scan refuses the whole pass,
	// so read the per-id decision directly.
	if err := store.Put(ctx, metadataPrefix+"vboth", []byte("{corrupt")); err != nil {
		t.Fatal(err)
	}
	report := &ReconcileReport{}
	failed, abort := svc.reconcileRetired(ctx, "vboth", &residueGuards{}, report)
	if abort != nil || len(failed) != 0 {
		t.Fatalf("reconcileRetired = %v, %v; want neither a failure nor an abort", failed, abort)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != SkipLiveTenantRecord {
		t.Fatalf("report = %+v; want the id skipped as %s", report, SkipLiveTenantRecord)
	}
	if !dirExists(t, dataRoot) {
		t.Fatal("an id with an existing record had its directory removed")
	}
}

// TestReconcileDeletedTenants_FailedRemovalIsFinishedByTheNextPass is B2. The
// records that locate a directory are dropped only after it is removed, so a
// removal that fails leaves the record, and the next pass finds the directory
// again. Dropping the records first orphaned a pool-allocated DataRoot that
// nothing named any more.
func TestReconcileDeletedTenants_FailedRemovalIsFinishedByTheNextPass(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	dataRoot, err := reg.LocalTenantDataRoot(ctx, "vgone")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vgone): %v", err)
	}
	retireElsewhere(t, store, gone)

	boom := errors.New("device busy")
	calls := 0
	real := svc.removeAll
	svc.removeAll = func(ctx context.Context, path string) error {
		calls++
		if calls == 1 {
			return boom
		}
		return real(ctx, path)
	}

	_, err = svc.ReconcileDeletedTenants(ctx)
	var re *ResidueReconcileError
	if !errors.As(err, &re) || re.Op != "remove" || !errors.Is(err, boom) {
		t.Fatalf("first pass = %v; want *ResidueReconcileError{remove}", err)
	}
	if !dirExists(t, dataRoot) {
		t.Fatal("precondition: the failed removal removed the directory")
	}
	if !assignmentExists(t, reg, "vgone") {
		t.Fatal("the record naming the directory was dropped although its removal failed")
	}

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if dirExists(t, dataRoot) {
		t.Fatal("the second pass did not reclaim the directory the first one failed on")
	}
	if len(report.Reclaimed) != 1 || report.Reclaimed[0].Path != filepath.Clean(dataRoot) {
		t.Fatalf("second pass report = %+v", report)
	}
	if assignmentExists(t, reg, "vgone") {
		t.Fatal("the record survived a successful reclamation")
	}
}

// TestReconcileDeletedTenants_PeerCreateDuringThePassIsHonoured is N1: the live
// set is re-read right before each removal, so a tenant created on a peer
// after the pass began, at the directory a stale record names, keeps it. The
// peer's create is applied from inside the runtime closer, which runs after
// the pass has read its guards and before it removes anything.
func TestReconcileDeletedTenants_PeerCreateDuringThePassIsHonoured(t *testing.T) {
	ctx := context.Background()
	svc, reg, store := newReconcileService(t)
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	contested, err := reg.LocalTenantDataRoot(ctx, "vgone")
	if err != nil {
		t.Fatalf("LocalTenantDataRoot(vgone): %v", err)
	}
	retireElsewhere(t, store, gone)

	allocator := volumeAllocatorFor(t, reg)
	svc.SetTenantRuntimeCloser(closerFunc(func(context.Context, string) error {
		// "vpeer" lands at the SAME tenant-relative path "vgone" resolves to
		// -- a direct Reserve, mirroring what a peer's CreateTenant racing
		// the delete would commit to the replicated catalog.
		if _, err := allocator.Reserve(ctx, "vpeer", "default", "vgone", 0); err != nil {
			return err
		}
		rec, _ := json.Marshal(&TenantInfo{ID: "vpeer", Name: "Peer", IsolationType: IsolationVolume, VolumePool: "default"})
		return store.Put(ctx, metadataPrefix+"vpeer", rec)
	}))

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(t, contested) {
		t.Fatal("a directory a tenant created mid-pass now claims was removed")
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != SkipOverlapsLiveTenant {
		t.Fatalf("report = %+v; want the contested directory skipped", report)
	}
}

type closerFunc func(context.Context, string) error

func (f closerFunc) CloseTenantRuntime(ctx context.Context, id string) error { return f(ctx, id) }

// TestReconcileDeletedTenants_RecordNamingAPathIsNeverRemoved covered a
// retired id's tenant-root record naming a path -- however canonical it
// looked -- refused with *seal.AbsoluteDataRootRecordError, via a raw
// record injected with putTenantRootRecord. Neither the helper nor the
// error type exist any more: a tenant-root record now holds {pool, slot},
// never a path (volume.Catalog's Assignment is the only durable record,
// and Reserve validates its path component's shape before it is ever
// written -- docs/volume-pools D9), so there is no longer a record shape
// this test's injection could even construct. The still-reachable
// property -- a candidate path is judged canonical before anything is
// removed -- is covered directly below by exercising removeResidue's own
// guard, the defense in depth it is.

// TestRemoveResidue_NonCanonicalPathIsNeverRemoved is N2: a candidate path
// that is relative or not canonical is skipped, not resolved against the
// process's working directory and removed.
//
// The candidate used to come from a raw persisted "tenant root" record,
// which a corrupt write could carry any string in. Every candidate
// removeResidue sees today comes from BarrierRegistry.TenantDataRootCandidates,
// which reconstructs it from a catalog Assignment whose path component the
// Allocator validated at Reserve time (docs/volume-pools D9) -- so the
// registry itself can no longer hand removeResidue a non-canonical
// candidate. The guard inside removeResidue is exercised directly instead,
// as the defense in depth it is.
func TestRemoveResidue_NonCanonicalPathIsNeverRemoved(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	for _, path := range []string{"relative/dir", "./also-relative"} {
		reason, rerr, err := svc.removeResidue(ctx, "vrel", path, &residueGuards{})
		if err != nil || rerr != nil || reason != SkipNotCanonical {
			t.Fatalf("removeResidue(%q) = %q, %v, %v; want %s, nil, nil", path, reason, rerr, err, SkipNotCanonical)
		}
	}
}

// TestRemoveResidue_DottedAbsolutePathIsNeverRemoved: an absolute candidate
// carrying a ".." segment names its PARENT once cleaned. It must be judged
// not canonical, so skipped -- and never cleaned into a different, existing
// directory and removed.
func TestRemoveResidue_DottedAbsolutePathIsNeverRemoved(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	dotted := filepath.Join(parent, "a") + "/.."

	reason, rerr, err := svc.removeResidue(ctx, "vdotted", dotted, &residueGuards{})
	if err != nil || rerr != nil || reason != SkipNotCanonical {
		t.Fatalf("removeResidue(%q) = %q, %v, %v; want %s, nil, nil", dotted, reason, rerr, err, SkipNotCanonical)
	}
	if !dirExists(t, parent) {
		t.Fatalf("removeResidue touched %s, the parent a dotted candidate resolves to", parent)
	}
}

// TestRemoveResidue_ReservedDataRootOverlapIsSkipped is N3: a reserved data
// root is guarded by overlap in BOTH directions, so a directory INSIDE the
// reserved root is refused too -- unless it also lies strictly inside a pool
// base, where every tenant's pool directory lies by construction (see
// TestReconcileDeletedTenants_PoolDirectoryInsideAReservedRootIsReclaimed).
// This path is in no pool, so it is refused.
func TestRemoveResidue_ReservedDataRootOverlapIsSkipped(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	reserved := t.TempDir()
	inside := filepath.Join(reserved, "system-data")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}

	reason, rerr, err := svc.removeResidue(ctx, "vsys", inside, &residueGuards{reserved: []string{reserved}})
	if err != nil || rerr != nil || reason != SkipOverlapsReservedRoot {
		t.Fatalf("removeResidue(%q) = %q, %v, %v; want %s, nil, nil", inside, reason, rerr, err, SkipOverlapsReservedRoot)
	}
	if !dirExists(t, inside) {
		t.Fatal("removeResidue removed a directory inside a reserved data root")
	}
}

// TestDecodePageToken_Contract covers every token shape: none means the first
// page; a decodable token resumes after the id it names; anything else is the
// caller's error. Raw base64url never decodes a non-empty token to zero bytes,
// so there is no "empty id" token to handle -- the one way to ask for no bound
// is to send no token.
func TestDecodePageToken_Contract(t *testing.T) {
	if after, err := decodePageToken(""); err != nil || after != "" {
		t.Fatalf("empty token = %q, %v; want the first page", after, err)
	}
	if after, err := decodePageToken(encodePageToken("acme")); err != nil || after != "acme" {
		t.Fatalf("round trip = %q, %v", after, err)
	}
	for _, bad := range []string{"A", "%%", "=", "YQ=="} {
		var pr *InvalidPageRequestError
		if _, err := decodePageToken(bad); !errors.As(err, &pr) || pr.Field != "page_token" {
			t.Errorf("decodePageToken(%q) = %v; want *InvalidPageRequestError{page_token}", bad, err)
		}
	}
}

// TestListDeletedTenants_TokenBeyondTheLastIdIsAnEmptyLastPage: a token that
// decodes to an id past every retirement yields an empty page with no token.
func TestListDeletedTenants_TokenBeyondTheLastIdIsAnEmptyLastPage(t *testing.T) {
	svc, _ := newTestService(t)
	retireIDs(t, svc, "alpha")
	page, err := svc.ListDeletedTenants(context.Background(), encodePageToken("zzz"), 10)
	if err != nil || len(page.Tombstones) != 0 || page.NextPageToken != "" {
		t.Fatalf("page = %+v, %v; want an empty last page", page, err)
	}
}

func TestTenantService_TenantRuntimeCloserAccessor(t *testing.T) {
	svc, _ := newTestService(t)
	if svc.TenantRuntimeCloser() != nil {
		t.Fatal("a fresh service reports a runtime closer")
	}
	c := closerFunc(func(context.Context, string) error { return nil })
	svc.SetTenantRuntimeCloser(c)
	if svc.TenantRuntimeCloser() == nil {
		t.Fatal("the wired closer is not reported")
	}
}

// TestCreateTenant_RefusesARootAStaleRecordStillClaims covered CreateTenant
// refusing an explicit --data-root that collided with a not-yet-reclaimed
// retired tenant's recorded root (*DataRootOverlapError, the
// retiredResidueRemedy). CreateTenant no longer accepts an explicit disk
// path: a volume tenant's reserved path is always its own tenant id
// (reserveAndRegisterVolumeTenant Reserves at id, never a caller-supplied
// path), so two DIFFERENT tenant ids can never contend for the same literal
// path through this API -- the collision this test built is no longer
// constructible. The analogous collision that IS still reachable -- a stale
// Assignment naming a path a LIVE tenant's own directory also occupies -- is
// covered on the reconcile side by
// TestReconcileDeletedTenants_StaleAssignmentNamingALiveDirectoryIsSkipped in
// deleted_tenants_test.go.

func TestVolumeRegisterError_MapsEachCause(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t)
	if err := svc.volumeRegisterError(ctx, "a", "register-volume", seal.ErrTenantAlreadyExists); !errors.Is(err, ErrTenantAlreadyExists) {
		t.Fatalf("already exists = %v", err)
	}
	var be *BarrierError
	if err := svc.volumeRegisterError(ctx, "a", "register-raft", errors.New("x")); !errors.As(err, &be) || be.Op != "register-raft" {
		t.Fatalf("other = %v", err)
	}
}

// TestVolumeRegisterError_LiveClaimantHasNoRemedy and
// TestVolumeRegisterError_TombstoneReadFailureIsReported covered the branch of
// volumeRegisterError that inspected a *seal.DataRootClaimedError and
// consulted the tombstone store to decide whether to name the
// reconcile-deleted remedy. The catalog-backed Allocator validates a path's
// shape at Reserve time (docs/volume-pools D9), so a registration can no
// longer fail with a raw claimed-path error for volumeRegisterError to
// classify -- that type is gone, and with it the branch; the only causes
// volumeRegisterError maps today are covered by
// TestVolumeRegisterError_MapsEachCause.
