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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// How one reconcile pass behaves as a whole: what it reports, what it logs,
// what it does when one id fails, and what it does not redo.

// TestReconcileDeletedTenants_PoolDirectoryInsideAReservedRootIsReclaimed: a
// pool base may itself lie inside a reserved root (a pool carved out of the
// system data directory). A retired tenant's pool directory is strictly
// inside the pool base, so the reserved root's "lies inside" arm must not
// protect it -- only a path that IS, or CONTAINS, a reserved root is refused.
func TestReconcileDeletedTenants_PoolDirectoryInsideAReservedRootIsReclaimed(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	bases, err := reg.PoolBasePaths(ctx)
	if err != nil || len(bases) != 1 {
		t.Fatalf("PoolBasePaths = %v, %v", bases, err)
	}
	reserved := filepath.Dir(bases[0]) // the reserved root contains the pool base
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(volumeEntitlements()), WithReservedDataRoots(reserved))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := svc.CreateTenant(ctx, "vpool", "Pool", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, gone)

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dirExists(t, gone.DataRoot) {
		t.Fatalf("report = %+v; a pool directory inside a reserved root was not reclaimed", report)
	}
	if !dirExists(t, bases[0]) || !dirExists(t, reserved) {
		t.Fatal("the pool base or the reserved root was removed")
	}
}

// TestReconcileDeletedTenants_ReservedRootItselfIsRefused: the pool-base
// exemption never extends to a path that IS a reserved root.
func TestReconcileDeletedTenants_ReservedRootItselfIsRefused(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	bases, _ := reg.PoolBasePaths(ctx)
	reserved := filepath.Join(bases[0], "system-data") // a reserved root INSIDE a pool base
	if err := mkdirAll(reserved); err != nil {
		t.Fatal(err)
	}
	svc, err := NewTenantService(store, reg, testLogger(),
		WithDefaultEntitlements(volumeEntitlements()), WithReservedDataRoots(reserved))
	if err != nil {
		t.Fatal(err)
	}
	putTenantRootRecord(t, svc, "vres", reserved)
	retireElsewhere(t, store, &TenantInfo{ID: "vres", IsolationType: IsolationVolume})

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !dirExists(t, reserved) {
		t.Fatal("a reserved root inside a pool base was removed")
	}
	if !hasSkip(report, "vres", reserved, SkipOverlapsReservedRoot) {
		t.Fatalf("report = %+v; want %s skipped as %s", report, reserved, SkipOverlapsReservedRoot)
	}
}

