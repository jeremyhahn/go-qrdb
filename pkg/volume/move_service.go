// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import "context"

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
	a, err := c.Resolve(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if nodeID == "" {
		return a, nil
	}
	m, _, err := c.readMove(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if m == nil || m.State == MoveStateCutover || m.State == MoveStateAdmitted || !contains(m.NodesCopied, nodeID) {
		return a, nil
	}
	if a.Pool != m.FromPool {
		// The assignment no longer names the move's source: the move's
		// view is stale, so the assignment stands.
		return a, nil
	}
	moved := *a
	moved.Pool = m.ToPool
	return &moved, nil
}
