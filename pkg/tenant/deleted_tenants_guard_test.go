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

// putTenantRootRecord writes a raw tenant-root record -- the state of a node
// holding a record for an id, in whatever form it was written.
func putTenantRootRecord(t *testing.T, svc *TenantService, id string, rec map[string]any) {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.registry.SystemBarrier().Put(context.Background(), "__registry:tenant_root/"+id, raw); err != nil {
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
// records that locate a directory are dropped only after it is removed, so a
// removal that fails leaves the record, and the next pass finds the directory
// again.
func TestReconcileDeletedTenants_FailedRemovalIsFinishedByTheNextPass(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	explicit := gone.DataRoot
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

type closerFunc func(context.Context, string) error

func (f closerFunc) CloseTenantRuntime(ctx context.Context, id string) error { return f(ctx, id) }

// TestReconcileDeletedTenants_RecordNamingAPathIsNeverRemoved: a retired id's
// tenant-root record that names a path -- however canonical it looks -- is
// refused, and the pass removes nothing for it. Only this node's own pool
// directory for the id is ever a candidate.
func TestReconcileDeletedTenants_RecordNamingAPathIsNeverRemoved(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	foreign := filepath.Join(t.TempDir(), "another-node", "vlegacy")
	if err := os.MkdirAll(filepath.Join(foreign, "barrier"), 0o700); err != nil {
		t.Fatal(err)
	}
	putTenantRootRecord(t, svc, "vlegacy", map[string]any{"pool": "default", "slot": 0, "data_root": foreign})
	retireElsewhere(t, store, &TenantInfo{ID: "vlegacy", IsolationType: IsolationVolume})

	report, err := svc.ReconcileDeletedTenants(ctx)
	var agg *ResidueReconcileFailuresError
	if !errors.As(err, &agg) || len(agg.Errors) != 1 || agg.Errors[0].TenantID != "vlegacy" {
		t.Fatalf("ReconcileDeletedTenants = %v; want vlegacy's record refused", err)
	}
	var abs *seal.AbsoluteDataRootRecordError
	if !errors.As(err, &abs) {
		t.Fatalf("the failure is %v; want *seal.AbsoluteDataRootRecordError", err)
	}
	if report == nil || len(report.Reclaimed) != 0 {
		t.Fatalf("report = %+v; want nothing reclaimed", report)
	}
	if !dirExists(t, foreign) {
		t.Fatal("the directory a refused record named was removed")
	}
	if !recordExists(t, svc, "vlegacy") {
		t.Fatal("the refused record was dropped")
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

func TestVolumeRegisterError_MapsEachCause(t *testing.T) {
	if err := volumeRegisterError("a", "register-volume", seal.ErrTenantAlreadyExists); !errors.Is(err, ErrTenantAlreadyExists) {
		t.Fatalf("already exists = %v", err)
	}
	var be *BarrierError
	if err := volumeRegisterError("a", "register-raft", errors.New("x")); !errors.As(err, &be) || be.Op != "register-raft" {
		t.Fatalf("other = %v", err)
	}
}
