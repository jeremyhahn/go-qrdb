// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"strconv"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// MoveReplicaNodeSource lists every node holding a replica of a tenant, as
// the decimal node IDs volume-pool node reports are keyed by. The tenant
// layer's half of a move's admit step (AdmitTenantMove) asks it for the
// nodes that must each report the destination pool ready (decision Q3).
type MoveReplicaNodeSource interface {
	MoveReplicaNodes(ctx context.Context, tenantID string) ([]string, error)
}

// MoveReplicaNodeWiring is the post-construction seam a composition sets a
// MoveReplicaNodeSource through.
type MoveReplicaNodeWiring interface {
	SetMoveReplicaNodeSource(MoveReplicaNodeSource)
}

// moveReplicaNodeSourceBox lets an interface value sit in an atomic.Pointer.
type moveReplicaNodeSourceBox struct {
	source MoveReplicaNodeSource
}

// moveReplicaNodes holds TenantService's MoveReplicaNodeSource.
type moveReplicaNodes struct {
	// moveNodeSource lists a tenant's replica nodes; see
	// SetMoveReplicaNodeSource.
	moveNodeSource atomic.Pointer[moveReplicaNodeSourceBox]
}

var (
	_ volume.MoveAdmission       = (*TenantService)(nil)
	_ volume.MoveCutoverRecorder = (*TenantService)(nil)
	_ MoveReplicaNodeWiring      = (*TenantService)(nil)
)

// SetMoveReplicaNodeSource wires the source of a tenant's replica nodes. nil
// unwires it. It is safe to call while moves are admitted.
func (m *moveReplicaNodes) SetMoveReplicaNodeSource(source MoveReplicaNodeSource) {
	if source == nil {
		m.moveNodeSource.Store(nil)
		return
	}
	m.moveNodeSource.Store(&moveReplicaNodeSourceBox{source: source})
}

func (m *moveReplicaNodes) moveReplicaNodeSource() MoveReplicaNodeSource {
	if box := m.moveNodeSource.Load(); box != nil {
		return box.source
	}
	return nil
}

// AdmitTenantMove is the tenant layer's half of a tenant move's admit step
// ("The move, phase by phase" step 1,
// docs/decisions/volume-pool-tenant-moves.md); it implements
// volume.MoveAdmission. The tenant must exist (ErrTenantNotFound otherwise),
// be a volume or raft tenant and not the system tenant
// (*volume.MoveConflictError otherwise), and be entitled to req.ToPool
// (*quota.EntitlementError otherwise). It returns the tenant's replica nodes
// from the wired MoveReplicaNodeSource; with none wired it returns
// *volume.MoveNotImplementedError, and an empty list is refused with
// *volume.MoveConflictError.
func (s *TenantService) AdmitTenantMove(ctx context.Context, req volume.MoveTenantRequest) ([]string, error) {
	info, err := s.GetTenant(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	kind := info.EffectiveIsolationType()
	if req.TenantID == tenantid.System || (kind != IsolationVolume && kind != IsolationRaft) {
		return nil, &volume.MoveConflictError{TenantID: req.TenantID, Reason: "only a volume or raft tenant can move between pools"}
	}
	ents, err := s.effectiveEntitlements(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	if !ents.AllowsVolumePool(req.ToPool) {
		return nil, &quota.EntitlementError{TenantID: req.TenantID, Feature: "volume_pool:" + req.ToPool}
	}
	source := s.moveReplicaNodeSource()
	if source == nil {
		return nil, &volume.MoveNotImplementedError{}
	}
	nodes, err := source.MoveReplicaNodes(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, &volume.MoveConflictError{TenantID: req.TenantID, Reason: "tenant has no replica node"}
	}
	return nodes, nil
}

// RecordTenantPoolMoved is the tenant layer's half of a tenant move's cut
// over ("The move, phase by phase" step 4,
// docs/decisions/volume-pool-tenant-moves.md); it implements
// volume.MoveCutoverRecorder. A record naming fromPool is rewritten to name
// toPool; one already naming toPool is left as is with no write, so the
// move reconciler may repeat the call. A record naming any other pool is
// refused with *volume.MoveConflictError, and a missing tenant returns
// ErrTenantNotFound.
func (s *TenantService) RecordTenantPoolMoved(ctx context.Context, tenantID, fromPool, toPool string) error {
	info, err := s.GetTenant(ctx, tenantID)
	if err != nil {
		return err
	}
	if info.VolumePool == toPool {
		return nil
	}
	_, err = s.swapRecord(ctx, tenantID, func(info *TenantInfo) (func(error), error) {
		switch info.VolumePool {
		case toPool:
			return nil, nil
		case fromPool:
			info.VolumePool = toPool
			return nil, nil
		default:
			return nil, &volume.MoveConflictError{TenantID: tenantID, Reason: "tenant record names pool " + strconv.Quote(info.VolumePool) + ", not the move's source " + strconv.Quote(fromPool)}
		}
	})
	return err
}
