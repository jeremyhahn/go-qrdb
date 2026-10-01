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
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// The guards on the destructive half of the deleted-tenant reconcile. Every
// test here drives the real TenantService over a real pooled registry; the
// only seam is the directory removal itself, in the one test that needs a
// removal to fail.

// putTenantRootRecord writes a tenant-root record in the exact form the
// registry persists it -- the state of a node holding a record for an id.
func putTenantRootRecord(t *testing.T, svc *TenantService, id, dataRoot string) {
	t.Helper()
	rec, err := json.Marshal(map[string]any{"data_root": dataRoot, "auto_unseal": false})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.registry.SystemBarrier().Put(context.Background(), "__registry:tenant_root/"+id, rec); err != nil {
		t.Fatal(err)
	}
}

func recordExists(t *testing.T, svc *TenantService, id string) bool {
	t.Helper()
	_, err := svc.registry.SystemBarrier().Get(context.Background(), "__registry:tenant_root/"+id)
	if err == nil {
		return true
	}
	if !errors.Is(err, seal.ErrSecretNotFound) {
		t.Fatal(err)
	}
	return false
}

// TestReconcileDeletedTenants_CorruptLiveRecordAbortsThePass is B1. A live
// tenant whose record cannot be decoded has an unknown DataRoot -- it may be
// the very directory a retired tenant's stale record names. A scan that skips
// undecodable records would read "no live tenant claims it" and remove a live
// tenant's data; the reconcile must fail closed instead.
func TestReconcileDeletedTenants_CorruptLiveRecordAbortsThePass(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume)
	if err != nil {
		t.Fatal(err)
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
	if !dirExists(t, gone.DataRoot) {
		t.Fatal("the reconcile removed a directory while a live tenant's record was unreadable")
	}
}

