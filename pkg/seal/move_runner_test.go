// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// fakeStores is a faithful TenantStoreCloser double: a reopen opens the
// directory the registry resolves at that moment, as the real barrier and
// shard reopen do, and writes go to the directory that is open.
type fakeStores struct {
	t       *testing.T
	reg     *BarrierRegistry
	open    string
	opens   []string
	closes  int
	openErr error
}

func (f *fakeStores) CloseTenantStores(context.Context, string) error {
	f.closes++
	f.open = ""
	return nil
}

func (f *fakeStores) OpenTenantStores(ctx context.Context, tenantID string) error {
	if f.openErr != nil {
		return f.openErr
	}
	root, err := f.reg.RequireInsideLocalPool(ctx, tenantID)
	if err != nil {
		return err
	}
	f.open = root
	f.opens = append(f.opens, root)
	return nil
}

// write records one acknowledged write in the open directory.
func (f *fakeStores) write(name string) {
	f.t.Helper()
	if f.open == "" {
		f.t.Fatalf("write %s with no store open", name)
	}
	if err := os.WriteFile(filepath.Join(f.open, name), []byte(name), 0o600); err != nil {
		f.t.Fatalf("write %s: %v", name, err)
	}
}

func newRunnerFixture(t *testing.T) (*moveFixture, *fakeStores, *MoveStepRunner) {
	t.Helper()
	fx := newMoveFixture(t)
	if err := fx.reg.system.Initialize(context.Background(), MustCredentials(context.Background(), []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	stores := &fakeStores{t: t, reg: fx.reg}
	if err := stores.OpenTenantStores(context.Background(), moveTestTenant); err != nil {
		t.Fatalf("open: %v", err)
	}
	r, err := NewMoveStepRunner(fx.reg, moveTestNode, stores)
	if err != nil {
		t.Fatalf("NewMoveStepRunner: %v", err)
	}
	return fx, stores, r
}

func runnerStep(phase string, copied bool) volume.MoveStep {
	return volume.MoveStep{TenantID: moveTestTenant, NodeID: moveTestNode, FromPool: "default", ToPool: "fast", Attempt: 1, Phase: phase, Copied: copied}
}

func (fx *moveFixture) dst() string { return filepath.Join(fx.fastRoot, fx.path) }

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("%s: %v", filepath.Base(p), err)
	}
}

func TestMoveStepRunner_CopyReopensFromDestination(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	if err := r.RunMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	// Before the journal CAS nothing serves from the destination.
	if stores.closes != 1 || stores.open != "" {
		t.Fatalf("closes = %d, open = %q, want 1 close and nothing open before the CAS", stores.closes, stores.open)
	}
	mustExist(t, filepath.Join(fx.dst(), "MANIFEST-000001"))
	mustExist(t, fx.src)
	if err := r.SettleMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	if stores.open != fx.dst() {
		t.Fatalf("open after settle = %q, want the destination %q", stores.open, fx.dst())
	}
}

// barrierStores is a TenantStoreCloser over the registry's own volume tenant
// barrier, the close and reopen a running node's MoveStoreCloser ends in:
// CloseVolumeTenant, then ReopenVolumeTenant and a credential-free unseal of
// the system-managed tenant.
type barrierStores struct {
	reg  *BarrierRegistry
	open string
}

func (s *barrierStores) CloseTenantStores(ctx context.Context, tenantID string) error {
	return s.reg.CloseVolumeTenant(ctx, tenantID)
}

func (s *barrierStores) OpenTenantStores(ctx context.Context, tenantID string) error {
	if err := s.reg.ReopenVolumeTenant(ctx, tenantID); err != nil {
		return err
	}
	tb, err := s.reg.GetTenant(tenantID)
	if err != nil {
		return err
	}
	if tb.IsSealed() {
		return ErrSealed // the reopen must unseal an auto-unseal tenant itself
	}
	root, err := s.reg.RequireInsideLocalPool(ctx, tenantID)
	if err != nil {
		return err
	}
	s.open = root
	return nil
}

