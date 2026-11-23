// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// DefaultTenantMoveInterval is how often a TenantMoveWorker runs this
// node's move steps and, on the system shard's leader, the leader pass.
const DefaultTenantMoveInterval = 2 * time.Second

// TenantMoveService is the tenant layer a tenant move is wired to: it
// admits a move, records its cut over, and takes the replica-node source.
// *tenant.TenantService satisfies it.
type TenantMoveService interface {
	volume.MoveAdmission
	volume.MoveCutoverRecorder
	tenant.MoveReplicaNodeWiring
}

var _ TenantMoveService = (*tenant.TenantService)(nil)

// MoveMembership is the part of the raft host the replica-node source and
// the leader check read: the system shard's members and its leader.
type MoveMembership interface {
	LeaderID(shardID uint64) (uint64, bool, error)
	SyncGetShardMembership(ctx context.Context, shardID uint64) (*quicraft.Membership, error)
}

// TenantMoveConfig is what WireTenantMoves wires a node's tenant moves
// from.
type TenantMoveConfig struct {
	// Catalog is the volume-pool catalog the move journal lives in.
	Catalog *volume.Catalog
	// BarrierReg is this node's barrier registry; its volume node ID must
	// already be NodeID (BuildTenantInfra sets it from VolumeNodeID).
	BarrierReg *seal.BarrierRegistry
	// Tenants is the tenant service moves admit and cut over through.
	Tenants TenantMoveService
	// Infra holds the tenant runtimes a move step closes and reopens.
	Infra *TenantInfra
	// NodeID is this node's move node ID: the NodeReport node ID its
	// volume reporter writes.
	NodeID string
	// Membership is the raft host, or nil on a node with no shard group
	// (the embedded node), which is its own leader and only replica node.
	Membership MoveMembership
	// ReplicaID is this node's replica ID in the system shard.
	ReplicaID uint64
	// Placements is the replicated placement table, or nil.
	Placements *placement.Store
	// StartAudit audits each move's start; nil audits nothing.
	StartAudit volume.MoveStartAuditor
	// Transitions records each journal transition; nil records nothing.
	Transitions volume.MoveTransitionRecorder
	// Interval is the tick; a non-positive one is DefaultTenantMoveInterval.
	Interval time.Duration
	Logger   *slog.Logger
}

// TenantMoveWorker runs a node's share of the tenant moves: every tick it
// runs this node's own steps (RunLocalSteps) and, while this node leads
// the system shard, the leader pass (ReconcileOnce).
type TenantMoveWorker struct {
	reconciler *volume.MoveReconciler
	nodeID     string
	leads      func() bool
	interval   time.Duration
	logger     *slog.Logger
}

