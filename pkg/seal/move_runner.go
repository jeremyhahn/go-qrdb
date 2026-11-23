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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jeremyhahn/go-qrdb/pkg/util/fsync"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// TenantStoreCloser closes and reopens one tenant's stores on this node:
// the tenant's barrier and every shard replica it has started here. The
// node-side MoveStepRunner calls it around the copy and the copy-back, so
// the tenant's directory is never written while it is copied.
type TenantStoreCloser interface {
	// CloseTenantStores closes the tenant's shard replicas, then its
	// barrier. A tenant with nothing open on this node is a no-op.
	CloseTenantStores(ctx context.Context, tenantID string) error
	// OpenTenantStores reopens the tenant's barrier, then its shard
	// replicas, from the directory BarrierRegistry.RequireInsideLocalPool
	// resolves, and returns once every replica reports caught up.
	OpenTenantStores(ctx context.Context, tenantID string) error
}

// MoveStepNodeError is returned by MoveStepRunner.RunMoveStep for a step
// granted to another node: a node runs only its own steps.
type MoveStepNodeError struct {
	StepNode  string
	LocalNode string
}

func (e *MoveStepNodeError) Error() string {
	return fmt.Sprintf("seal: move step for node %q refused on node %q", e.StepNode, e.LocalNode)
}

// MoveStepRunner is this node's volume.MoveStepRunner and
// volume.MoveStepSettler: it runs the copy, abort and retire steps the
// leader grants to this node, over this node's own pools only. It derives
// no key and calls no Unlock: the data it copies is ciphertext under the
// tenant barrier, and the destination pool was ready (unlocked) at admit.
type MoveStepRunner struct {
	reg    *BarrierRegistry
	nodeID string
	stores TenantStoreCloser

	// rename is the rename copy-back moves a staged tree with; nil means
	// fsync.Rename. A same-package test swaps it to model a source root that
	// is a mount point.
	rename func(oldpath, newpath string) error
}

// NewMoveStepRunner returns the runner for nodeID over reg and stores. A
// nil reg or stores, or an empty nodeID, returns ErrMoveStepInvalid.
func NewMoveStepRunner(reg *BarrierRegistry, nodeID string, stores TenantStoreCloser) (*MoveStepRunner, error) {
	if reg == nil || stores == nil || nodeID == "" {
		return nil, ErrMoveStepInvalid
	}
	return &MoveStepRunner{reg: reg, nodeID: nodeID, stores: stores}, nil
}

// RunMoveStep runs step on this node:
//   - copy: closes the tenant's stores, copies and verifies the source
//     into the destination (RunMoveCopyStep) and leaves the stores closed:
//     nothing serves from the destination until the journal names it for
//     this node (CompleteMoveStep records the node copied), so a crash or a
//     failed journal write cannot roll back over an acknowledged write;
//     SettleMoveStep then reopens from the destination; a failed copy
//     reopens from the source;
//   - abort with Copied: closes the stores and copies the destination back
//     over the source, verified; the stores stay closed until
//     SettleMoveStep, after the journal CAS;
//   - abort without Copied: removes any partial destination and makes sure
//     the stores are open from the source;
//   - retire: removes the source directory.
//
// A step for another node returns *MoveStepNodeError; a sealed node
// returns *volume.NodeSealedError. Neither runs anything.
func (m *MoveStepRunner) RunMoveStep(ctx context.Context, step volume.MoveStep) error {
	if step.NodeID != m.nodeID {
		return &MoveStepNodeError{StepNode: step.NodeID, LocalNode: m.nodeID}
	}
	if m.reg.system == nil || m.reg.system.IsSealed() {
		return &volume.NodeSealedError{Op: "move step"}
	}
	if step.TenantID == "" || step.FromPool == "" || step.ToPool == "" {
		return ErrMoveStepInvalid
	}
	switch step.Phase {
	case volume.MoveStepPhaseCopy:
		return m.copyStep(ctx, step)
	case volume.MoveStepPhaseAbort:
		if step.Copied {
			return m.copyBack(ctx, step)
		}
		return m.abortUncopied(ctx, step)
	case volume.MoveStepPhaseRetire:
		return m.retire(ctx, step)
	default:
		return ErrMoveStepInvalid
	}
}