// TestMoveStepRunner_CopyMovesOpenPebbleStore drives the copy step over a
// volume tenant whose barrier lives in a real per-tenant Pebble store, as on a
// running node: the copy must close that store at the source, so the reopen
// from the destination opens a fresh store there and reads back the record
// written before the move.
func TestMoveStepRunner_CopyMovesOpenPebbleStore(t *testing.T) {
	ctx := context.Background()
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })
	fx := newMoveFixture(t, WithPerTenantRegistry(perTenantReg))
	if err := fx.reg.system.Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	_, tb, err := fx.reg.RegisterVolumeTenant(ctx, moveTestTenant, true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := tb.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("tenant Initialize: %v", err)
	}
	if err := tb.Barrier().Put(ctx, "move/record", []byte("before-move")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	assertPebbleAtPath(t, filepath.Join(fx.src, "barrier"))

	stores := &barrierStores{reg: fx.reg}
	r, err := NewMoveStepRunner(fx.reg, moveTestNode, stores)
	if err != nil {
		t.Fatalf("NewMoveStepRunner: %v", err)
	}
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if _, err := fx.reg.GetTenant(moveTestTenant); err == nil {
		t.Fatal("GetTenant before the copy CAS: the tenant is open, want it closed")
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	if stores.open != fx.dst() {
		t.Fatalf("open = %q, want the destination %q", stores.open, fx.dst())
	}
	assertPebbleAtPath(t, filepath.Join(fx.dst(), "barrier"))
	moved, err := fx.reg.GetTenant(moveTestTenant)
	if err != nil {
		t.Fatalf("GetTenant after copy: %v", err)
	}
	got, err := moved.Barrier().Get(ctx, "move/record")
	if err != nil {
		t.Fatalf("Get after copy: %v", err)
	}
	if string(got) != "before-move" {
		t.Fatalf("record = %q, want %q", got, "before-move")
	}
	if err := moved.Barrier().Put(ctx, "move/after", []byte("after-move")); err != nil {
		t.Fatalf("Put after copy: %v", err)
	}
}

// TestMoveStepRunner_FailedCopyCASKeepsEveryWrite: a copy whose journal
// write fails (or a crash before it) leaves the destination closed, so no
// write lands on it; the source still holds every acknowledged write when
// the move rolls back to it.
func TestMoveStepRunner_FailedCopyCASKeepsEveryWrite(t *testing.T) {
	ctx := context.Background()
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })
	fx := newMoveFixture(t, WithPerTenantRegistry(perTenantReg))
	if err := fx.reg.system.Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	_, tb, err := fx.reg.RegisterVolumeTenant(ctx, moveTestTenant, true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := tb.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("tenant Initialize: %v", err)
	}
	if err := tb.Barrier().Put(ctx, "move/acked", []byte("acknowledged")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	stores := &barrierStores{reg: fx.reg}
	r, err := NewMoveStepRunner(fx.reg, moveTestNode, stores)
	if err != nil {
		t.Fatalf("NewMoveStepRunner: %v", err)
	}
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	// The CAS fails here: SettleMoveStep never runs.
	if _, err := fx.reg.GetTenant(moveTestTenant); err == nil {
		t.Fatal("GetTenant after a copy without its CAS: open, want closed (no write may land on the destination)")
	}
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseAbort, false)); err != nil {
		t.Fatalf("abort uncopied: %v", err)
	}
	if stores.open != fx.src {
		t.Fatalf("open = %q, want the source %q", stores.open, fx.src)
	}
	back, err := fx.reg.GetTenant(moveTestTenant)
	if err != nil {
		t.Fatalf("GetTenant after abort: %v", err)
	}
	got, err := back.Barrier().Get(ctx, "move/acked")
	if err != nil || string(got) != "acknowledged" {
		t.Fatalf("Get after abort = %q, %v; want %q", got, err, "acknowledged")
	}
}