func hasSkip(report *ReconcileReport, id, path, reason string) bool {
	for _, sk := range report.Skipped {
		if sk.TenantID == id && sk.Path == path && sk.Reason == reason {
			return true
		}
	}
	return false
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestReconcileDeletedTenants_EverySkipIsLoggedAtWarn: a directory left in
// place is disk the operator still owns, so each one is logged at Warn with
// the path and the reason, not only counted in the report.
func TestReconcileDeletedTenants_EverySkipIsLoggedAtWarn(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	svc, err := NewTenantService(store, reg, logger, WithDefaultEntitlements(volumeEntitlements()))
	if err != nil {
		t.Fatal(err)
	}
	putTenantRootRecord(t, svc, "vrel", "relative/dir")
	retireElsewhere(t, store, &TenantInfo{ID: "vrel", IsolationType: IsolationVolume})

	if _, err := svc.ReconcileDeletedTenants(ctx); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{"level=WARN", "tenant_id=vrel", "path=relative/dir", "reason=" + SkipNotCanonical} {
		if !strings.Contains(out, want) {
			t.Fatalf("log lacks %q:\n%s", want, out)
		}
	}
}

// TestReconcileDeletedTenants_OneFailingIdDoesNotStopTheOthers: a per-id
// failure is collected and the pass goes on to the next id; the pass returns
// its report AND an aggregate typed error naming what failed.
func TestReconcileDeletedTenants_OneFailingIdDoesNotStopTheOthers(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	first, err := svc.CreateTenant(ctx, "va", "A", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.CreateTenant(ctx, "vb", "B", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, first)
	retireElsewhere(t, store, second)

	boom := errors.New("device busy")
	real := svc.removeAll
	svc.removeAll = func(path string) error {
		if path == first.DataRoot {
			return boom
		}
		return real(path)
	}

	report, err := svc.ReconcileDeletedTenants(ctx)
	var agg *ResidueReconcileFailuresError
	if !errors.As(err, &agg) || len(agg.Errors) != 1 || agg.Errors[0].TenantID != "va" || !errors.Is(err, boom) {
		t.Fatalf("err = %v; want *ResidueReconcileFailuresError with va's removal failure", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "1 failed") || !strings.Contains(msg, "va") {
		t.Fatalf("message = %q; want the failure count and the failed id", msg)
	}
	if report == nil || len(report.Reclaimed) != 1 || report.Reclaimed[0].TenantID != "vb" {
		t.Fatalf("report = %+v; want vb reclaimed despite va's failure", report)
	}
	// The report carries its own copy of the failures, which is what lets a
	// transport answer with the report alone.
	if len(report.Failed) != 1 || report.Failed[0].TenantID != "va" || report.Failed[0].Op != "remove" || report.Failed[0].Error != boom.Error() {
		t.Fatalf("report.Failed = %+v; want va's removal failure", report.Failed)
	}
	if rerr := ReconcileResponseError(report, err); rerr != nil {
		t.Fatalf("ReconcileResponseError = %v; want nil, the report carries the failures", rerr)
	}
	if dirExists(t, second.DataRoot) {
		t.Fatal("the second id was not reclaimed after the first failed")
	}
	if !recordExists(t, svc, "va") {
		t.Fatal("the failed id's record was dropped")
	}
}

// countingCloser counts CloseTenantRuntime calls per id.
type countingCloser struct {
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingCloser) CloseTenantRuntime(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[id]++
	return nil
}

// TestReconcileDeletedTenants_FullyReclaimedIdsAreShortCircuited: an id with
// no directory left on disk, no record and no registration has nothing to
// reclaim, and a later pass does no work for it -- it does not stop runtimes,
// release barriers or touch records for every retirement ever made.
func TestReconcileDeletedTenants_FullyReclaimedIdsAreShortCircuited(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume)
	if err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, gone)
	closer := &countingCloser{calls: map[string]int{}}
	svc.SetTenantRuntimeCloser(closer)

	if _, err := svc.ReconcileDeletedTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if closer.calls["vgone"] != 1 {
		t.Fatalf("first pass closed vgone %d times; want 1", closer.calls["vgone"])
	}
	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if closer.calls["vgone"] != 1 {
		t.Fatalf("a fully reclaimed id was worked again (closer calls = %d)", closer.calls["vgone"])
	}
	if report.Inspected != 1 || len(report.Reclaimed) != 0 || len(report.Skipped) != 0 {
		t.Fatalf("second pass report = %+v; want 1 inspected and nothing else", report)
	}
}

// TestCreateTenant_ClaimedByARetiredTenantNamesTheVerb: when the record that
// claims the DataRoot belongs to a RETIRED tenant, the refusal says so and
// names the verb that reclaims it.
func TestCreateTenant_ClaimedByARetiredTenantNamesTheVerb(t *testing.T) {
	ctx := context.Background()
	svc, _, store := newReconcileService(t)
	disk := filepath.Join(t.TempDir(), "disk")
	gone, err := svc.CreateTenant(ctx, "vgone", "Gone", IsolationVolume, WithDataRoot(disk))
	if err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, gone)
	if err := removeAllForTest(disk); err != nil {
		t.Fatal(err)
	}
	if err := svc.registry.ReleaseTenant(ctx, "vgone"); err != nil {
		t.Fatal(err)
	}

	_, err = svc.CreateTenant(ctx, "vnew", "New", IsolationVolume, WithDataRoot(disk))
	var oe *DataRootOverlapError
	if !errors.As(err, &oe) {
		t.Fatalf("CreateTenant = %v; want *DataRootOverlapError", err)
	}
	for _, want := range []string{"retired tenant vgone", "qrdb tenant reconcile-deleted"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func mkdirAll(path string) error         { return os.MkdirAll(path, 0o700) }
func removeAllForTest(path string) error { return os.RemoveAll(path) }

// mountedRootRemover is the operating system's filesystem with one
// difference: the directories named in mounts are mount points, so removing
// one -- however empty -- fails with EBUSY, exactly as rmdir(2) does.
type mountedRootRemover struct {
	mounts map[string]bool
}

var _ seal.DirectoryRemover = (*mountedRootRemover)(nil)

func (m *mountedRootRemover) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }
func (m *mountedRootRemover) RemoveAll(path string) error {
	if m.mounts[filepath.Clean(path)] {
		// os.RemoveAll ends in the same rmdir the mount point refuses.
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		return &os.PathError{Op: "unlinkat", Path: path, Err: syscall.EBUSY}
	}
	return os.RemoveAll(path)
}
func (m *mountedRootRemover) Remove(path string) error {
	if m.mounts[filepath.Clean(path)] {
		return &os.PathError{Op: "remove", Path: path, Err: syscall.EBUSY}
	}
	return os.Remove(path)
}

// TestReconcileDeletedTenants_MountPointDataRootIsReclaimed: a retired
// tenant's dedicated DataRoot is a mount point. The reconcile empties it and
// counts it reclaimed -- and drops the records -- rather than failing on the
// rmdir of the mount itself and retrying it on every pass forever.
func TestReconcileDeletedTenants_MountPointDataRootIsReclaimed(t *testing.T) {
	ctx := context.Background()
	disk := filepath.Join(t.TempDir(), "tenant-disk")
	remover := &mountedRootRemover{mounts: map[string]bool{disk: true}}
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t, seal.WithDirectoryRemover(remover))
	if err := reg.SystemBarrier().Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewTenantService(store, reg, testLogger(), WithDefaultEntitlements(volumeEntitlements()))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := svc.CreateTenant(ctx, "vmount", "Mounted", IsolationVolume, WithDataRoot(disk))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(disk, "leftover"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	retireElsewhere(t, store, gone)

	report, err := svc.ReconcileDeletedTenants(ctx)
	if err != nil {
		t.Fatalf("ReconcileDeletedTenants = %v; want a mount point that was emptied to count as reclaimed", err)
	}
	entries, err := os.ReadDir(disk)
	if err != nil || len(entries) != 0 {
		t.Fatalf("mount point contents = %v, %v; want it emptied", entries, err)
	}
	reclaimed := false
	for _, r := range report.Reclaimed {
		if r.Path == disk {
			reclaimed = true
		}
	}
	if !reclaimed {
		t.Fatalf("report = %+v; want %s reclaimed", report, disk)
	}
	if recordExists(t, svc, "vmount") {
		t.Fatal("the records survived a reclaimed mount point, so every later pass would retry it")
	}
}

// TestReconcileResponseError_OnlyPerIDFailuresTravelInTheReport pins which
// errors a transport may fold into the report: only an aggregate the report
// already copies. A guard failure, or an aggregate with no report (or a report
// that does not carry it), is still an error.
func TestReconcileResponseError_OnlyPerIDFailuresTravelInTheReport(t *testing.T) {
	boom := errors.New("boom")
	agg := &ResidueReconcileFailuresError{Errors: []*ResidueReconcileError{{TenantID: "va", Op: "remove", Err: boom}}}
	carried := &ReconcileReport{Failed: []FailedReclaim{{TenantID: "va", Op: "remove", Error: "boom"}}}
	guard := &ResidueReconcileError{Op: "list-tenants", Err: boom}
	cases := []struct {
		name   string
		report *ReconcileReport
		err    error
		want   error
	}{
		{"success", &ReconcileReport{}, nil, nil},
		{"per-id failures the report carries", carried, agg, nil},
		{"guard failure", nil, guard, guard},
		{"aggregate without a report", nil, agg, agg},
		{"aggregate the report does not carry", &ReconcileReport{}, agg, agg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReconcileResponseError(tc.report, tc.err); got != tc.want {
				t.Fatalf("ReconcileResponseError = %v; want %v", got, tc.want)
			}
		})
	}
}
