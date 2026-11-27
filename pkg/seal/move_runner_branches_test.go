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

package seal

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// snapshotStores is fakeStores that also relocates the tenant's Raft
// snapshot records, recording each root it is asked to rewrite them for.
type snapshotStores struct {
	*fakeStores
	roots []string
	err   error
}

func (s *snapshotStores) RelocateSnapshots(_ context.Context, tenantID, root string) error {
	if tenantID != moveTestTenant {
		return errors.New("unexpected tenant " + tenantID)
	}
	if s.err != nil {
		return s.err
	}
	s.roots = append(s.roots, root)
	return nil
}

// closeFailStores is fakeStores whose close fails: the tenant's stores
// cannot be released, so no step may touch its directory.
type closeFailStores struct {
	*fakeStores
	err error
}

func (s *closeFailStores) CloseTenantStores(context.Context, string) error { return s.err }

func newRunnerWith(t *testing.T, fx *moveFixture, stores TenantStoreCloser) *MoveStepRunner {
	t.Helper()
	r, err := NewMoveStepRunner(fx.reg, moveTestNode, stores)
	if err != nil {
		t.Fatalf("NewMoveStepRunner: %v", err)
	}
	return r
}

func TestMoveStepRunner_SettleRelocatesSnapshots(t *testing.T) {
	fx, stores, _ := newRunnerFixture(t)
	ctx := context.Background()
	ss := &snapshotStores{fakeStores: stores}
	r := newRunnerWith(t, fx, ss)
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	ss.err = errors.New("snapshot relocation refused")
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); !errors.Is(err, ss.err) {
		t.Fatalf("copy settle with a refused snapshot relocation = %v, want it", err)
	}
	ss.err = nil
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy settle: %v", err)
	}
	if got := ss.roots[len(ss.roots)-1]; got != fx.dst() {
		t.Fatalf("copy settle relocated snapshots to %q, want the destination %q", got, fx.dst())
	}
	abort := runnerStep(volume.MoveStepPhaseAbort, true)
	if err := r.RunMoveStep(ctx, abort); err != nil {
		t.Fatalf("abort run: %v", err)
	}
	ss.err = errors.New("snapshot relocation refused")
	if err := r.SettleMoveStep(ctx, abort); !errors.Is(err, ss.err) {
		t.Fatalf("abort settle with a refused snapshot relocation = %v, want it", err)
	}
	ss.err = nil
	if err := r.SettleMoveStep(ctx, abort); err != nil {
		t.Fatalf("abort settle: %v", err)
	}
	if got := ss.roots[len(ss.roots)-1]; got != fx.src {
		t.Fatalf("abort settle relocated snapshots to %q, want the source %q", got, fx.src)
	}
}

func TestMoveStepRunner_SettleAbortFailures(t *testing.T) {
	ctx := context.Background()
	abort := runnerStep(volume.MoveStepPhaseAbort, true)

	t.Run("stores cannot reopen", func(t *testing.T) {
		_, stores, r := newRunnerFixture(t)
		stores.openErr = errors.New("open refused")
		if err := r.SettleMoveStep(ctx, abort); !errors.Is(err, stores.openErr) {
			t.Fatalf("abort settle = %v, want the open error", err)
		}
	})
	t.Run("data dirs cannot relocate", func(t *testing.T) {
		fx, stores, _ := newRunnerFixture(t)
		rs := &relocatingStores{fakeStores: stores, err: errors.New("relocate refused")}
		r := newRunnerWith(t, fx, rs)
		if err := r.SettleMoveStep(ctx, abort); !errors.Is(err, rs.err) {
			t.Fatalf("abort settle = %v, want the relocation error", err)
		}
	})
	t.Run("unknown destination pool", func(t *testing.T) {
		_, _, r := newRunnerFixture(t)
		bad := abort
		bad.ToPool = "no-such-pool"
		if err := r.SettleMoveStep(ctx, bad); err == nil {
			t.Fatal("abort settle onto an unknown pool succeeded")
		}
	})
	t.Run("phases with nothing to settle", func(t *testing.T) {
		_, stores, r := newRunnerFixture(t)
		opens := len(stores.opens)
		for _, step := range []volume.MoveStep{
			runnerStep(volume.MoveStepPhaseAbort, false),
			runnerStep(volume.MoveStepPhaseRetire, true),
		} {
			if err := r.SettleMoveStep(ctx, step); err != nil {
				t.Fatalf("settle %s: %v", step.Phase, err)
			}
		}
		if len(stores.opens) != opens {
			t.Fatal("a settle with nothing to do reopened the stores")
		}
	})
}