// SettleMoveStep runs the part of step that must follow the journal CAS:
// for a copy, the stores open from the destination the journal now names
// for this node; for an abort of a copied node, the stores reopen from the source (the
// journal now names it for this node) and the destination is removed.
// Every other phase has nothing left to do.
func (m *MoveStepRunner) SettleMoveStep(ctx context.Context, step volume.MoveStep) error {
	if step.NodeID != m.nodeID {
		return &MoveStepNodeError{StepNode: step.NodeID, LocalNode: m.nodeID}
	}
	switch {
	case step.Phase == volume.MoveStepPhaseCopy:
		// The journal now records this node copied, so the destination is
		// authoritative here: open the stores from it.
		m.reg.moveOpenPools.Store(step.TenantID, step.ToPool)
		return m.stores.OpenTenantStores(ctx, step.TenantID)
	case step.Phase == volume.MoveStepPhaseAbort && step.Copied:
		m.reg.moveOpenPools.Delete(step.TenantID)
		if err := m.stores.OpenTenantStores(ctx, step.TenantID); err != nil {
			return err
		}
		return m.removePlacement(ctx, step.TenantID, step.ToPool)
	default:
		return nil
	}
}

func (m *MoveStepRunner) copyStep(ctx context.Context, step volume.MoveStep) error {
	if err := m.stores.CloseTenantStores(ctx, step.TenantID); err != nil {
		return err
	}
	m.reg.moveOpenPools.Delete(step.TenantID)
	if _, err := m.reg.RunMoveCopyStep(ctx, m.nodeID, step.TenantID, step.ToPool); err != nil {
		if openErr := m.stores.OpenTenantStores(ctx, step.TenantID); openErr != nil {
			return errors.Join(err, openErr)
		}
		return err
	}
	// The stores stay closed: the destination serves only once the journal
	// records this node copied, in SettleMoveStep.
	return nil
}

// copyBack makes the source authoritative again (decision doc, "Failure and
// rollback", path (b)): the destination, which served since this node's
// copy step, is copied back over the source and verified. go-quicraft's
// Promoter cannot demote a voter in place, so path (b) serves multi-replica
// tenants too, as the decision allows.
//
// The source is never removed before an intact copy of the destination
// exists beside it: the destination's tree is copied into a staging
// directory in the source pool (copyBackStagingName) and verified there,
// and only then is the source removed and the staging directory renamed
// into its place. A copy or verify that fails -- a destination that cannot
// be read back is the likely case -- removes only the staging directory,
// so the source stays byte for byte as it was, and the stores reopen from
// the destination, which the journal still names for this node. A rerun
// after a crash at any point before the journal CAS first removes a
// leftover staging directory and treats a source already missing as
// removed: the destination stays authoritative until the CAS.
//
// A source root that is itself a mount point cannot be removed or renamed
// onto (EBUSY, or EXDEV across the mount): RemoveDataRoot empties it and
// leaves it in place, and copyBack then keeps the root and swaps its
// contents instead (swapIntoMountedRoot). A rerun after a crash part way
// through the swap empties the root again and redoes it.
func (m *MoveStepRunner) copyBack(ctx context.Context, step volume.MoveStep) error {
	if err := m.stores.CloseTenantStores(ctx, step.TenantID); err != nil {
		return err
	}
	stepErr := func(err error) error {
		return &volume.MoveStepError{NodeID: m.nodeID, Phase: volume.MoveStepPhaseAbort, Err: err}
	}
	// fail keeps the destination authoritative: the journal still names it
	// for this node, so it serves from there until the step is redone.
	fail := func(err error) error {
		if openErr := m.stores.OpenTenantStores(ctx, step.TenantID); openErr != nil {
			return errors.Join(stepErr(err), openErr)
		}
		return stepErr(err)
	}
	src, dst, err := m.placements(ctx, step)
	if err != nil {
		return stepErr(err)
	}
	path, err := m.assignmentPath(ctx, step.TenantID)
	if err != nil {
		return stepErr(err)
	}
	staging, err := m.reg.PlacementDataRoot(ctx, step.TenantID, step.FromPool, copyBackStagingName(path))
	if err != nil {
		return stepErr(err)
	}
	if _, err := os.Lstat(dst); err != nil {
		return stepErr(err)
	}
	if err := m.reg.RemoveDataRoot(ctx, staging); err != nil {
		return fail(err)
	}
	manifest, err := volume.CopyTree(ctx, dst, staging)
	if err == nil {
		if m.reg.moveAfterCopy != nil {
			m.reg.moveAfterCopy(staging)
		}
		err = volume.VerifyCopy(ctx, staging, manifest)
	}
	if err != nil {
		if rmErr := m.reg.RemoveDataRoot(ctx, staging); rmErr != nil {
			err = errors.Join(err, rmErr)
		}
		return fail(err)
	}
	if err := m.reg.RemoveDataRoot(ctx, src); err != nil {
		return fail(err)
	}
	if err := m.renameFn()(staging, src); err != nil {
		if !errors.Is(err, syscall.EBUSY) && !errors.Is(err, syscall.EXDEV) {
			return fail(err)
		}
		if err := m.swapIntoMountedRoot(ctx, staging, src); err != nil {
			return fail(err)
		}
	}
	return nil
}