// TestBarrierRegistry_MoveHoldRefusesFreshRegistration pins the hold a move
// step's close puts on a volume tenant: until the step's reopen, a fresh
// registration (the placement reconciler's barrier shell) is refused with
// *TenantMoveHeldError, and the reopen itself re-attaches the tenant
// unsealed, ending the hold.
func TestBarrierRegistry_MoveHoldRefusesFreshRegistration(t *testing.T) {
	ctx := context.Background()
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })
	fx := newMoveFixture(t, WithPerTenantRegistry(perTenantReg))
	if err := fx.reg.system.Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	_, tb, err := fx.reg.RegisterVolumeTenant(ctx, moveTestTenant, true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := tb.Initialize(ctx, Credentials{}); err != nil {
		t.Fatalf("tenant Initialize: %v", err)
	}

	if err := fx.reg.CloseVolumeTenant(ctx, moveTestTenant); err != nil {
		t.Fatalf("CloseVolumeTenant: %v", err)
	}
	_, _, err = fx.reg.RegisterVolumeTenant(ctx, moveTestTenant, true)
	var held *TenantMoveHeldError
	if !errors.As(err, &held) || held.TenantID != moveTestTenant {
		t.Fatalf("RegisterVolumeTenant while held = %v, want *TenantMoveHeldError for %q", err, moveTestTenant)
	}

	if err := fx.reg.ReopenVolumeTenant(ctx, moveTestTenant); err != nil {
		t.Fatalf("ReopenVolumeTenant: %v", err)
	}
	reopened, err := fx.reg.GetTenant(moveTestTenant)
	if err != nil {
		t.Fatalf("GetTenant after reopen: %v", err)
	}
	if reopened.IsSealed() {
		t.Fatal("auto-unseal tenant is sealed after the reopen, want unsealed")
	}
	if err := fx.reg.CloseVolumeTenant(ctx, moveTestTenant); err != nil {
		t.Fatalf("second CloseVolumeTenant: %v", err)
	}
	fx.reg.moveHeld.Delete(moveTestTenant)
	if _, _, err := fx.reg.RegisterVolumeTenant(ctx, moveTestTenant, true); err != nil {
		t.Fatalf("RegisterVolumeTenant after the hold ended: %v", err)
	}
}

// newReopenFixture registers moveTestTenant through its volume pool with
// the given posture, initializes it and writes one record.
func newReopenFixture(t *testing.T, autoUnseal bool) *moveFixture {
	t.Helper()
	ctx := context.Background()
	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })
	fx := newMoveFixture(t, WithPerTenantRegistry(perTenantReg))
	if err := fx.reg.system.Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	_, tb, err := fx.reg.RegisterVolumeTenant(ctx, moveTestTenant, autoUnseal)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	creds := Credentials{}
	if !autoUnseal {
		creds = MustCredentials(ctx, []byte("tenant-pass"))
	}
	if err := tb.Initialize(ctx, creds); err != nil {
		t.Fatalf("tenant Initialize: %v", err)
	}
	if tb.IsSealed() {
		if err := tb.Unseal(ctx, creds); err != nil {
			t.Fatalf("tenant Unseal: %v", err)
		}
	}
	if err := tb.Barrier().Put(ctx, "reopen/record", []byte("kept")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return fx
}

// assertReopened checks the hold ended and the tenant's seal state.
func assertReopened(t *testing.T, fx *moveFixture, wantSealed bool) *TenantBarrier {
	t.Helper()
	if _, held := fx.reg.moveHeld.Load(moveTestTenant); held {
		t.Fatal("move hold still set after ReopenVolumeTenant")
	}
	tb, err := fx.reg.GetTenant(moveTestTenant)
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if tb.IsSealed() != wantSealed {
		t.Fatalf("sealed = %v, want %v", tb.IsSealed(), wantSealed)
	}
	return tb
}

