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
	"sort"
	"sync"
	"time"
)

// MoveShardLifecycle is the part of ShardLifecycle a MoveStoreCloser
// drives: which of a tenant's shards are started here, and stopping and
// restarting them.
type MoveShardLifecycle interface {
	Attach(ctx context.Context, tenantID, db string) error
	Close(tenantID, db string) error
	ListStarted() []StartedShard
}

// MoveBarriers is the part of seal.BarrierRegistry a MoveStoreCloser
// drives: closing and reopening one tenant's barrier.
type MoveBarriers interface {
	CloseVolumeTenant(ctx context.Context, tenantID string) error
	ReopenVolumeTenant(ctx context.Context, tenantID string) error
}

// MoveSealedBarriers is a MoveBarriers that reports a tenant barrier left
// sealed by ReopenVolumeTenant (a self-managed tenant awaiting its
// administrator's unseal). OpenTenantStores then attaches nothing: the
// stores attach on that unseal.
type MoveSealedBarriers interface {
	MoveBarriers
	TenantBarrierSealed(tenantID string) bool
}

// MoveTenantHosts is the part of TenantHostRegistry a MoveStoreCloser
// drives for an IsolationVolume tenant: its one Host on this node.
type MoveTenantHosts interface {
	Started(tenantID string) bool
	Attach(ctx context.Context, tenantID string) error
	Close(tenantID string) error
}

// ReplicaCaughtUp reports whether this node's replica of (tenantID, db)
// has caught up with its raft group.
type ReplicaCaughtUp func(ctx context.Context, tenantID, db string) (bool, error)

// DefaultMoveCatchUpPoll is how often OpenTenantStores re-reads a
// reopened replica's catch-up state.
const DefaultMoveCatchUpPoll = 100 * time.Millisecond

// MoveStoreCloser is the seal.TenantStoreCloser over this node's shard
// lifecycle, tenant host registry and barrier registry: a tenant-move step
// closes the tenant's shard replicas (or its Host) and barrier on this
// node, and reopens them from the directory the registry then resolves.
type MoveStoreCloser struct {
	shards   MoveShardLifecycle
	hosts    MoveTenantHosts
	barriers MoveBarriers
	caughtUp ReplicaCaughtUp
	poll     time.Duration

	mu         sync.Mutex
	closed     map[string][]string
	closedHost map[string]bool
	// barrierOpen names each closed tenant whose barrier a reopen has
	// already reopened, so a retry does not reopen it twice.
	barrierOpen map[string]bool
	// pending names each tenant whose reopen failed; RetryReopen finishes it.
	pending map[string]bool

	// holdMu guards held apart from mu: the placement reconciler reads the
	// hold while CloseTenantStores holds mu across its shard closes.
	holdMu sync.Mutex
	// held names each tenant whose stores a move step has closed or is
	// closing; the placement reconciler leaves a held tenant alone.
	held map[string]bool
}

// MoveStoreCloserOption configures a MoveStoreCloser.
type MoveStoreCloserOption func(*MoveStoreCloser)

// WithMoveTenantHosts sets the IsolationVolume host registry a move closes
// and reopens a volume tenant's Host through. A nil hosts is ignored.
func WithMoveTenantHosts(hosts MoveTenantHosts) MoveStoreCloserOption {
	return func(c *MoveStoreCloser) {
		if hosts != nil {
			c.hosts = hosts
		}
	}
}