// TestReconcileDeletedTenants_UndecodableRecordForTheRetiredIdIsStillLive: the
// live-record check is an existence read of the id's own key, so even a record
// that will not decode stops the reclamation of that id.
func TestReconcileDeletedTenants_UndecodableRecordForTheRetiredIdIsStillLive(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	gone, err := svc.CreateTenant(ctx, "vboth", "Both", IsolationVolume)
	if err != nil {
		t.Fatal(err)
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
	if !dirExists(t, gone.DataRoot) {
		t.Fatal("an id with an existing record had its directory removed")
	}
}

// TestReconcileDeletedTenants_FailedRemovalIsFinishedByTheNextPass is B2. The
// records that name a directory are dropped only after it is removed, so a
// removal that fails leaves the record, and the next pass finds the directory
// again. Dropping the records first orphaned an operator-supplied DataRoot
// that nothing named any more.
func TestReconcileDeletedTenants_FailedRemovalIsFinishedByTheNextPass(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	explicit := filepath.Join(t.TempDir(), "operator-disk")
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume, WithDataRoot(explicit))
	if err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, gone)

	boom := errors.New("device busy")
	calls := 0
	real := svc.removeAll
	svc.removeAll = func(path string) error {
		calls++
		if calls == 1 {
			return boom
		}
		return real(path)
	}

	_, err = svc.ReconcileDeletedTenants(ctx)
	var re *ResidueReconcileError
	if !errors.As(err, &re) || re.Op != "remove" || !errors.Is(err, boom) {
		t.Fatalf("first pass = %v; want *ResidueReconcileError{remove}", err)
	}
	if !dirExists(t, explicit) {
		t.Fatal("precondition: the failed removal removed the directory")
	}
	if !recordExists(t, svc, "vgone") {
		t.Fatal("the record naming the directory was dropped although its removal failed")
	}

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if dirExists(t, explicit) {
		t.Fatal("the second pass did not reclaim the directory the first one failed on")
	}
	if len(report.Reclaimed) != 1 || report.Reclaimed[0].Path != explicit {
		t.Fatalf("second pass report = %+v", report)
	}
	if recordExists(t, svc, "vgone") {
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
	svc, _, store := newReconcileService(t)
	contested := filepath.Join(t.TempDir(), "contested")
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume, WithDataRoot(contested))
	if err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, gone)

	svc.SetTenantRuntimeCloser(closerFunc(func(context.Context, string) error {
		rec, _ := json.Marshal(&TenantInfo{ID: "vpeer", Name: "Peer", IsolationType: IsolationVolume, DataRoot: contested})
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

// TestReconcileDeletedTenants_NonCanonicalRecordIsNeverRemoved is N2: a
// recorded root that is relative or not canonical is skipped, not resolved
// against the process's working directory and removed.
func TestReconcileDeletedTenants_NonCanonicalRecordIsNeverRemoved(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	putTenantRootRecord(t, svc, "vrel", "relative/dir")
	putTenantRootRecord(t, svc, "vdots", "./also-relative")
	for _, id := range []string{"vrel", "vdots"} {
		retireElsewhere(t, store, &TenantInfo{ID: id, IsolationType: IsolationVolume})
	}

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	notCanonical := 0
	for _, sk := range report.Skipped {
		if sk.Reason == SkipNotCanonical {
			notCanonical++
		}
	}
	if notCanonical != 2 || len(report.Reclaimed) != 0 {
		t.Fatalf("report = %+v; want both records skipped as %s", report, SkipNotCanonical)
	}
}

// TestReconcileDeletedTenants_DottedAbsoluteRecordIsNeverRemoved: an absolute
// recorded root carrying a ".." segment names its PARENT once cleaned. It must
// be judged as recorded -- not canonical, so skipped -- and never cleaned into
// a different, existing directory and removed.
func TestReconcileDeletedTenants_DottedAbsoluteRecordIsNeverRemoved(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	dotted := filepath.Join(parent, "a") + "/.."
	putTenantRootRecord(t, svc, "vdotted", dotted)
	retireElsewhere(t, store, &TenantInfo{ID: "vdotted", IsolationType: IsolationVolume})

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(t, parent) {
		t.Fatalf("the reconcile removed %s, the parent a dotted record resolves to", parent)
	}
	found := false
	for _, sk := range report.Skipped {
		if sk.Path == dotted && sk.Reason == SkipNotCanonical {
			found = true
		}
	}
	if !found || len(report.Reclaimed) != 0 {
		t.Fatalf("report = %+v; want %s skipped as %s", report, dotted, SkipNotCanonical)
	}
}

// TestReconcileDeletedTenants_ReservedDataRootOverlapIsSkipped is N3: a
// reserved data root is guarded by overlap in BOTH directions, so a directory
// INSIDE the reserved root is refused too -- unless it also lies strictly
// inside a pool base, where every tenant's pool directory lies by
// construction (see TestReconcileDeletedTenants_PoolDirectoryInsideAReservedRootIsReclaimed).
// This path is in no pool, so it is refused.
func TestReconcileDeletedTenants_ReservedDataRootOverlapIsSkipped(t *testing.T) {
	ctx := context.Background()
	reserved := t.TempDir()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(volumeEntitlements()), WithReservedDataRoots(reserved))
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(reserved, "system-data")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	putTenantRootRecord(t, svc, "vsys", inside)
	retireElsewhere(t, store, &TenantInfo{ID: "vsys", IsolationType: IsolationVolume})

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(t, inside) {
		t.Fatal("the reconcile removed a directory inside a reserved data root")
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Reason != SkipOverlapsReservedRoot {
		t.Fatalf("report = %+v; want the path skipped as %s", report, SkipOverlapsReservedRoot)
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

// TestCreateTenant_RefusesARootAStaleRecordStillClaims is the create side of
// N1: a create at the directory a not-yet-reclaimed retired tenant's record
// names is refused as a DataRoot overlap rather than handed a path the
// reconcile may remove.
func TestCreateTenant_RefusesARootAStaleRecordStillClaims(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	disk := filepath.Join(t.TempDir(), "disk")
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume, WithDataRoot(disk))
	if err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, gone)
	// Empty the directory as another node's delete would have, leaving this
	// node's record behind.
	if err := os.RemoveAll(disk); err != nil {
		t.Fatal(err)
	}
	if err := svc.registry.ReleaseTenant(ctx, "vgone"); err != nil {
		t.Fatal(err)
	}

	_, err = svc.CreateTenant(ctx, "vnew", "New", IsolationVolume, WithDataRoot(disk))
	var oe *DataRootOverlapError
	if !errors.As(err, &oe) || oe.Kind != "retired tenant vgone" || oe.Remedy != retiredResidueRemedy {
		t.Fatalf("CreateTenant = %v; want *DataRootOverlapError naming retired tenant vgone and the remedy", err)
	}
}

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

// TestVolumeRegisterError_LiveClaimantHasNoRemedy: a claimant with no
// tombstone is an ordinary overlap -- the operator must pick another path, and
// the reconcile verb would not help, so none is named.
func TestVolumeRegisterError_LiveClaimantHasNoRemedy(t *testing.T) {
	svc, _ := newTestService(t)
	err := svc.volumeRegisterError(context.Background(), "b", "register-volume",
		&seal.DataRootClaimedError{DataRoot: "/srv/disk", ClaimedBy: "alive"})
	var oe *DataRootOverlapError
	if !errors.As(err, &oe) || oe.Kind != "tenant alive" || oe.Remedy != "" {
		t.Fatalf("err = %v; want an overlap naming tenant alive with no remedy", err)
	}
	if strings.Contains(err.Error(), "reconcile-deleted") {
		t.Fatalf("a live claimant's error names the reconcile verb: %v", err)
	}
}

// TestVolumeRegisterError_TombstoneReadFailureIsReported: when whether the
// claimant is retired cannot be read, the store failure is reported rather
// than a guess at the remedy.
func TestVolumeRegisterError_TombstoneReadFailureIsReported(t *testing.T) {
	svc, _ := newTestService(t)
	svc.store = &failingBackend{inner: svc.store, failGet: true}
	err := svc.volumeRegisterError(context.Background(), "b", "register-volume",
		&seal.DataRootClaimedError{DataRoot: "/srv/disk", ClaimedBy: "who"})
	var se *StoreError
	if !errors.As(err, &se) || !errors.Is(err, errStoreFault) {
		t.Fatalf("err = %v; want the *StoreError wrapping the read failure", err)
	}
}