// A settle pass that reopened the tenant but failed to release its step
// reopens it again: the second ReopenVolumeTenant reuses the registered
// barrier instead of failing on ErrTenantAlreadyExists.
func TestReopenVolumeTenant_TwiceAutoUnseal(t *testing.T) {
	ctx := context.Background()
	fx := newReopenFixture(t, true)
	if err := fx.reg.CloseVolumeTenant(ctx, moveTestTenant); err != nil {
		t.Fatalf("CloseVolumeTenant: %v", err)
	}
	for i := range 2 {
		if err := fx.reg.ReopenVolumeTenant(ctx, moveTestTenant); err != nil {
			t.Fatalf("ReopenVolumeTenant #%d: %v", i+1, err)
		}
		tb := assertReopened(t, fx, false)
		got, err := tb.Barrier().Get(ctx, "reopen/record")
		if err != nil || string(got) != "kept" {
			t.Fatalf("read after reopen #%d = %q, %v; want kept", i+1, got, err)
		}
	}
	// An auto-unseal tenant sealed while registered is unsealed again.
	if err := fx.reg.SealTenant(moveTestTenant); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := fx.reg.ReopenVolumeTenant(ctx, moveTestTenant); err != nil {
		t.Fatalf("ReopenVolumeTenant of a sealed registered tenant: %v", err)
	}
	assertReopened(t, fx, false)
}

// A self-managed tenant stays sealed through both reopens.
func TestReopenVolumeTenant_TwiceSelfManagedStaysSealed(t *testing.T) {
	ctx := context.Background()
	fx := newReopenFixture(t, false)
	if err := fx.reg.CloseVolumeTenant(ctx, moveTestTenant); err != nil {
		t.Fatalf("CloseVolumeTenant: %v", err)
	}
	for i := range 2 {
		if err := fx.reg.ReopenVolumeTenant(ctx, moveTestTenant); err != nil {
			t.Fatalf("ReopenVolumeTenant #%d: %v", i+1, err)
		}
		assertReopened(t, fx, true)
	}
}

// A restart restores the tenant while its journal still records the step
// settling; the settle pass's ReopenVolumeTenant then finds it registered
// at the same root and succeeds, and the tenant serves reads.
func TestReopenVolumeTenant_AfterRestoreVolumeTenants(t *testing.T) {
	ctx := context.Background()
	fx := newReopenFixture(t, true)
	if err := fx.reg.CloseVolumeTenant(ctx, moveTestTenant); err != nil {
		t.Fatalf("CloseVolumeTenant: %v", err)
	}
	fx.reg.moveHeld.Delete(moveTestTenant) // the restart drops the memory-only hold
	if err := fx.reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	if _, err := fx.reg.GetTenant(moveTestTenant); err != nil {
		t.Fatalf("GetTenant after restore: %v", err)
	}
	if err := fx.reg.ReopenVolumeTenant(ctx, moveTestTenant); err != nil {
		t.Fatalf("ReopenVolumeTenant after restore: %v", err)
	}
	tb := assertReopened(t, fx, false)
	got, err := tb.Barrier().Get(ctx, "reopen/record")
	if err != nil || string(got) != "kept" {
		t.Fatalf("read after restore and reopen = %q, %v; want kept", got, err)
	}
}

// A barrier registered at the other pool's directory is refused rather
// than served from the wrong directory.
func TestReopenVolumeTenant_RefusesBarrierOnOtherPool(t *testing.T) {
	ctx := context.Background()
	fx := newReopenFixture(t, true) // registered at the source
	fx.putJournal(t, journal(volume.MoveStateCopying, moveTestNode))
	fx.reg.SetVolumeNodeID(moveTestNode) // now resolves to the destination
	err := fx.reg.ReopenVolumeTenant(ctx, moveTestTenant)
	var mm *VolumeTenantRootMismatchError
	if !errors.As(err, &mm) || mm.Registered != fx.src || mm.Want != fx.dst() {
		t.Fatalf("ReopenVolumeTenant = %v, want *VolumeTenantRootMismatchError registered %q want %q", err, fx.src, fx.dst())
	}
}

func TestMoveStepRunner_FailedCopyReopensFromSource(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	truncateOnCopy(t, fx.reg)
	err := r.RunMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseCopy, false))
	var se *volume.MoveStepError
	if !errors.As(err, &se) {
		t.Fatalf("copy = %v, want *volume.MoveStepError", err)
	}
	if stores.open != fx.src {
		t.Fatalf("open = %q, want the source", stores.open)
	}
	assertGone(t, fx.dst())
}