// swapIntoMountedRoot finishes a copy-back whose source root is a mount
// point, already emptied by RemoveDataRoot: it copies the verified staging
// tree into copyBackInPlaceName inside the root (a rename across the mount
// fails with EXDEV, so the tree must first reach the mounted filesystem),
// verifies it there, moves each child up into the root with a durable
// rename, syncs the root, and removes the inner and the staging directories.
// The root itself is kept. Every step is redone by a rerun: copyBack empties
// the root again first, inner directory included.
func (m *MoveStepRunner) swapIntoMountedRoot(ctx context.Context, staging, root string) error {
	inner := filepath.Join(root, copyBackInPlaceName)
	manifest, err := volume.CopyTree(ctx, staging, inner)
	if err == nil {
		err = volume.VerifyCopy(ctx, inner, manifest)
	}
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(inner)
	if err != nil {
		return err
	}
	rename := m.renameFn()
	for _, e := range entries {
		if e.Name() == copyBackInPlaceName {
			return fmt.Errorf("copy-back: the tenant tree holds %q, the in-place staging name", copyBackInPlaceName)
		}
		if err := rename(filepath.Join(inner, e.Name()), filepath.Join(root, e.Name())); err != nil {
			return err
		}
	}
	if err := m.reg.RemoveDataRoot(ctx, inner); err != nil {
		return err
	}
	if err := fsync.Dir(root); err != nil {
		return err
	}
	return m.reg.RemoveDataRoot(ctx, staging)
}

func (m *MoveStepRunner) abortUncopied(ctx context.Context, step volume.MoveStep) error {
	m.reg.moveOpenPools.Delete(step.TenantID)
	if err := m.removePlacement(ctx, step.TenantID, step.ToPool); err != nil {
		return err
	}
	if _, open := m.reg.tenants.Load(step.TenantID); open {
		return nil
	}
	return m.stores.OpenTenantStores(ctx, step.TenantID)
}

func (m *MoveStepRunner) retire(ctx context.Context, step volume.MoveStep) error {
	m.reg.moveOpenPools.Delete(step.TenantID)
	return m.removePlacement(ctx, step.TenantID, step.FromPool)
}

// placements returns this node's source and destination directories for
// step's tenant.
func (m *MoveStepRunner) placements(ctx context.Context, step volume.MoveStep) (src, dst string, err error) {
	path, err := m.assignmentPath(ctx, step.TenantID)
	if err != nil {
		return "", "", err
	}
	if src, err = m.reg.PlacementDataRoot(ctx, step.TenantID, step.FromPool, path); err != nil {
		return "", "", err
	}
	if dst, err = m.reg.PlacementDataRoot(ctx, step.TenantID, step.ToPool, path); err != nil {
		return "", "", err
	}
	return src, dst, nil
}

// copyBackStagingName is the directory, in the source pool beside the
// tenant's own, that a copy-back fills and verifies before it replaces the
// source. It is one path component, as placementRoot requires. It begins
// with volume.ReservedPathPrefix, which no assignment path or tenant id may
// begin with, so it is never another tenant's directory: a staging directory
// found there can only be this tenant's earlier attempt, and copy-back
// removes it before it stages again.
func copyBackStagingName(path string) string {
	return volume.ReservedPathPrefix + "copyback." + path
}

// copyBackInPlaceName is the directory, inside a source root that is a mount
// point, a copy-back stages the tree in before it moves each child up into the
// root: a rename across the mount fails with EXDEV, so this staging must lie
// on the mounted filesystem itself.
const copyBackInPlaceName = ".qrdb-copyback"

// renameFn returns the rename copy-back uses.
func (m *MoveStepRunner) renameFn() func(oldpath, newpath string) error {
	if m.rename != nil {
		return m.rename
	}
	return fsync.Rename
}

