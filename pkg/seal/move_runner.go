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
//     into the destination (RunMoveCopyStep), reopens the stores from the
//     destination and returns once the replicas report caught up; a failed
//     copy reopens from the source;
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
// for an abort of a copied node, the stores reopen from the source (the
// journal now names it for this node) and the destination is removed.
// Every other phase has nothing left to do.
func (m *MoveStepRunner) SettleMoveStep(ctx context.Context, step volume.MoveStep) error {
	if step.NodeID != m.nodeID {
		return &MoveStepNodeError{StepNode: step.NodeID, LocalNode: m.nodeID}
	}
	if step.Phase != volume.MoveStepPhaseAbort || !step.Copied {
		return nil
	}
	m.reg.moveOpenPools.Delete(step.TenantID)
	if err := m.stores.OpenTenantStores(ctx, step.TenantID); err != nil {
		return err
	}
	return m.removePlacement(ctx, step.TenantID, step.ToPool)
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
	m.reg.moveOpenPools.Store(step.TenantID, step.ToPool)
	if err := m.stores.OpenTenantStores(ctx, step.TenantID); err != nil {
		return err
	}
	return nil
}

// copyBack makes the source authoritative again (decision doc, "Failure and
// rollback", path (b)): the destination, which served since this node's
// copy step, is copied back over the source and verified. go-quicraft's
// Promoter cannot demote a voter in place, so path (b) serves multi-replica
// tenants too, as the decision allows.
func (m *MoveStepRunner) copyBack(ctx context.Context, step volume.MoveStep) error {
	if err := m.stores.CloseTenantStores(ctx, step.TenantID); err != nil {
		return err
	}
	stepErr := func(err error) error {
		return &volume.MoveStepError{NodeID: m.nodeID, Phase: volume.MoveStepPhaseAbort, Err: err}
	}
	src, dst, err := m.placements(ctx, step)
	if err != nil {
		return stepErr(err)
	}
	if _, err := os.Lstat(dst); err != nil {
		return stepErr(err)
	}
	if err := m.reg.RemoveDataRoot(ctx, src); err != nil {
		return stepErr(err)
	}
	manifest, err := volume.CopyTree(ctx, dst, src)
	if err == nil {
		if m.reg.moveAfterCopy != nil {
			m.reg.moveAfterCopy(src)
		}
		err = volume.VerifyCopy(ctx, src, manifest)
	}
	if err != nil {
		// The destination stays authoritative: the journal still names it
		// for this node, so it serves from there until the step is redone.
		if openErr := m.stores.OpenTenantStores(ctx, step.TenantID); openErr != nil {
			return errors.Join(stepErr(err), openErr)
		}
		return stepErr(err)
	}
	return nil
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
// persisted, as boot's ReattachVolumeTenant does. It is the barrier half of
// a TenantStoreCloser's reopen.
func (r *BarrierRegistry) ReopenVolumeTenant(ctx context.Context, tenantID string) error {
	autoUnseal, _, err := r.loadTenantBarrierPosture(ctx, tenantID)
	if err != nil {
		return err
	}
	_, _, err = r.ReattachVolumeTenant(ctx, tenantID, autoUnseal)
	return err
}

// CloseVolumeTenant closes tenantID's barrier and its per-tenant Pebble
// store on this node, so nothing holds the tenant's directory under the
// source pool while a move copies it and the reopen from the destination
// opens a fresh store entry; a tenant not open here is a no-op. It is the
// barrier half of a TenantStoreCloser's close.
func (r *BarrierRegistry) CloseVolumeTenant(ctx context.Context, tenantID string) error {
	if err := r.ReleaseTenant(ctx, tenantID); err != nil && !errors.Is(err, ErrTenantNotFound) {
		return err
	}
	return nil
}
