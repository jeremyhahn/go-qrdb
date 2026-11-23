// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// moveAdmissionBox lets an interface value sit in an atomic.Pointer.
type moveAdmissionBox struct{ a MoveAdmission }

// moveAuditorBox lets an interface value sit in an atomic.Pointer.
type moveAuditorBox struct{ a MoveStartAuditor }

// SetMoveAdmission wires the tenant layer's half of a move's admit step.
// nil unwires it. It is safe to call while moves are served.
func (c *Catalog) SetMoveAdmission(a MoveAdmission) {
	if a == nil {
		c.moveAdmission.Store(nil)
		return
	}
	c.moveAdmission.Store(&moveAdmissionBox{a: a})
}

// SetMoveStartAuditor wires the auditor of a move's start.
// nil unwires it. It is safe to call while moves are served.
func (c *Catalog) SetMoveStartAuditor(a MoveStartAuditor) {
	if a == nil {
		c.moveAuditor.Store(nil)
		return
	}
	c.moveAuditor.Store(&moveAuditorBox{a: a})
}

// moveRecorderBox lets an interface value sit in an atomic.Pointer.
type moveRecorderBox struct{ r MoveCutoverRecorder }

// SetMoveCutoverRecorder wires the tenant layer's half of a move's cut
// over. nil unwires it; the move reconciler then leaves copied moves
// copied. It is safe to call while moves are served.
func (c *Catalog) SetMoveCutoverRecorder(r MoveCutoverRecorder) {
	if r == nil {
		c.moveRecorder.Store(nil)
		return
	}
	c.moveRecorder.Store(&moveRecorderBox{r: r})
}

func (c *Catalog) currentMoveCutoverRecorder() MoveCutoverRecorder {
	if b := c.moveRecorder.Load(); b != nil {
		return b.r
	}
	return nil
}

func (c *Catalog) currentMoveAdmission() MoveAdmission {
	if b := c.moveAdmission.Load(); b != nil {
		return b.a
	}
	return nil
}

func (c *Catalog) currentMoveAuditor() MoveStartAuditor {
	if b := c.moveAuditor.Load(); b != nil {
		return b.a
	}
	return nil
}

// MoveTenant admits a tenant move ("The move, phase by phase" step 1,
// docs/decisions/volume-pool-tenant-moves.md). The wired MoveAdmission
// checks the tenant (it exists, is a volume or raft tenant, is entitled to
// req.ToPool) and returns its replica nodes; the wired MoveStartAuditor, if
// any, records the start; AdmitMove then reserves
// the destination and writes the journal.
//
// An empty tenant or pool returns *InvalidMoveRequestError. A composition
// that wired no MoveAdmission returns *MoveNotImplementedError. An
// admission or auditor refusal is returned as is and nothing is admitted.
func (c *Catalog) MoveTenant(ctx context.Context, req MoveTenantRequest) (*Move, error) {
	if req.TenantID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty tenant ID"}
	}
	if req.ToPool == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty destination pool"}
	}
	admission := c.currentMoveAdmission()
	if admission == nil {
		return nil, &MoveNotImplementedError{}
	}
	nodes, err := admission.AdmitTenantMove(ctx, req)
	if err != nil {
		return nil, err
	}
	settle := func(*Move, error) {}
	if auditor := c.currentMoveAuditor(); auditor != nil {
		s, err := auditor.BeginMoveStart(ctx, req)
		if err != nil {
			return nil, err
		}
		if s != nil {
			settle = s
		}
	}
	m, err := c.AdmitMove(ctx, MoveAdmitRequest{
		TenantID:     req.TenantID,
		ToPool:       req.ToPool,
		ReplicaNodes: nodes,
	})
	settle(m, err)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// GetMove reads the move journalled for tenantID. An empty tenant returns
// *InvalidMoveRequestError, no journal *MoveNotFoundError, and an
// undecodable record its *CorruptRecordError.
func (c *Catalog) GetMove(ctx context.Context, tenantID string) (*Move, error) {
	if tenantID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty tenant ID"}
	}
	m, _, err := c.readMove(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, &MoveNotFoundError{TenantID: tenantID}
	}
	return m, nil
}

// ResolveForNode is Resolve as seen by nodeID during a move: before cut
// over, a node in the journal's NodesCopied serves the tenant from ToPool
// (its copy step reopened it there), so the returned assignment names
// ToPool. Every other node, and every tenant with no move or a cut-over
// move, gets Resolve's assignment unchanged. A boot or a reopen resolves
// through it, so a node that crashed after its step's journal write opens
// from the directory the journal names for it.
func (c *Catalog) ResolveForNode(ctx context.Context, tenantID, nodeID string) (*Assignment, error) {
	if nodeID == "" {
		return c.Resolve(ctx, tenantID)
	}
	// The assignment and the journal are two keys: a cut-over (or the
	// retire that deletes the journal) between their reads would pair the
	// source assignment with a journal that no longer says so, naming the
	// source directory retirement may delete. Read the assignment again
	// after the journal and retry the pair until both reads of it agree.
	for range resolveForNodeAttempts {
		a, raw, err := c.resolveRaw(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		m, _, err := c.readMove(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		after, rawAfter, err := c.resolveRaw(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if after.Pool != a.Pool || !bytes.Equal(raw, rawAfter) {
			continue // the assignment changed between the reads
		}
		return resolveAgainstMove(after, m, nodeID), nil
	}
	return nil, &ResolveInconsistentError{TenantID: tenantID, Attempts: resolveForNodeAttempts}
}

// resolveForNodeAttempts bounds ResolveForNode's validate-and-retry of the
// assignment/journal read pair.
const resolveForNodeAttempts = 8

// ResolveInconsistentError reports that ResolveForNode's assignment changed
// between its reads on every one of Attempts tries.
type ResolveInconsistentError struct {
	TenantID string
	Attempts int
}

func (e *ResolveInconsistentError) Error() string {
	return fmt.Sprintf("volume: tenant %q assignment changed during each of %d resolve attempts", e.TenantID, e.Attempts)
}

// resolveRaw is Resolve that also returns the stored record's bytes.
func (c *Catalog) resolveRaw(ctx context.Context, tenantID string) (*Assignment, []byte, error) {
	raw, err := c.kv.Get(ctx, AssignmentKey(tenantID))
	if err != nil {
		return nil, nil, err
	}
	if raw == nil {
		return nil, nil, &AssignmentNotFoundError{TenantID: tenantID}
	}
	var a Assignment
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, nil, &CorruptRecordError{Key: AssignmentKey(tenantID), Err: err}
	}
	if err := ValidateAssignmentPath(tenantID, a.Path); err != nil {
		return nil, nil, err
	}
	return &a, raw, nil
}

// resolveAgainstMove applies the move journal m's per-node view to the
// assignment a read consistently with it: a copied node of a move in flight
// resolves to the destination.
func resolveAgainstMove(a *Assignment, m *Move, nodeID string) *Assignment {
	if m == nil || m.State == MoveStateCutover || m.State == MoveStateAdmitted || !contains(m.NodesCopied, nodeID) {
		return a
	}
	if a.Pool != m.FromPool {
		// The assignment no longer names the move's source: the move's
		// view is stale, so the assignment stands.
		return a
	}
	moved := *a
	moved.Pool = m.ToPool
	return &moved
}
