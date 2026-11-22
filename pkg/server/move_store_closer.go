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
	CloseVolumeTenant(tenantID string) error
	ReopenVolumeTenant(ctx context.Context, tenantID string) error
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
func (c *MoveStoreCloser) CloseTenantStores(_ context.Context, tenantID string) error {
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
	return c.barriers.CloseVolumeTenant(tenantID)
}

// OpenTenantStores reopens tenantID's barrier, restarts the shards and Host
// CloseTenantStores stopped, and returns once each shard reports caught up
// or ctx ends.
func (c *MoveStoreCloser) OpenTenantStores(ctx context.Context, tenantID string) error {
	c.mu.Lock()
	dbs := c.closed[tenantID]
	host := c.closedHost[tenantID]
	c.mu.Unlock()
	if err := c.barriers.ReopenVolumeTenant(ctx, tenantID); err != nil {
		return err
	}
	if host {
		if err := c.hosts.Attach(ctx, tenantID); err != nil {
			return err
		}
	}
	for _, db := range dbs {
		if err := c.shards.Attach(ctx, tenantID, db); err != nil {
			return err
		}
	}
	for _, db := range dbs {
		if err := c.awaitCaughtUp(ctx, tenantID, db); err != nil {
			return err
		}
	}
	c.mu.Lock()
	delete(c.closed, tenantID)
	delete(c.closedHost, tenantID)
	c.mu.Unlock()
	return nil
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