// NewMoveStoreCloser returns the closer over shards and barriers. shards
// may be nil on a node with no IsolationRaft factory; caughtUp may be nil
// when every started replica counts as caught up (a node with no shard
// group, such as the embedded node). A non-positive poll uses
// DefaultMoveCatchUpPoll.
func NewMoveStoreCloser(shards MoveShardLifecycle, barriers MoveBarriers, caughtUp ReplicaCaughtUp, poll time.Duration, opts ...MoveStoreCloserOption) (*MoveStoreCloser, error) {
	if barriers == nil {
		return nil, errors.New("server: move store closer needs a barrier registry")
	}
	if poll <= 0 {
		poll = DefaultMoveCatchUpPoll
	}
	c := &MoveStoreCloser{shards: shards, barriers: barriers, caughtUp: caughtUp, poll: poll,
		closed: map[string][]string{}, closedHost: map[string]bool{}}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// CloseTenantStores stops every shard of tenantID started on this node and
// its Host, then closes its barrier, and remembers what it stopped for the
// reopen.
func (c *MoveStoreCloser) CloseTenantStores(ctx context.Context, tenantID string) (err error) {
	c.hold(tenantID)
	defer func() {
		if err != nil {
			// A failed close leaves the tenant to the reconciler again.
			c.release(tenantID)
		}
	}()
	c.mu.Lock()
	defer c.mu.Unlock()
	dbs := append([]string(nil), c.closed[tenantID]...)
	if c.shards != nil {
		for _, s := range c.shards.ListStarted() {
			if s.TenantID != tenantID {
				continue
			}
			if err := c.shards.Close(tenantID, s.Database); err != nil {
				return err
			}
			dbs = appendUnique(dbs, s.Database)
		}
	}
	sort.Strings(dbs)
	c.closed[tenantID] = dbs
	if c.hosts != nil && c.hosts.Started(tenantID) {
		if err := c.hosts.Close(tenantID); err != nil {
			return err
		}
		c.closedHost[tenantID] = true
	}
	if err := c.barriers.CloseVolumeTenant(ctx, tenantID); err != nil {
		return err
	}
	// A fresh close supersedes a pending reopen: the next OpenTenantStores
	// reopens the barrier again.
	delete(c.barrierOpen, tenantID)
	delete(c.pending, tenantID)
	return nil
}

// HoldsTenant reports whether a move step holds tenantID's stores: from the
// start of CloseTenantStores until OpenTenantStores returns. The placement
// reconciler neither re-attaches a held tenant's shards nor registers its
// barrier shell, so nothing reopens the tenant's directory mid-move.
func (c *MoveStoreCloser) HoldsTenant(tenantID string) bool {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	return c.held[tenantID]
}

func (c *MoveStoreCloser) hold(tenantID string) {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	if c.held == nil {
		c.held = map[string]bool{}
	}
	c.held[tenantID] = true
}

func (c *MoveStoreCloser) release(tenantID string) {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	delete(c.held, tenantID)
}

// tryHold holds tenantID unless something already holds it.
func (c *MoveStoreCloser) tryHold(tenantID string) bool {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	if c.held[tenantID] {
		return false
	}
	if c.held == nil {
		c.held = map[string]bool{}
	}
	c.held[tenantID] = true
	return true
}

// OpenTenantStores reopens tenantID's barrier, restarts the shards and Host
// CloseTenantStores stopped, and returns once each shard reports caught up
// or ctx ends.
func (c *MoveStoreCloser) OpenTenantStores(ctx context.Context, tenantID string) error {
	// Released on every return: a failed reopen leaves the tenant pending,
	// and RetryReopen (driven by the move worker's tick) finishes it. The
	// tenant stays unserved meanwhile because its barrier or stores are
	// closed, not because of the hold.
	defer c.release(tenantID)
	return c.reopen(ctx, tenantID)
}

// reopen runs the reopen path for tenantID idempotently: the barrier is
// reopened once per close, a Host or shard already started is not attached
// again, and every shard waits for its replica to catch up. Any failure
// marks the tenant pending a reopen; success clears every record of the
// close.
func (c *MoveStoreCloser) reopen(ctx context.Context, tenantID string) error {
	c.mu.Lock()
	dbs := append([]string(nil), c.closed[tenantID]...)
	host := c.closedHost[tenantID]
	barrierOpen := c.barrierOpen[tenantID]
	c.mu.Unlock()
	fail := func(err error) error {
		c.mu.Lock()
		if c.pending == nil {
			c.pending = map[string]bool{}
		}
		c.pending[tenantID] = true
		c.mu.Unlock()
		return err
	}
	if !barrierOpen {
		if err := c.barriers.ReopenVolumeTenant(ctx, tenantID); err != nil {
			return fail(err)
		}
		c.mu.Lock()
		if c.barrierOpen == nil {
			c.barrierOpen = map[string]bool{}
		}
		c.barrierOpen[tenantID] = true
		c.mu.Unlock()
	}
	if sr, ok := c.barriers.(MoveSealedBarriers); ok && sr.TenantBarrierSealed(tenantID) {
		// A self-managed barrier stays sealed until its administrator
		// unseals it, and Attach refuses a sealed barrier: the reopen is
		// done, and the shard reconciler (on the unseal notification) and
		// Services attach the stores once it serves.
		c.finishReopen(tenantID)
		return nil
	}
	if host && !c.hosts.Started(tenantID) {
		if err := c.hosts.Attach(ctx, tenantID); err != nil {
			return fail(err)
		}
	}
	started := map[string]bool{}
	if c.shards != nil && len(dbs) > 0 {
		for _, s := range c.shards.ListStarted() {
			if s.TenantID == tenantID {
				started[s.Database] = true
			}
		}
	}
	for _, db := range dbs {
		if started[db] {
			continue
		}
		if err := c.shards.Attach(ctx, tenantID, db); err != nil {
			return fail(err)
		}
	}
	for _, db := range dbs {
		if err := c.awaitCaughtUp(ctx, tenantID, db); err != nil {
			return fail(err)
		}
	}
	c.finishReopen(tenantID)
	return nil
}

// finishReopen forgets tenantID's close once its stores serve again.
func (c *MoveStoreCloser) finishReopen(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.closed, tenantID)
	delete(c.closedHost, tenantID)
	delete(c.barrierOpen, tenantID)
	delete(c.pending, tenantID)
}

// PendingReopen lists, sorted, each tenant whose last reopen failed and
// has not been retried to success.
func (c *MoveStoreCloser) PendingReopen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.pending))
	for id := range c.pending {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// RetryReopen reruns the reopen path for a tenant PendingReopen lists. It
// does nothing for a tenant not pending, or one a move step holds (that
// step owns the stores). A self-managed tenant still comes back sealed.
func (c *MoveStoreCloser) RetryReopen(ctx context.Context, tenantID string) error {
	c.mu.Lock()
	pending := c.pending[tenantID]
	c.mu.Unlock()
	if !pending || !c.tryHold(tenantID) {
		return nil
	}
	defer c.release(tenantID)
	return c.reopen(ctx, tenantID)
}

func (c *MoveStoreCloser) awaitCaughtUp(ctx context.Context, tenantID, db string) error {
	if c.caughtUp == nil {
		return nil
	}
	ticker := time.NewTicker(c.poll)
	defer ticker.Stop()
	for {
		ok, err := c.caughtUp(ctx, tenantID, db)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func appendUnique(s []string, v string) []string {
	for _, e := range s {
		if e == v {
			return s
		}
	}
	return append(s, v)
}