func TestMoveStepRunner_SingleReplicaAbortKeepsEveryWrite(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	stores.write("w1")
	stores.write("w2")
	abort := runnerStep(volume.MoveStepPhaseAbort, true)
	if err := r.RunMoveStep(ctx, abort); err != nil {
		t.Fatalf("abort: %v", err)
	}
	if stores.open != "" {
		t.Fatalf("open = %q, want closed until the journal CAS", stores.open)
	}
	if err := r.SettleMoveStep(ctx, abort); err != nil {
		t.Fatalf("settle abort: %v", err)
	}
	if stores.open != fx.src {
		t.Fatalf("open = %q, want the source", stores.open)
	}
	for _, w := range []string{"w1", "w2", "MANIFEST-000001"} {
		mustExist(t, filepath.Join(fx.src, w))
	}
	assertGone(t, fx.dst())
}

func TestMoveStepRunner_CorruptCopyBackKeepsDestination(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	stores.write("w1")
	truncateOnCopy(t, fx.reg)
	err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseAbort, true))
	var se *volume.MoveStepError
	if !errors.As(err, &se) || se.Phase != volume.MoveStepPhaseAbort {
		t.Fatalf("abort = %v, want an abort *volume.MoveStepError", err)
	}
	if stores.open != fx.dst() {
		t.Fatalf("open = %q, want the destination, not the source", stores.open)
	}
	mustExist(t, filepath.Join(fx.dst(), "w1"))
}

// TestMoveStepRunner_CopyBackFromUnreadableDestinationKeepsSource is the
// rollback of a destination that cannot be read back intact: the copy-back
// stages the destination's tree in the source pool and verifies it there,
// so a failed verify removes only the staging directory and the source
// stays byte for byte as it was.
func TestMoveStepRunner_CopyBackFromUnreadableDestinationKeepsSource(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	before := mustManifest(t, fx.src)
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	stores.write("w1")
	truncateOnCopy(t, fx.reg)
	err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseAbort, true))
	var se *volume.MoveStepError
	if !errors.As(err, &se) || se.Phase != volume.MoveStepPhaseAbort {
		t.Fatalf("abort = %v, want an abort *volume.MoveStepError", err)
	}
	if after := mustManifest(t, fx.src); !reflect.DeepEqual(after, before) {
		t.Fatalf("source changed by a failed copy-back")
	}
	assertGone(t, filepath.Join(fx.defaultRoot, copyBackStagingName(fx.path)))
	mustExist(t, filepath.Join(fx.dst(), "w1"))
}

// TestMoveStepRunner_CopyBackRerunAfterSourceRemoved is a crash between the
// source's removal and the staging directory's rename: the rerun removes
// the stale staging directory, treats the missing source as removed and
// rebuilds the source from the destination, which stayed authoritative.
func TestMoveStepRunner_CopyBackRerunAfterSourceRemoved(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	stores.write("w1")
	if err := os.RemoveAll(fx.src); err != nil {
		t.Fatalf("remove source: %v", err)
	}
	staging := filepath.Join(fx.defaultRoot, copyBackStagingName(fx.path))
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatalf("stale staging: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staging, "partial"), []byte("x"), 0o600); err != nil {
		t.Fatalf("stale staging file: %v", err)
	}
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseAbort, true)); err != nil {
		t.Fatalf("abort rerun: %v", err)
	}
	if got, want := mustManifest(t, fx.src), mustManifest(t, fx.dst()); !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt source differs from the destination")
	}
	mustExist(t, filepath.Join(fx.src, "w1"))
	assertGone(t, staging)
}

// mountedSourceRemover is an OS remover for which root is a mount point: its
// children go, but rmdir of root itself fails with EBUSY, as the kernel does.
type mountedSourceRemover struct {
	osDirectoryRemover
	root string
}

func (m mountedSourceRemover) Remove(path string) error {
	if filepath.Clean(path) == m.root {
		return &os.PathError{Op: "remove", Path: path, Err: syscall.EBUSY}
	}
	return os.Remove(path)
}