func TestMoveStepRunner_PlacementResolutionFailures(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		step func() volume.MoveStep
	}{
		{"unknown tenant", func() volume.MoveStep {
			s := runnerStep(volume.MoveStepPhaseCopy, false)
			s.TenantID = "ghost"
			return s
		}},
		{"unknown source pool", func() volume.MoveStep {
			s := runnerStep(volume.MoveStepPhaseCopy, false)
			s.FromPool = "no-such-pool"
			return s
		}},
		{"unknown destination pool", func() volume.MoveStep {
			s := runnerStep(volume.MoveStepPhaseCopy, false)
			s.ToPool = "no-such-pool"
			return s
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx, stores, _ := newRunnerFixture(t)
			rs := &relocatingStores{fakeStores: stores}
			// The fake opens only tenants with a pool assignment.
			r := newRunnerWith(t, fx, &noOpenRelocator{rs})
			step := tc.step()
			if err := r.SettleMoveStep(ctx, step); err == nil {
				t.Fatal("settle with an unresolvable placement succeeded")
			}
			if len(rs.moves) != 0 {
				t.Fatalf("an unresolvable placement relocated %v", rs.moves)
			}
			abort := step
			abort.Phase = volume.MoveStepPhaseAbort
			abort.Copied = true
			if err := r.RunMoveStep(ctx, abort); err == nil {
				t.Fatal("copy back with an unresolvable placement succeeded")
			}
		})
	}
}

// noOpenRelocator is relocatingStores whose open and close are no-ops, for
// a tenant the fake cannot resolve.
type noOpenRelocator struct{ *relocatingStores }

func (n *noOpenRelocator) OpenTenantStores(context.Context, string) error  { return nil }
func (n *noOpenRelocator) CloseTenantStores(context.Context, string) error { return nil }

func TestMoveStepRunner_UnconfiguredPoolsRefuseSettle(t *testing.T) {
	reg, err := NewBarrierRegistry(poolTestLogger(), newPoolSystemBarrier(t))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	rs := &noOpenRelocator{&relocatingStores{fakeStores: &fakeStores{t: t, reg: reg}}}
	r, err := NewMoveStepRunner(reg, moveTestNode, rs)
	if err != nil {
		t.Fatalf("NewMoveStepRunner: %v", err)
	}
	if err := r.SettleMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseCopy, false)); !errors.Is(err, ErrPoolNotConfigured) {
		t.Fatalf("settle without pools = %v, want ErrPoolNotConfigured", err)
	}
}

func TestMoveStepRunner_CloseFailureTouchesNothing(t *testing.T) {
	ctx := context.Background()
	for _, step := range []volume.MoveStep{
		runnerStep(volume.MoveStepPhaseCopy, false),
		runnerStep(volume.MoveStepPhaseAbort, true),
	} {
		t.Run(step.Phase, func(t *testing.T) {
			fx, stores, _ := newRunnerFixture(t)
			cs := &closeFailStores{fakeStores: stores, err: errors.New("close refused")}
			r := newRunnerWith(t, fx, cs)
			if err := r.RunMoveStep(ctx, step); !errors.Is(err, cs.err) {
				t.Fatalf("step = %v, want the close error", err)
			}
			assertGone(t, fx.dst())
			mustExist(t, fx.src)
		})
	}
}

