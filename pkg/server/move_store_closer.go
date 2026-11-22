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

// ReplicaCaughtUp reports whether this node's replica of (tenantID, db)
// has caught up with its raft group.
type ReplicaCaughtUp func(ctx context.Context, tenantID, db string) (bool, error)

// DefaultMoveCatchUpPoll is how often OpenTenantStores re-reads a
// reopened replica's catch-up state.
const DefaultMoveCatchUpPoll = 100 * time.Millisecond

// MoveStoreCloser is the seal.TenantStoreCloser over this node's shard
// lifecycle and barrier registry: a tenant-move step closes the tenant's
// shard replicas and barrier on this node, and reopens them from the
// directory the registry then resolves.
type MoveStoreCloser struct {
	shards   MoveShardLifecycle
	barriers MoveBarriers
	caughtUp ReplicaCaughtUp
	poll     time.Duration

	mu     sync.Mutex
	closed map[string][]string
}

// NewMoveStoreCloser returns the closer over shards and barriers. caughtUp
// may be nil when every started replica counts as caught up (a node with
// no shard group, such as the embedded node). A non-positive poll uses
// DefaultMoveCatchUpPoll.
func NewMoveStoreCloser(shards MoveShardLifecycle, barriers MoveBarriers, caughtUp ReplicaCaughtUp, poll time.Duration) (*MoveStoreCloser, error) {
	if shards == nil || barriers == nil {
		return nil, errors.New("server: move store closer needs a shard lifecycle and a barrier registry")
	}
	if poll <= 0 {
		poll = DefaultMoveCatchUpPoll
	}
	return &MoveStoreCloser{shards: shards, barriers: barriers, caughtUp: caughtUp, poll: poll, closed: map[string][]string{}}, nil
}

// CloseTenantStores stops every shard of tenantID started on this node,
// then closes its barrier, and remembers the shards for the reopen.
func (c *MoveStoreCloser) CloseTenantStores(_ context.Context, tenantID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	dbs := append([]string(nil), c.closed[tenantID]...)
	for _, s := range c.shards.ListStarted() {
		if s.TenantID != tenantID {
			continue
		}
		if err := c.shards.Close(tenantID, s.Database); err != nil {
			return err
		}
		dbs = appendUnique(dbs, s.Database)
	}
	sort.Strings(dbs)
	c.closed[tenantID] = dbs
	return c.barriers.CloseVolumeTenant(tenantID)
}

// OpenTenantStores reopens tenantID's barrier, restarts the shards
// CloseTenantStores stopped, and returns once each reports caught up or
// ctx ends.
func (c *MoveStoreCloser) OpenTenantStores(ctx context.Context, tenantID string) error {
	c.mu.Lock()
	dbs := c.closed[tenantID]
	c.mu.Unlock()
	if err := c.barriers.ReopenVolumeTenant(ctx, tenantID); err != nil {
		return err
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