func (m mountedSourceRemover) RemoveAll(path string) error {
	if filepath.Clean(path) != m.root {
		return os.RemoveAll(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(path, e.Name())); err != nil {
			return err
		}
	}
	return &os.PathError{Op: "unlinkat", Path: path, Err: syscall.EBUSY}
}

// mountPointRename models rename(2) when root is a mount point: renaming root
// itself, or onto it, fails with EBUSY, and a rename whose ends lie on
// different sides of the mount fails with EXDEV. failAt, when positive, makes
// the failAt-th otherwise-allowed rename into root fail once with EIO, a crash
// part way through a swap.
func mountPointRename(root string, failAt int) func(oldpath, newpath string) error {
	inside := func(p string) bool {
		return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
	}
	n := 0
	return func(oldpath, newpath string) error {
		o, d := filepath.Clean(oldpath), filepath.Clean(newpath)
		if o == root || d == root {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EBUSY}
		}
		if inside(o) != inside(d) {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EXDEV}
		}
		if inside(d) {
			n++
			if n == failAt {
				return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.EIO}
			}
		}
		return fsync.Rename(oldpath, newpath)
	}
}

func TestMoveStepRunner_CopyBackOntoMountPointSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failAt int
	}{
		{name: "clean", failAt: 0},
		{name: "rerun after a partial swap", failAt: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx, stores, r := newRunnerFixture(t)
			ctx := context.Background()
			if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
				t.Fatalf("copy: %v", err)
			}
			if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
				t.Fatalf("settle copy: %v", err)
			}
			stores.write("w1")
			stores.write("w2")
			root := filepath.Clean(fx.src)
			fx.reg.remover = mountedSourceRemover{root: root}
			r.rename = mountPointRename(root, tc.failAt)
			before, err := os.Stat(root)
			if err != nil {
				t.Fatalf("stat source: %v", err)
			}
			abort := runnerStep(volume.MoveStepPhaseAbort, true)
			if tc.failAt > 0 {
				if err := r.RunMoveStep(ctx, abort); err == nil {
					t.Fatalf("abort with a failing swap rename succeeded")
				}
			}
			staging := filepath.Join(fx.defaultRoot, copyBackStagingName(fx.path))
			check := func(what string) {
				t.Helper()
				if got, want := mustManifest(t, root), mustManifest(t, fx.dst()); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: source differs from the destination", what)
				}
				mustExist(t, filepath.Join(root, "w1"))
				mustExist(t, filepath.Join(root, "w2"))
				assertGone(t, staging)
				assertGone(t, filepath.Join(root, copyBackInPlaceName))
				after, err := os.Stat(root)
				if err != nil {
					t.Fatalf("%s: stat source: %v", what, err)
				}
				if !os.SameFile(before, after) {
					t.Fatalf("%s: the mount-point source root was replaced, not kept", what)
				}
			}
			if err := r.RunMoveStep(ctx, abort); err != nil {
				t.Fatalf("abort: %v", err)
			}
			check("abort")
			if err := r.RunMoveStep(ctx, abort); err != nil {
				t.Fatalf("abort rerun: %v", err)
			}
			check("abort rerun")
		})
	}
}

// TestMoveStepRunner_AbortRedoneBeforeCAS is the first abort crash point:
// after the copy-back verifies and before the journal CAS, the node redoes
// the abort step and finds the destination intact.
func TestMoveStepRunner_AbortRedoneBeforeCAS(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	stores.write("w1")
	abort := runnerStep(volume.MoveStepPhaseAbort, true)
	for i := 0; i < 2; i++ {
		if err := r.RunMoveStep(ctx, abort); err != nil {
			t.Fatalf("abort run %d: %v", i, err)
		}
		mustExist(t, filepath.Join(fx.dst(), "w1"))
	}
	mustExist(t, filepath.Join(fx.src, "w1"))
}

func TestMoveStepRunner_AbortUncopiedRemovesPartialDestination(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	if err := os.MkdirAll(filepath.Join(fx.dst(), "partial"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := stores.CloseTenantStores(context.Background(), moveTestTenant); err != nil {
		t.Fatal(err)
	}
	if err := r.RunMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseAbort, false)); err != nil {
		t.Fatalf("abort: %v", err)
	}
	assertGone(t, fx.dst())
	if stores.open != fx.src {
		t.Fatalf("open = %q, want the source", stores.open)
	}
	// Nothing to remove the second time.
	if err := r.RunMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseAbort, false)); err != nil {
		t.Fatalf("abort again: %v", err)
	}
}

