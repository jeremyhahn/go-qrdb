// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package clustermig

import (
	"context"
	"log/slog"
	"slices"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// OwnerResolver enumerates the node owners (and the Raft shard) that must each
// recycle for a migration scope. For a tenant database scope this is the
// placement Replicas' NodeIDs and the database's dedicated shard; for the system
// scope it is every cluster member and the system shard. The resolver is the
// single seam between the coordination plane and the placement/membership
// subsystems, keeping clustermig free of those dependencies.
type OwnerResolver interface {
	Owners(ctx context.Context, tenantID, database string) (shardID uint64, owners []string, err error)
}

// Coordinator runs on the cluster leader. When an enable-encryption is requested
// for a scope it resolves the owner set, allocates the next generation, and
// writes the replicated ClusterMigration intent record. The record carries no
// credential: each owner supplies its own (CredentialSource). It moves no data;
// per-node execution is the Reconciler's job. The coordinator is safe to call
// on any node — Request is a no-op-with-error when this node is not the leader,
// so the transport layer can route blindly and let the seam gate.
type Coordinator struct {
	store    *Store
	owners   OwnerResolver
	isLeader func() bool
	nowUnix  func() int64

	// rolling and self enable rolling migrations (WithRollingSteps).
	rolling func() bool
	self    string

	// progress gates a rolling step's hand-on on the owner's Raft log having
	// caught up (WithCatchUp); nil hands on at the ack alone.
	progress ReplicationProgress

	// logger records each rolling step's hand-on (WithLogger).
	logger *slog.Logger

	// mu serialises this node's own read-modify-writes of a migration record,
	// so Request and Advance on one node do not refuse each other's writes.
	// It cannot serialise them across nodes: a former leader that still
	// believes it leads is refused by the guarded write instead
	// (Store.swapMigration, ErrMigrationMoved).
	mu sync.Mutex
}

// CoordinatorOption configures a Coordinator.
type CoordinatorOption func(*Coordinator)

// WithRollingSteps makes every request the Coordinator writes while rolling
// reports true a rolling one: its owners recycle one at a time in
// ClusterMigration.Steps, with self, this node's NodeID, last. rolling
// reports whether featuregate.RollingMigration is activated cluster-wide; the
// composer supplies it, so this package does not depend on the gate. A nil
// rolling leaves every request recycling all owners at once.
func WithRollingSteps(rolling func() bool, self string) CoordinatorOption {
	return func(c *Coordinator) {
		c.rolling = rolling
		c.self = self
	}
}

// WithLogger logs each rolling step the coordinator hands on, with the commit
// index its owner caught up to. Without it the coordinator logs nothing.
func WithLogger(logger *slog.Logger) CoordinatorOption {
	return func(c *Coordinator) { c.logger = logger }
}

// NewCoordinator constructs a Coordinator. isLeader gates intent writes to the
// leader (the replicated record must be written exactly once per request).
// nowUnix stamps the request time; pass nil to omit the timestamp (the state
// machine has no wall clock, so callers supply one).
func NewCoordinator(store *Store, owners OwnerResolver, isLeader func() bool, nowUnix func() int64, opts ...CoordinatorOption) *Coordinator {
	if isLeader == nil {
		isLeader = func() bool { return true }
	}
	c := &Coordinator{store: store, owners: owners, isLeader: isLeader, nowUnix: nowUnix}
	for _, o := range opts {
		o(c)
	}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// Prepare runs with a request's complete record -- generation included --
// before the record is written, so nothing the record starts can run ahead
// of it. A returned error aborts the request; undo, when not nil, runs if the
// write then fails.
type Prepare func(m *ClusterMigration) (undo func(), err error)

// RequestOption configures one Request.
type RequestOption func(*requestOptions)

type requestOptions struct {
	prepare Prepare
}

// WithPrepare runs p before the request's record is written. The node that
// accepts a request holds the request's credential this way, so its own
// reconciler can never read the record before the credential is held.
func WithPrepare(p Prepare) RequestOption {
	return func(o *requestOptions) { o.prepare = p }
}

// Request begins a cluster-wide enable-encryption for a scope. It resolves the
// owners, bumps the generation past any prior request for the same scope, and
// writes the intent record. Only the leader writes; a follower returns
// ErrNotLeader so the caller can forward to the leader. An empty tenantID with
// empty database denotes the system scope. opts.Credentials is not read: the
// caller holds it for this node's own recycle (LocalCredentials.Hold).
func (c *Coordinator) Request(ctx context.Context, tenantID, database string, opts seal.MigrationOptions, ropts ...RequestOption) (*ClusterMigration, error) {
	var ro requestOptions
	for _, o := range ropts {
		o(&ro)
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if !c.isLeader() {
		return nil, ErrNotLeader
	}

	shardID, owners, err := c.owners.Owners(ctx, tenantID, database)
	if err != nil {
		return nil, &StoreError{Op: "resolve-owners", Err: err}
	}
	if len(owners) == 0 {
		return nil, ErrNoOwners
	}

	// Bump the generation past any prior request for this scope so a stale or
	// aborted request's acks never satisfy the new one (the Store completion
	// logic matches on Generation).
	c.mu.Lock()
	defer c.mu.Unlock()
	var gen uint64 = 1
	prev, prevRaw, err := c.store.readMigration(ctx, tenantID, database)
	if err != nil {
		return nil, err
	}
	if prev != nil {
		gen = prev.Generation + 1
	}

	mech := opts.Mechanism
	if mech == "" {
		mech = seal.MechanismSwap
	}
	m := &ClusterMigration{
		TenantID:        tenantID,
		Database:        database,
		ShardID:         shardID,
		Target:          opts.TargetStrategy,
		Mechanism:       mech,
		ShamirThreshold: opts.ShamirThreshold,
		ShamirTotal:     opts.ShamirTotal,
		Owners:          owners,
		Generation:      gen,
	}
	if c.nowUnix != nil {
		m.RequestedAtUnix = c.nowUnix()
	}
	if c.rolling != nil && c.rolling() {
		m.Steps = rollingSteps(owners, c.self)
	}

	var undo func()
	if ro.prepare != nil {
		u, err := ro.prepare(m)
		if err != nil {
			return nil, err
		}
		undo = u
	}
	if perr := c.store.swapMigration(ctx, prevRaw, m); perr != nil {
		if undo != nil {
			undo()
		}
		return nil, perr
	}
	return m, nil
}

// IsLeader reports whether this node is currently the cluster leader (per the
// configured probe). Callers use it to skip leader-only work — e.g. a follower
// avoids scanning the coordination store every reconcile tick just to discover
// it has nothing to reap.
func (c *Coordinator) IsLeader() bool { return c.isLeader() }

// Reap clears completed migration records. The leader calls it after observing
// IsComplete so finished coordination state does not accumulate. It is a no-op
// for an incomplete or absent migration, making it safe to call repeatedly.
func (c *Coordinator) Reap(ctx context.Context, tenantID, database string) (bool, error) {
	if !c.isLeader() {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store.deleteIfComplete(ctx, tenantID, database)
}

// Advance moves a rolling migration on: once the owner whose step is
// StepRecycling has acked the current Generation with PhaseComplete and, with
// WithCatchUp, its Raft log has caught up, its step becomes StepServing and
// the next owner's StepRecycling, in one write. The first call that sees the
// ack records the catch-up target in the step (NodeStep.CatchUpIndex); a later
// call, on this leader or the next, hands on once the owner reaches it. It
// reports whether it wrote the record. A migration without steps, one whose
// recycling owner has not acked or caught up yet, and every call on a node
// that does not coordinate the migration (coordinates) are left alone, so at
// most one owner is ever away from the cluster for the migration, and a
// coordinator that restarts, or a new leader's, picks up from the replicated
// record where the last one left off.
// Every write is guarded on the record as read, so a former leader's
// Advance is refused with ErrMigrationMoved rather than undoing a newer one.
func (c *Coordinator) Advance(ctx context.Context, tenantID, database string) (bool, error) {
	if c.progress == nil && !c.isLeader() {
		return false, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m, raw, err := c.store.readMigration(ctx, tenantID, database)
	if err != nil || m == nil || !c.coordinates(m.ShardID) {
		return false, err
	}
	next := slices.IndexFunc(m.Steps, func(s NodeStep) bool { return s.State != StepServing })
	if next < 0 {
		return false, nil
	}
	var handedOn *NodeStep
	if m.Steps[next].State == StepRecycling {
		st, err := c.store.GetNodeStatus(ctx, tenantID, database, m.Steps[next].NodeID)
		if err != nil {
			return false, err
		}
		if st == nil || st.Generation != m.Generation || st.Phase != seal.PhaseComplete {
			return false, nil
		}
		done, changed, err := c.caughtUp(m.ShardID, &m.Steps[next])
		if err != nil || (!done && !changed) {
			return false, err
		}
		if done {
			m.Steps[next].State = StepServing
			handedOn = &m.Steps[next]
			next++
			if next < len(m.Steps) {
				m.Steps[next].State = StepRecycling
			}
		}
	} else {
		m.Steps[next].State = StepRecycling
	}
	if err := c.store.swapMigration(ctx, raw, m); err != nil {
		return false, err
	}
	if handedOn != nil {
		c.logger.Info("cluster-migration step handed on",
			slog.String("scope", scopeLabel(tenantID, database)),
			slog.Uint64("generation", m.Generation),
			slog.String("node", handedOn.NodeID),
			slog.Uint64("catch_up_index", handedOn.CatchUpIndex))
	}
	return true, nil
}