// WireTenantMoves wires cfg's catalog to its tenant service, audit and
// replica-node source, and returns the worker that runs the moves. The
// caller starts it with Run.
func WireTenantMoves(cfg TenantMoveConfig) (*TenantMoveWorker, error) {
	if cfg.Catalog == nil || cfg.BarrierReg == nil || cfg.Tenants == nil || cfg.NodeID == "" {
		return nil, errors.New("server: tenant moves need a catalog, a barrier registry, a tenant service and a node id")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	stores, err := newMoveStores(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Infra != nil && cfg.Infra.Reconciler != nil {
		// The reconciler must not re-attach a shard (or register a barrier
		// shell) between a move closing the tenant's shards and its barrier.
		cfg.Infra.Reconciler.SetMoveHold(stores)
	}
	if cfg.Infra != nil && cfg.Infra.Hosts != nil {
		cfg.Infra.Hosts.SetMoveHold(stores)
	}
	runner, err := seal.NewMoveStepRunner(cfg.BarrierReg, cfg.NodeID, stores)
	if err != nil {
		return nil, err
	}
	opts := []volume.MoveReconcilerOption{volume.WithMoveReconcilerLogger(logger)}
	if cfg.Transitions != nil {
		opts = append(opts, volume.WithMoveTransitionRecorder(cfg.Transitions))
	}
	reconciler, err := volume.NewMoveReconciler(cfg.Catalog, runner, opts...)
	if err != nil {
		return nil, err
	}
	cfg.Catalog.SetMoveAdmission(cfg.Tenants)
	cfg.Catalog.SetMoveCutoverRecorder(cfg.Tenants)
	if cfg.StartAudit != nil {
		cfg.Catalog.SetMoveStartAuditor(cfg.StartAudit)
	}
	cfg.Tenants.SetMoveReplicaNodeSource(&moveReplicaNodeSource{
		membership: cfg.Membership,
		placements: cfg.Placements,
		local:      cfg.NodeID,
	})
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultTenantMoveInterval
	}
	return &TenantMoveWorker{
		reconciler: reconciler,
		nodeID:     cfg.NodeID,
		leads:      systemLeaderCheck(cfg.Membership, cfg.ReplicaID),
		interval:   interval,
		logger:     logger,
	}, nil
}

// newMoveStores is the TenantStoreCloser over cfg's tenant runtimes.
func newMoveStores(cfg TenantMoveConfig) (*MoveStoreCloser, error) {
	var shards MoveShardLifecycle
	var opts []MoveStoreCloserOption
	if cfg.Infra != nil && cfg.Infra.Raft != nil {
		shards = cfg.Infra.Raft
	}
	if cfg.Infra != nil && cfg.Infra.Hosts != nil {
		opts = append(opts, WithMoveTenantHosts(cfg.Infra.Hosts))
	}
	var caughtUp ReplicaCaughtUp
	if host, ok := cfg.Membership.(*quicraft.Host); ok && host != nil && cfg.Placements != nil {
		caughtUp = readIndexCaughtUp(quicraftShardSource{host: host}, cfg.Placements)
	}
	return NewMoveStoreCloser(shards, cfg.BarrierReg, caughtUp, 0, opts...)
}

// readIndexCaughtUp reports a reopened replica caught up once a
// linearizable read on its shard completes here: the local state machine
// has then applied up to the group's commit index.
func readIndexCaughtUp(probe shardLeaderSource, placements *placement.Store) ReplicaCaughtUp {
	return func(ctx context.Context, tenantID, db string) (bool, error) {
		p, err := placements.Get(ctx, tenantID, db)
		if errors.Is(err, placement.ErrPlacementNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if p == nil {
			return true, nil
		}
		return probe.ReadReady(ctx, p.ShardID) == nil, nil
	}
}

// systemLeaderCheck reports whether this node leads the system shard. With
// no membership the node is its own leader.
func systemLeaderCheck(m MoveMembership, replicaID uint64) func() bool {
	if m == nil {
		return func() bool { return true }
	}
	return func() bool {
		leader, ok, err := m.LeaderID(shard.ShardKV)
		return err == nil && ok && leader == replicaID
	}
}

// Tick runs one round: this node's own steps, then the leader pass when
// this node leads the system shard. Errors are logged; the next tick
// retries.
func (w *TenantMoveWorker) Tick(ctx context.Context) {
	if err := w.reconciler.RunLocalSteps(ctx, w.nodeID); err != nil {
		w.logger.Warn("volume: tenant move steps failed on this node", slog.String("error", err.Error()))
	}
	if !w.leads() {
		return
	}
	if err := w.reconciler.ReconcileOnce(ctx); err != nil {
		w.logger.Warn("volume: tenant move leader pass failed", slog.String("error", err.Error()))
	}
}

// Run ticks until ctx ends.
func (w *TenantMoveWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.Tick(ctx)
		}
	}
}

// moveReplicaNodeSource lists the move node IDs of a tenant's replicas:
// for an IsolationRaft tenant, the system-shard replica ID of each node its
// placements name (matched by raft address); for a tenant with no
// placement (an IsolationVolume tenant, or any tenant on a node with no
// shard group), this node.
type moveReplicaNodeSource struct {
	membership MoveMembership
	placements *placement.Store
	local      string
}

// MoveReplicaNodes implements tenant.MoveReplicaNodeSource.
func (s *moveReplicaNodeSource) MoveReplicaNodes(ctx context.Context, tenantID string) ([]string, error) {
	if s.placements == nil || s.membership == nil {
		return []string{s.local}, nil
	}
	ps, err := s.placements.ListForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return []string{s.local}, nil
	}
	m, err := s.membership.SyncGetShardMembership(ctx, shard.ShardKV)
	if err != nil {
		return nil, err
	}
	byAddr := make(map[string]uint64, len(m.Nodes))
	for id, addr := range m.Nodes {
		byAddr[addr] = id
	}
	seen := map[string]struct{}{}
	var nodes []string
	for _, p := range ps {
		for _, r := range p.Replicas {
			id, ok := byAddr[r.RaftAddr]
			if !ok {
				return nil, &volume.MoveConflictError{TenantID: tenantID, Reason: "a replica's node is not a member of the cluster"}
			}
			n := strconv.FormatUint(id, 10)
			if _, dup := seen[n]; !dup {
				seen[n] = struct{}{}
				nodes = append(nodes, n)
			}
		}
	}
	sort.Strings(nodes)
	return nodes, nil
}