func TestMoveStepRunner_FailedCopyThatCannotReopenReportsBoth(t *testing.T) {
	_, stores, r := newRunnerFixture(t)
	stores.openErr = errors.New("open refused")
	same := runnerStep(volume.MoveStepPhaseCopy, false)
	same.ToPool = "default"
	err := r.RunMoveStep(context.Background(), same)
	if !errors.Is(err, ErrMoveSamePool) || !errors.Is(err, stores.openErr) {
		t.Fatalf("copy = %v, want the copy refusal joined with the open error", err)
	}
}

func TestMoveStepRunner_CopyBackWithoutDestinationRefused(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	err := r.RunMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseAbort, true))
	var stepErr *volume.MoveStepError
	if !errors.As(err, &stepErr) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("copy back with no destination = %v, want a *MoveStepError wrapping fs.ErrNotExist", err)
	}
	mustExist(t, fx.src)
	_ = stores
}

func TestMoveStepRunner_CopyBackRenameFailureKeepsDestination(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	renameErr := errors.New("rename failed")
	r.rename = func(string, string) error { return renameErr }
	abort := runnerStep(volume.MoveStepPhaseAbort, true)
	if err := r.RunMoveStep(ctx, abort); !errors.Is(err, renameErr) {
		t.Fatalf("copy back = %v, want the rename error", err)
	}
	if stores.open != fx.dst() {
		t.Fatalf("open = %q, want the destination still serving", stores.open)
	}
	stores.openErr = errors.New("open refused")
	err := r.RunMoveStep(ctx, abort)
	if !errors.Is(err, renameErr) || !errors.Is(err, stores.openErr) {
		t.Fatalf("copy back = %v, want the rename error joined with the open error", err)
	}
	mustExist(t, fx.dst())
}

func TestMoveRunner_ErrorStringsNameTheirSubject(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{&MoveStepNodeError{StepNode: "n2", LocalNode: "n1"}, []string{"n2", "n1"}},
		{&VolumeTenantRootMismatchError{TenantID: "acme", Registered: "/a", Want: "/b"}, []string{"acme", "/a", "/b"}},
		{&TenantMoveHeldError{TenantID: "acme"}, []string{"acme"}},
	} {
		msg := tc.err.Error()
		for _, w := range tc.want {
			if !strings.Contains(msg, w) {
				t.Errorf("%T.Error() = %q, want it to name %q", tc.err, msg, w)
			}
		}
	}
}

func TestBarrierRegistry_TenantBarrierSealedOfUnknownTenant(t *testing.T) {
	fx, _, _ := newRunnerFixture(t)
	if fx.reg.TenantBarrierSealed("ghost") {
		t.Fatal("an unregistered tenant reports a sealed barrier")
	}
}

func TestBarrierRegistry_ReopenVolumeTenantOfUnknownTenantFails(t *testing.T) {
	fx, _, _ := newRunnerFixture(t)
	if err := fx.reg.ReopenVolumeTenant(context.Background(), "ghost"); err == nil {
		t.Fatal("reopening an unknown tenant succeeded")
	}
}

// pathFailRemover is the wrapped remover (the OS one when nil) except that
// removing anything at or under fail returns EIO: a data root that cannot
// be deleted.
type pathFailRemover struct {
	DirectoryRemover
	fail     string
	readFail bool // listing fail fails too
}

func (f pathFailRemover) next() DirectoryRemover {
	if f.DirectoryRemover == nil {
		return osDirectoryRemover{}
	}
	return f.DirectoryRemover
}

func (f pathFailRemover) ReadDir(path string) ([]os.DirEntry, error) {
	if f.readFail && f.hit(path) {
		return nil, &os.PathError{Op: "readdirent", Path: path, Err: syscall.EIO}
	}
	return f.next().ReadDir(path)
}

