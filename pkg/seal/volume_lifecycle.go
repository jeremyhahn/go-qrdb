// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// defaultObserverTimeout bounds every single pool's Unlock/Lock call made
// by VolumeLifecycle's StorageBarrier observers. StorageBarrier.Seal calls
// lockAll synchronously and wipes the root key regardless of what it
// returns; without a bound, one hung Lock call (a device driver that never
// returns) would hang the whole node seal, and the key would never be
// wiped. See WithObserverTimeout.
const defaultObserverTimeout = 30 * time.Second

// VolumeLifecycle keeps a node's Lockable pool volumes in step with its
// system barrier's seal state, per decision D6 (ruled 2026-10-04:
// docs/volume-pools/architecture.md#decisions-ruled-2026-10-04):
//
//   - UNSEAL: barrier unseal -> unlock every tracked Lockable volume ->
//     (the composer then opens tenant/database stores on them).
//   - SEAL, including a whole-node seal and the cluster cut-off: close
//     every per-tenant store on the volume (CloseStoresFunc) -> lock
//     every tracked Lockable volume -> wipe the barrier key.
//
// A VolumeLifecycle registers its own StorageBarrier.OnUnsealed and
// OnSealing observers at construction, so build it over a node's system
// barrier BEFORE building the node's BarrierRegistry: StorageBarrier runs
// OnUnsealed observers in registration order, and BarrierRegistry
// registers its own observer (bringing back system-managed tenants, which
// opens their stores) at its own construction. Registering first is what
// makes "unlock before stores open" true without either package reaching
// into the other's internals.
//
// A failed Unlock leaves that pool's volume unavailable (logged and
// audited; OpenVolumes.Root refuses it via Health, not any state this type
// tracks itself) without touching any other tracked pool; this matches
// the rule that a pool unavailable on this node only refuses that pool's
// tenants on this node. A failed Lock is logged, audited and folded into
// Seal's returned error, but the barrier's root key is wiped regardless:
// Seal always completes, bounded by WithObserverTimeout even if the
// underlying Lock call itself never returns.
//
// VolumeLifecycle never reads the system barrier's master key directly:
// every key it hands to a Lockable's Unlock comes from a volume.KeyProvider
// (WithVolumeKeyProvider), defaulting to the software HKDF derivation
// (softwareVolumeKeyProvider). A composer with its own key custody injects
// its own implementation there; this package never knows the difference.
type VolumeLifecycle struct {
	logger *slog.Logger
	system *StorageBarrier
	nodeID string

	mu    sync.RWMutex
	pools map[string]volume.Lockable

	keyProvider volume.KeyProvider

	// closeStores is called for a pool, under a bounded context, BEFORE
	// Lock: it must close every per-tenant store (Pebble, raft host) this
	// node has open on that pool, so Lock never runs against a volume
	// with live file handles still on it. The composer wires a real
	// implementation (it needs the per-tenant registry and raft backend
	// factory, which this package cannot import); the default is a no-op,
	// correct only for a deployment with no per-tenant stores to close
	// (e.g. tests, or a pool with no tenants on it yet).
	closeStores func(ctx context.Context, pool string) error

	// observerTimeout bounds every single pool's Unlock/Lock call. See
	// WithObserverTimeout.
	observerTimeout time.Duration
}

// VolumeLifecycleOption configures a VolumeLifecycle at construction time.
type VolumeLifecycleOption func(*VolumeLifecycle)

// WithVolumeKeyProvider replaces the volume.KeyProvider VolumeLifecycle
// derives every Unlock key through. Nil is a no-op, leaving the default
// software HKDF derivation in place. A composer with hardware-rooted (or
// otherwise non-software) key custody injects its own implementation here;
// go-qrdb never imports that custody code, and this is the ONLY seam
// VolumeLifecycle obtains a key through -- it never calls
// StorageBarrier.GetMasterKey itself.
func WithVolumeKeyProvider(p volume.KeyProvider) VolumeLifecycleOption {
	return func(vl *VolumeLifecycle) {
		if p != nil {
			vl.keyProvider = p
		}
	}
}