func TestMoveStepRunner_RetireRemovesSource(t *testing.T) {
	fx, _, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseRetire, false)); err != nil {
			t.Fatalf("retire %d: %v", i, err)
		}
	}
	assertGone(t, fx.src)
	mustExist(t, filepath.Join(fx.dst(), "MANIFEST-000001"))
}

func TestMoveStepRunner_Refusals(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	other := runnerStep(volume.MoveStepPhaseCopy, false)
	other.NodeID = "other"
	var ne *MoveStepNodeError
	if err := r.RunMoveStep(ctx, other); !errors.As(err, &ne) || ne.StepNode != "other" {
		t.Fatalf("other node = %v, want *MoveStepNodeError", err)
	}
	if err := r.SettleMoveStep(ctx, other); !errors.As(err, &ne) {
		t.Fatalf("settle other node = %v, want *MoveStepNodeError", err)
	}
	bad := runnerStep("bogus", false)
	if err := r.RunMoveStep(ctx, bad); !errors.Is(err, ErrMoveStepInvalid) {
		t.Fatalf("bogus phase = %v, want ErrMoveStepInvalid", err)
	}
	empty := runnerStep(volume.MoveStepPhaseCopy, false)
	empty.ToPool = ""
	if err := r.RunMoveStep(ctx, empty); !errors.Is(err, ErrMoveStepInvalid) {
		t.Fatalf("empty pool = %v, want ErrMoveStepInvalid", err)
	}
	if _, err := NewMoveStepRunner(nil, moveTestNode, stores); !errors.Is(err, ErrMoveStepInvalid) {
		t.Fatalf("nil registry = %v", err)
	}
	if stores.closes != 0 {
		t.Fatalf("a refused step closed the stores %d times", stores.closes)
	}
	if err := fx.reg.system.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	var se *volume.NodeSealedError
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); !errors.As(err, &se) {
		t.Fatalf("sealed node = %v, want *volume.NodeSealedError", err)
	}
	if stores.closes != 0 {
		t.Fatalf("a sealed node's step closed the stores")
	}
}

// A sealed node's copy step is refused with *volume.NodeSealedError, which
// the reconciler treats as a wait (no attempt counted, no abort; proven by
// pkg/volume's TestMoveReconciler_SealedNodeStepWaits, as this fixture has
// no volume catalog). After the unseal the same step runs: the destination
// holds the copy and the settle reopens the stores on it.
func TestMoveStepRunner_SealedStepWaitsThenRuns(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := fx.reg.system.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	step := runnerStep(volume.MoveStepPhaseCopy, false)
	for i := 0; i < volume.DefaultMoveStepAttempts+1; i++ {
		var se *volume.NodeSealedError
		if err := r.RunMoveStep(ctx, step); !errors.As(err, &se) {
			t.Fatalf("sealed step #%d = %v, want *volume.NodeSealedError", i+1, err)
		}
	}
	if stores.closes != 0 {
		t.Fatalf("a sealed node's step closed the stores %d times", stores.closes)
	}
	assertGone(t, fx.dst())
	mustExist(t, fx.src)
	if err := fx.reg.system.Unseal(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if err := r.RunMoveStep(ctx, step); err != nil {
		t.Fatalf("copy after the unseal: %v", err)
	}
	mustExist(t, filepath.Join(fx.dst(), "MANIFEST-000001"))
	if err := r.SettleMoveStep(ctx, step); err != nil {
		t.Fatalf("settle copy after the unseal: %v", err)
	}
	if stores.open != fx.dst() {
		t.Fatalf("open after settle = %q, want the destination %q", stores.open, fx.dst())
	}
}

// unlockCounter is a Lockable resolver double: it records every Unlock.
type unlockCounter struct {
	VolumeResolver
	unlocks int
}

func (u *unlockCounter) Unlock(context.Context, string, []byte) error {
	u.unlocks++
	return nil
}

func TestMoveStepRunner_NoUnlock(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	counter := &unlockCounter{VolumeResolver: fx.open}
	fx.reg.poolMu.Lock()
	fx.reg.volumeResolver = counter
	fx.reg.poolMu.Unlock()
	for _, s := range []volume.MoveStep{
		runnerStep(volume.MoveStepPhaseCopy, false),
		runnerStep(volume.MoveStepPhaseAbort, true),
	} {
		if err := r.RunMoveStep(ctx, s); err != nil {
			t.Fatalf("%s: %v", s.Phase, err)
		}
		if err := r.SettleMoveStep(ctx, s); err != nil {
			t.Fatalf("settle %s: %v", s.Phase, err)
		}
	}
	if counter.unlocks != 0 {
		t.Fatalf("unlocks = %d, want none", counter.unlocks)
	}
	if stores.open != fx.src {
		t.Fatalf("open = %q, want the source", stores.open)
	}
}

// putJournal writes m as the tenant's move journal in the fixture's KV.
func (fx *moveFixture) putJournal(t *testing.T, m volume.Move) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.kv.Put(context.Background(), volume.MoveKey(m.TenantID), raw); err != nil {
		t.Fatalf("put journal: %v", err)
	}
}