func (f pathFailRemover) hit(path string) bool {
	p := filepath.Clean(path)
	return p == f.fail || strings.HasPrefix(p, f.fail+string(filepath.Separator))
}

func (f pathFailRemover) Remove(path string) error {
	if f.hit(path) {
		return &os.PathError{Op: "remove", Path: path, Err: syscall.EIO}
	}
	return f.next().Remove(path)
}

func (f pathFailRemover) RemoveAll(path string) error {
	if f.hit(path) {
		return &os.PathError{Op: "unlinkat", Path: path, Err: syscall.EIO}
	}
	return f.next().RemoveAll(path)
}

// corruptManifest rewrites the copied tree's manifest so VerifyCopy fails.
func corruptManifest(t *testing.T) func(dst string) {
	return func(dst string) {
		if err := os.WriteFile(filepath.Join(dst, "MANIFEST-000001"), []byte("corrupted"), 0o600); err != nil {
			t.Errorf("corrupt %s: %v", dst, err)
		}
	}
}

func settledCopy(t *testing.T, r *MoveStepRunner) {
	t.Helper()
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
}

func TestMoveStepRunner_CopyRedoOverUndeletableDestinationFails(t *testing.T) {
	fx, _, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	fx.reg.remover = pathFailRemover{fail: filepath.Clean(fx.dst())}
	var stepErr *volume.MoveStepError
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); !errors.As(err, &stepErr) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("copy redo = %v, want a *MoveStepError wrapping EIO", err)
	}
	mustExist(t, filepath.Join(fx.src, "MANIFEST-000001"))
}

func TestMoveStepRunner_FailedVerifyWithUndeletableDestinationReportsBoth(t *testing.T) {
	fx, _, r := newRunnerFixture(t)
	fx.reg.remover = pathFailRemover{fail: filepath.Clean(fx.dst())}
	fx.reg.moveAfterCopy = corruptManifest(t)
	err := r.RunMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseCopy, false))
	var stepErr *volume.MoveStepError
	if !errors.As(err, &stepErr) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("copy = %v, want the verify failure joined with the removal error", err)
	}
	mustExist(t, filepath.Join(fx.src, "MANIFEST-000001"))
}

func TestMoveStepRunner_CopyBackUndeletableSourceKeepsDestination(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	settledCopy(t, r)
	fx.reg.remover = pathFailRemover{fail: filepath.Clean(fx.src)}
	if err := r.RunMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseAbort, true)); !errors.Is(err, syscall.EIO) {
		t.Fatalf("copy back = %v, want the source removal error", err)
	}
	if stores.open != fx.dst() {
		t.Fatalf("open = %q, want the destination still serving", stores.open)
	}
}

func TestMoveStepRunner_CopyBackUndeletableStaging(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	settledCopy(t, r)
	staging := filepath.Join(fx.defaultRoot, copyBackStagingName(fx.path))
	fx.reg.remover = pathFailRemover{fail: filepath.Clean(staging)}
	fx.reg.moveAfterCopy = corruptManifest(t)
	abort := runnerStep(volume.MoveStepPhaseAbort, true)
	if err := r.RunMoveStep(ctx, abort); !errors.Is(err, syscall.EIO) {
		t.Fatalf("copy back with a corrupt staging copy = %v, want the staging removal error joined", err)
	}
	mustExist(t, staging)
	fx.reg.moveAfterCopy = nil
	if err := r.RunMoveStep(ctx, abort); !errors.Is(err, syscall.EIO) {
		t.Fatalf("copy back over a leftover staging = %v, want the staging removal error", err)
	}
	if stores.open != fx.dst() {
		t.Fatalf("open = %q, want the destination still serving", stores.open)
	}
}

