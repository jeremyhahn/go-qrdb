// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"sort"
	"sync"
)

// OpenVolumes is the set of pool volumes this node has opened, keyed by
// pool name. The composition layer builds one at boot by iterating node
// configuration (config.VolumePoolConfig), looking each pool's Provider up
// in a Registry, and calling Provider.Open; the Lockable ones are also
// Tracked with a seal.VolumeLifecycle (decision D6) so they are unlocked
// before anything resolves through them.
//
// Root(pool) (string, error) gives OpenVolumes the shape
// seal.BarrierRegistry's VolumeResolver option expects, so BarrierRegistry
// resolves a tenant's pool to a local directory without this package
// knowing anything about pkg/seal.
type OpenVolumes struct {
	mu      sync.RWMutex
	volumes map[string]Volume
	nodeID  string
}

// NewOpenVolumes returns an empty OpenVolumes for nodeID, used only to
// name this node in PoolUnavailableError when Root is asked for a pool
// that was never opened or has since failed.
func NewOpenVolumes(nodeID string) *OpenVolumes {
	return &OpenVolumes{volumes: make(map[string]Volume), nodeID: nodeID}
}

// Add records that pool's volume is v. Returns *VolumeAlreadyOpenError on
// a duplicate; the caller must Remove it first to replace it.
func (o *OpenVolumes) Add(pool string, v Volume) error {
	if pool == "" {
		return &InvalidSpecError{Pool: pool, Reason: "empty pool name"}
	}
	if v == nil {
		return &InvalidProviderError{Reason: "nil volume"}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, exists := o.volumes[pool]; exists {
		return &VolumeAlreadyOpenError{Pool: pool}
	}
	o.volumes[pool] = v
	return nil
}

// Remove stops tracking pool and returns the Volume that was tracked, if
// any. The caller closes it; Remove does not. A no-op (ok=false) when the
// pool was not tracked.
func (o *OpenVolumes) Remove(pool string) (Volume, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	v, ok := o.volumes[pool]
	if ok {
		delete(o.volumes, pool)
	}
	return v, ok
}

// Get returns the Volume open for pool, if any.
func (o *OpenVolumes) Get(pool string) (Volume, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	v, ok := o.volumes[pool]
	return v, ok
}

// Root returns the local root directory of pool's opened volume. Returns
// *PoolUnavailableError when the pool was never opened on this node, or
// when it IS open but its Health is not StateReady -- a Lockable volume
// before its first unlock, after a failed unlock, or after a re-seal
// locked it again. Root never falls through to the bare mount point for a
// volume it cannot confirm is unlocked: being open (tracked here) and
// being readable are different things for a Lockable volume, and only
// Health, not presence in this map, tells the two apart.
func (o *OpenVolumes) Root(ctx context.Context, pool string) (string, error) {
	v, ok := o.Get(pool)
	if !ok {
		return "", &PoolUnavailableError{Pool: pool, NodeID: o.nodeID}
	}
	if health := v.Health(ctx); health.State != StateReady {
		return "", &PoolUnavailableError{Pool: pool, NodeID: o.nodeID}
	}
	return v.Root(), nil
}

// Pools returns the sorted names of every pool currently open.
func (o *OpenVolumes) Pools() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	names := make([]string, 0, len(o.volumes))
	for name := range o.volumes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