// WithCloseStoresFunc replaces the function VolumeLifecycle calls for a
// pool, under a bounded context, before Lock. Nil is a no-op, leaving the
// default no-op closer in place (correct only when nothing has a store
// open on the pool). The composer's implementation must close every
// per-tenant store open on that pool (Pebble, raft host) so Lock never
// races live file handles.
func WithCloseStoresFunc(fn func(ctx context.Context, pool string) error) VolumeLifecycleOption {
	return func(vl *VolumeLifecycle) {
		if fn != nil {
			vl.closeStores = fn
		}
	}
}

// WithObserverTimeout replaces the bound placed on every single pool's
// Unlock/Lock call (default defaultObserverTimeout). A zero or negative
// duration is a no-op, leaving the default in place: these observers are
// never allowed to block forever, since StorageBarrier.Seal wipes the root
// key only after lockAll returns.
func WithObserverTimeout(d time.Duration) VolumeLifecycleOption {
	return func(vl *VolumeLifecycle) {
		if d > 0 {
			vl.observerTimeout = d
		}
	}
}

// softwareVolumeKeyProvider is the default, software-only
// volume.KeyProvider: HKDF derivation from the system barrier's own
// unsealed root key (volume.DeriveVolumeKey), via StorageBarrier
// .GetMasterKey. It is the implementation VolumeLifecycle uses unless a
// composer supplies its own with WithVolumeKeyProvider; the master key is
// never retained beyond this one call and is wiped immediately after
// deriving.
type softwareVolumeKeyProvider struct {
	system *StorageBarrier
}

// VolumeKey implements volume.KeyProvider.
func (p *softwareVolumeKeyProvider) VolumeKey(ctx context.Context, nodeID, pool, _ string) (volume.VolumeKey, error) {
	masterKey, err := p.system.GetMasterKey(ctx)
	if err != nil {
		return volume.VolumeKey{}, err
	}
	defer secure.Wipe(masterKey)
	return volume.DeriveVolumeKey(masterKey, nodeID, pool)
}

// NewVolumeLifecycle builds a VolumeLifecycle over system and registers its
// unlock/lock observers immediately. nodeID is bound into every VolumeKey
// this VolumeLifecycle derives (by the default key provider; a composer's
// own provider may use it however it needs to), so it must be this node's
// own stable identity -- the same one Spec.NodeID carries when the
// volume's Provider opened it.
func NewVolumeLifecycle(system *StorageBarrier, nodeID string, logger *slog.Logger, opts ...VolumeLifecycleOption) *VolumeLifecycle {
	if logger == nil {
		logger = slog.Default()
	}
	vl := &VolumeLifecycle{
		logger:          logger,
		system:          system,
		nodeID:          nodeID,
		pools:           make(map[string]volume.Lockable),
		keyProvider:     &softwareVolumeKeyProvider{system: system},
		closeStores:     func(context.Context, string) error { return nil },
		observerTimeout: defaultObserverTimeout,
	}
	for _, opt := range opts {
		opt(vl)
	}
	system.OnUnsealed(vl.unlockAll)
	system.OnSealing(vl.lockAll)
	return vl
}

// Track registers the Lockable opened for pool on this node. Returns
// *VolumeAlreadyTrackedError on a duplicate pool name; the caller must
// Untrack it first (after closing the old Volume) to replace it.
func (vl *VolumeLifecycle) Track(pool string, l volume.Lockable) error {
	if pool == "" {
		return &InvalidVolumeLifecycleError{Reason: "empty pool name"}
	}
	if l == nil {
		return &InvalidVolumeLifecycleError{Reason: "nil lockable"}
	}
	vl.mu.Lock()
	defer vl.mu.Unlock()
	if _, exists := vl.pools[pool]; exists {
		return &VolumeAlreadyTrackedError{Pool: pool}
	}
	vl.pools[pool] = l
	return nil
}

// Untrack stops tracking pool, e.g. after its Volume has been closed. It is
// a no-op when pool is not tracked.
func (vl *VolumeLifecycle) Untrack(pool string) {
	vl.mu.Lock()
	defer vl.mu.Unlock()
	delete(vl.pools, pool)
}