// removePlacement removes this node's directory for tenantID on pool, if
// any, through RemoveDataRoot's containment check.
func (m *MoveStepRunner) removePlacement(ctx context.Context, tenantID, pool string) error {
	path, err := m.assignmentPath(ctx, tenantID)
	if err != nil {
		return err
	}
	dir, err := m.reg.PlacementDataRoot(ctx, tenantID, pool, path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return m.reg.RemoveDataRoot(ctx, dir)
}

// assignmentPath is the tenant's directory name, the same on both pools.
func (m *MoveStepRunner) assignmentPath(ctx context.Context, tenantID string) (string, error) {
	allocator, _ := m.reg.volumeResources()
	if allocator == nil {
		return "", ErrPoolNotConfigured
	}
	a, err := allocator.Resolve(ctx, tenantID)
	if err != nil {
		return "", err
	}
	return a.Path, nil
}

// ReopenVolumeTenant re-registers tenantID's barrier from the directory
// RequireInsideLocalPool resolves, with the posture its last registration
// persisted, and unseals an auto-unseal tenant through the system barrier,
// as boot's RestoreVolumeTenants does: the tenant's host and shards attach
// right after and read through the barrier. A self-managed tenant stays
// sealed until its admin unseals it, as after a restart. A successful
// re-attach ends the hold CloseVolumeTenant began. It is the barrier half of
// a TenantStoreCloser's reopen.
//
// It is idempotent: a settle pass that reopened the tenant but failed to
// release its step retries it, and boot's RestoreVolumeTenants may already
// have registered a tenant whose journal still records the step settling.
// A barrier already registered at the data root RequireInsideLocalPool
// resolves now is reused (no second registration, which RegisterTenant
// would refuse), the hold still ends, and an auto-unseal tenant left sealed
// is unsealed (an unsealed one is left as is). A barrier registered at any
// other data root -- the other pool's directory -- fails closed with
// *VolumeTenantRootMismatchError rather than serve the wrong directory.
func (r *BarrierRegistry) ReopenVolumeTenant(ctx context.Context, tenantID string) error {
	autoUnseal, _, err := r.loadTenantBarrierPosture(ctx, tenantID)
	if err != nil {
		return err
	}
	registeredRoot, tb, registered := r.VolumeTenantDataRoot(tenantID)
	if registered {
		wantRoot, rerr := r.RequireInsideLocalPool(ctx, tenantID)
		if rerr != nil {
			return rerr
		}
		if registeredRoot != wantRoot {
			return &VolumeTenantRootMismatchError{TenantID: tenantID, Registered: registeredRoot, Want: wantRoot}
		}
	} else if _, tb, err = r.ReattachVolumeTenant(ctx, tenantID, autoUnseal); err != nil {
		return err
	}
	r.moveHeld.Delete(tenantID)
	if !autoUnseal || !tb.IsSealed() {
		return nil
	}
	return r.UnsealTenant(ctx, tenantID, Credentials{})
}

// VolumeTenantRootMismatchError reports that ReopenVolumeTenant found the
// tenant's barrier already registered at a data root other than the one its
// volume pool assignment resolves to on this node now. Registered is empty
// when the barrier was not registered through a volume pool.
type VolumeTenantRootMismatchError struct {
	TenantID   string
	Registered string
	Want       string
}

func (e *VolumeTenantRootMismatchError) Error() string {
	return fmt.Sprintf("seal: tenant %q barrier is registered at data root %q, not the resolved %q", e.TenantID, e.Registered, e.Want)
}

// TenantBarrierSealed reports whether tenantID's barrier is registered on
// this node and sealed: after ReopenVolumeTenant, a self-managed tenant
// whose barrier stays sealed awaits its administrator's unseal. Its sealed
// posture is durable (persisted with the barrier), so nothing more is
// recorded: the stores attach on that unseal (the shard reconciler's
// unseal notification, and Services once the barrier serves).
func (r *BarrierRegistry) TenantBarrierSealed(tenantID string) bool {
	tb, err := r.GetTenant(tenantID)
	return err == nil && tb.IsSealed()
}

// TenantMoveHeldError is a fresh registration of a volume tenant refused
// because a move step on this node holds the tenant's stores closed: the
// move's own reopen opens them again, from whichever pool the move settles.
type TenantMoveHeldError struct {
	TenantID string
}

func (e *TenantMoveHeldError) Error() string {
	return "qrdb/seal: tenant " + e.TenantID + " is held closed by a volume-pool move step"
}

// CloseVolumeTenant closes tenantID's barrier and its per-tenant Pebble
// store on this node, so nothing holds the tenant's directory under the
// source pool while a move copies it and the reopen from the destination
// opens a fresh store entry; a tenant not open here is a no-op. Until
// ReopenVolumeTenant, a fresh registration of the tenant (a placement-driven
// barrier shell) is refused with *TenantMoveHeldError. It is the barrier
// half of a TenantStoreCloser's close.
func (r *BarrierRegistry) CloseVolumeTenant(ctx context.Context, tenantID string) error {
	r.moveHeld.Store(tenantID, struct{}{})
	if err := r.ReleaseTenant(ctx, tenantID); err != nil && !errors.Is(err, ErrTenantNotFound) {
		return err
	}
	return nil
}