func journal(state string, copied ...string) volume.Move {
	return volume.Move{TenantID: moveTestTenant, FromPool: "default", ToPool: "fast", State: state, NodesCopied: copied}
}

// TestMoveStepRunner_CrashAfterCopyCASOpensFromDestination: a node that
// crashed after its copy step's journal write (the in-memory override is
// gone) resolves the destination from the journal, and only that node.
func TestMoveStepRunner_CrashAfterCopyCASOpensFromDestination(t *testing.T) {
	fx, _, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	fx.putJournal(t, journal(volume.MoveStateCopying, moveTestNode))
	fx.reg.moveOpenPools.Delete(moveTestTenant) // the crash
	if root, err := fx.reg.RequireInsideLocalPool(ctx, moveTestTenant); err != nil || root != fx.src {
		t.Fatalf("no node ID: root = %q, %v; want the source", root, err)
	}
	fx.reg.SetVolumeNodeID(moveTestNode)
	if root, err := fx.reg.RequireInsideLocalPool(ctx, moveTestTenant); err != nil || root != fx.dst() {
		t.Fatalf("copied node: root = %q, %v; want the destination", root, err)
	}
	fx.reg.SetVolumeNodeID("other")
	if root, err := fx.reg.RequireInsideLocalPool(ctx, moveTestTenant); err != nil || root != fx.src {
		t.Fatalf("other node: root = %q, %v; want the source", root, err)
	}
}

// TestMoveStepRunner_BootAfterAbortCASRemovesDestination covers the abort
// crash points after the journal write: before the reopen and before the
// destination's removal. Boot opens the source (the journal no longer
// lists the node as copied) and removes the leftover destination; a node
// the journal still serves from the destination keeps it.
func TestMoveStepRunner_BootAfterAbortCASRemovesDestination(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
	}
	stores.write("w1")
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseAbort, true)); err != nil {
		t.Fatalf("abort: %v", err)
	}
	fx.reg.moveOpenPools.Delete(moveTestTenant) // the crash
	fx.reg.SetVolumeNodeID(moveTestNode)

	fx.putJournal(t, journal(volume.MoveStateAborting, moveTestNode))
	if err := fx.reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("restore, still copied: %v", err)
	}
	mustExist(t, filepath.Join(fx.dst(), "w1"))

	fx.putJournal(t, journal(volume.MoveStateAborting))
	if err := fx.reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("restore after the abort CAS: %v", err)
	}
	assertGone(t, fx.dst())
	mustExist(t, filepath.Join(fx.src, "w1"))
	if root, err := fx.reg.RequireInsideLocalPool(ctx, moveTestTenant); err != nil || root != fx.src {
		t.Fatalf("root = %q, %v; want the source", root, err)
	}
}
