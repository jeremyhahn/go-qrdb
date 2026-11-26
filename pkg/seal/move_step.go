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

package seal

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// movePhaseCopy is the MoveStepError phase of every failure before the
// copy's verify (resolution, refusal, pre-clean and the copy itself).
const movePhaseCopy = "copy"

// RunMoveCopyStep runs one node's copy and verify step of a tenant move
// between volume pools (docs/decisions/volume-pool-tenant-moves.md, Q2 A
// and Q4 A) on THIS node's own pools.
//
// It resolves the tenant's source directory from its assignment through
// the inside-pool guard (RequireInsideLocalPool) and its destination
// directory on toPool at the same Assignment.Path (PlacementDataRoot,
// which refuses a pool that is not ready on this node: the per-step
// check of Q3 A). A destination left by an earlier, crashed attempt is
// removed first and the copy redone from the start. It then copies the
// tree (volume.CopyTree) and verifies the copy against the manifest
// (volume.VerifyCopy). On a copy or verify failure the partial
// destination is removed through RemoveDataRoot; the source is never
// written or removed.
//
// The tenant must be closed on this node (no barrier held), because the
// step copies a closed replica directory. The step stops at a verified
// copy: the caller reopens stores, cuts over and retires the source.
//
// Argument errors are sentinels (ErrEmptyTenantID, ErrMoveStepInvalid,
// ErrPoolNotConfigured); every later error is a *volume.MoveStepError
// carrying nodeID, possibly joined with the error of removing the
// partial destination.
func (r *BarrierRegistry) RunMoveCopyStep(ctx context.Context, nodeID, tenantID, toPool string) (*volume.Manifest, error) {
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}
	if nodeID == "" || toPool == "" {
		return nil, ErrMoveStepInvalid
	}
	allocator, resolver := r.volumeResources()
	if allocator == nil || resolver == nil {
		return nil, ErrPoolNotConfigured
	}
	copyErr := func(err error) error {
		return &volume.MoveStepError{NodeID: nodeID, Phase: movePhaseCopy, Err: err}
	}
	if _, open := r.tenants.Load(tenantID); open {
		return nil, copyErr(ErrMoveTenantOpen)
	}
	assignment, err := allocator.Resolve(ctx, tenantID)
	if err != nil {
		return nil, copyErr(err)
	}
	if assignment.Pool == toPool {
		return nil, copyErr(ErrMoveSamePool)
	}
	src, err := r.RequireInsideLocalPool(ctx, tenantID)
	if err != nil {
		return nil, copyErr(err)
	}
	dst, err := r.PlacementDataRoot(ctx, tenantID, toPool, assignment.Path)
	if err != nil {
		return nil, copyErr(err)
	}
	if _, err := os.Lstat(dst); err == nil {
		if err := r.RemoveDataRoot(ctx, dst); err != nil {
			return nil, copyErr(err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, copyErr(err)
	}

	manifest, err := volume.CopyTree(ctx, src, dst)
	if err == nil {
		if r.moveAfterCopy != nil {
			r.moveAfterCopy(dst)
		}
		err = volume.VerifyCopy(ctx, dst, manifest)
	}
	if err != nil {
		return nil, r.failMoveStep(ctx, nodeID, dst, err)
	}
	return manifest, nil
}

// failMoveStep stamps nodeID on the *volume.MoveStepError of a failed copy
// or verify, removes the partial destination through the inside-pool
// guard, and joins the removal's error when that fails too.
func (r *BarrierRegistry) failMoveStep(ctx context.Context, nodeID, dst string, err error) error {
	var stepErr *volume.MoveStepError
	if errors.As(err, &stepErr) {
		stepErr.NodeID = nodeID
	} else {
		err = &volume.MoveStepError{NodeID: nodeID, Phase: movePhaseCopy, Err: err}
	}
	if removeErr := r.RemoveDataRoot(context.WithoutCancel(ctx), dst); removeErr != nil {
		return errors.Join(err, removeErr)
	}
	return err
}