// Tracked returns the sorted names of every pool this VolumeLifecycle
// currently tracks.
func (vl *VolumeLifecycle) Tracked() []string {
	vl.mu.RLock()
	defer vl.mu.RUnlock()
	names := make([]string, 0, len(vl.pools))
	for name := range vl.pools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// lockableFor returns the Lockable tracked for pool, or nil when untracked.
func (vl *VolumeLifecycle) lockableFor(pool string) volume.Lockable {
	vl.mu.RLock()
	defer vl.mu.RUnlock()
	return vl.pools[pool]
}

// unlockAll is the StorageBarrier.OnUnsealed observer. For every tracked
// pool it derives that pool's VolumeKey through the configured
// volume.KeyProvider and calls Unlock, each bounded by observerTimeout. A
// pool whose Unlock fails (or times out) is logged and audited and left
// locked; the loop continues to the remaining pools rather than stopping,
// matching the per-pool (not per-node) blast radius of a failed unlock.
func (vl *VolumeLifecycle) unlockAll(ctx context.Context) {
	for _, pool := range vl.Tracked() {
		pctx, cancel := context.WithTimeout(ctx, vl.observerTimeout)
		err := vl.unlockOne(pctx, pool)
		cancel()
		if err != nil {
			vl.logger.Error("volume: unlock failed",
				slog.String("pool", pool), slog.String("error", err.Error()))
			vl.system.Audit("volume_unlock", "deny", map[string]string{"pool": pool, "error": err.Error()})
			continue
		}
		vl.logger.Info("volume: unlocked", slog.String("pool", pool))
		vl.system.Audit("volume_unlock", "allow", map[string]string{"pool": pool})
	}
}

// unlockOne derives pool's VolumeKey through the configured KeyProvider
// and unlocks it, wiping the key immediately after the call returns
// regardless of outcome.
func (vl *VolumeLifecycle) unlockOne(ctx context.Context, pool string) error {
	l := vl.lockableFor(pool)
	if l == nil {
		return nil
	}
	key, err := vl.keyProvider.VolumeKey(ctx, vl.nodeID, pool, pool)
	if err != nil {
		return &VolumeUnlockError{Pool: pool, Err: err}
	}
	defer key.Wipe()
	if err := l.Unlock(ctx, key); err != nil {
		return &VolumeUnlockError{Pool: pool, Err: err}
	}
	return nil
}

// lockAll is the StorageBarrier.OnSealing observer. It is called by Seal
// BEFORE the EpochBarrier wipes the root key. For every tracked pool, in
// order: closeStores (closing every per-tenant store this node has open
// on that pool) runs first, then Lock, both bounded by observerTimeout --
// so "close stores -> lock -> wipe key" (decision D6) is actually true,
// not merely documented. A pool whose closeStores or Lock fails (or times
// out) is logged, audited and folded into the joined error this returns,
// but every other tracked pool is still attempted, and the caller
// (StorageBarrier.Seal) wipes the key regardless of what this returns --
// including when a Lock call itself never returns: lockOne runs it in its
// own goroutine and reports a timeout rather than blocking lockAll (and
// therefore Seal, and therefore the key wipe) forever.
func (vl *VolumeLifecycle) lockAll(ctx context.Context) error {
	var joined error
	for _, pool := range vl.Tracked() {
		l := vl.lockableFor(pool)
		if l == nil {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, vl.observerTimeout)
		err := vl.lockOne(pctx, pool, l)
		cancel()
		if err != nil {
			wrapped := &VolumeLockError{Pool: pool, Err: err}
			vl.logger.Error("volume: lock failed",
				slog.String("pool", pool), slog.String("error", err.Error()))
			vl.system.Audit("volume_lock", "deny", map[string]string{"pool": pool, "error": err.Error()})
			joined = errors.Join(joined, wrapped)
			continue
		}
		vl.logger.Info("volume: locked", slog.String("pool", pool))
		vl.system.Audit("volume_lock", "allow", map[string]string{"pool": pool})
	}
	return joined
}

// lockOne closes every per-tenant store open on pool, then locks it. Lock
// runs in its own goroutine so a Lock implementation that never returns
// cannot hang this call past ctx's deadline; *VolumeLockTimeoutError is
// returned in that case and the abandoned goroutine's eventual Lock
// result, if it ever arrives, is discarded.
func (vl *VolumeLifecycle) lockOne(ctx context.Context, pool string, l volume.Lockable) error {
	if err := vl.closeStores(ctx, pool); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- l.Lock(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return &VolumeLockTimeoutError{Pool: pool}
	}
}