func TestMoveStepRunner_MountedSwapRemovalFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(fx *moveFixture) string
	}{
		{"in-place staging undeletable", func(fx *moveFixture) string {
			return filepath.Join(filepath.Clean(fx.src), copyBackInPlaceName)
		}},
		{"copy-back staging undeletable", func(fx *moveFixture) string {
			return filepath.Join(fx.defaultRoot, copyBackStagingName(fx.path))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, _, r := newRunnerFixture(t)
			settledCopy(t, r)
			r.rename = mountPointRename(filepath.Clean(fx.src), 0)
			root := filepath.Clean(fx.src)
			fx.reg.remover = pathFailRemover{DirectoryRemover: mountedSourceRemover{root: root}, fail: filepath.Clean(tc.fail(fx))}
			if err := r.RunMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseAbort, true)); !errors.Is(err, syscall.EIO) {
				t.Fatalf("copy back = %v, want the removal error", err)
			}
			mustExist(t, fx.dst())
		})
	}
}

func TestMoveStepRunner_UncopiedAbortOfUnresolvableTenantFails(t *testing.T) {
	fx, stores, _ := newRunnerFixture(t)
	r := newRunnerWith(t, fx, &noOpenRelocator{&relocatingStores{fakeStores: stores}})
	ghost := runnerStep(volume.MoveStepPhaseAbort, false)
	ghost.TenantID = "ghost"
	if err := r.RunMoveStep(context.Background(), ghost); err == nil {
		t.Fatal("abort of a tenant with no pool assignment succeeded")
	}
}

// noOpenSnapshots relocates snapshots only, with no-op open and close.
type noOpenSnapshots struct{ *snapshotStores }

func (n *noOpenSnapshots) OpenTenantStores(context.Context, string) error  { return nil }
func (n *noOpenSnapshots) CloseTenantStores(context.Context, string) error { return nil }

func TestMoveStepRunner_SnapshotRelocationOfUnresolvableTenantFails(t *testing.T) {
	fx, stores, _ := newRunnerFixture(t)
	ss := &snapshotStores{fakeStores: stores}
	r := newRunnerWith(t, fx, &noOpenSnapshots{ss})
	ghost := runnerStep(volume.MoveStepPhaseCopy, false)
	ghost.TenantID = "ghost"
	if err := r.SettleMoveStep(context.Background(), ghost); err == nil {
		t.Fatal("settle of a tenant with no pool assignment succeeded")
	}
	if len(ss.roots) != 0 {
		t.Fatalf("relocated snapshots %v for an unresolvable tenant", ss.roots)
	}
}

func TestRemoveDataRoot_UnlistableRootFailsAndKeepsIt(t *testing.T) {
	fx := newMoveFixture(t)
	fx.reg.remover = pathFailRemover{fail: filepath.Clean(fx.src), readFail: true}
	if err := fx.reg.RemoveDataRoot(context.Background(), fx.src); !errors.Is(err, syscall.EIO) {
		t.Fatalf("RemoveDataRoot of an unlistable root = %v, want EIO", err)
	}
	mustExist(t, filepath.Join(fx.src, "MANIFEST-000001"))
}

func TestRemoveDataRoot_WithoutPoolsRefused(t *testing.T) {
	reg, err := NewBarrierRegistry(poolTestLogger(), newPoolSystemBarrier(t))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	dir := t.TempDir()
	if err := reg.RemoveDataRoot(context.Background(), dir); err == nil {
		t.Fatal("RemoveDataRoot with no pools configured succeeded")
	}
	mustExist(t, dir)
}

func TestRequirePathInsideLocalPools_MissingPoolBaseContainsNothing(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing")
	catalog, open := newTestVolumeFixtureAtAlias(t, filepath.Join(base, "real"), missing)
	reg, err := NewBarrierRegistry(poolTestLogger(), newPoolSystemBarrier(t), WithVolumeAllocator(catalog), WithVolumeResolver(open))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	var outside *DataRootOutsideLocalRootsError
	if err := reg.RequirePathInsideLocalPools(context.Background(), filepath.Join(missing, "acme")); !errors.As(err, &outside) {
		t.Fatalf("path under a pool base that does not resolve = %v, want *DataRootOutsideLocalRootsError", err)
	}
}
