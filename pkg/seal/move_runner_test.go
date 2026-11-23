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
	"testing"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
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
	if stores.closes != 1 || stores.open != fx.dst() {
		t.Fatalf("closes = %d, open = %q, want 1 close and the destination open", stores.closes, stores.open)
	}
	mustExist(t, filepath.Join(fx.dst(), "MANIFEST-000001"))
	mustExist(t, fx.src)
	if err := r.SettleMoveStep(context.Background(), runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
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
	if err := tb.Unseal(ctx, Credentials{}); err != nil && !errors.Is(err, ErrAlreadyUnsealed) {
		return err
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
	if err := r.SettleMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("settle copy: %v", err)
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

// TestMoveStepRunner_AbortRedoneBeforeCAS is the first abort crash point:
// after the copy-back verifies and before the journal CAS, the node redoes
// the abort step and finds the destination intact.
func TestMoveStepRunner_AbortRedoneBeforeCAS(t *testing.T) {
	fx, stores, r := newRunnerFixture(t)
	ctx := context.Background()
	if err := r.RunMoveStep(ctx, runnerStep(volume.MoveStepPhaseCopy, false)); err != nil {
		t.Fatalf("copy: %v", err)
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
