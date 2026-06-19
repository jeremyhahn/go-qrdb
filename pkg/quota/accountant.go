// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quota

import (
	"context"
	"log/slog"
	"sync"
)

// StorageQuotaFunc resolves a tenant's quota record, including both the
// configured limits and the persisted current-usage counters. It is the read
// side the Accountant uses to seed a tenant's in-memory base usage.
type StorageQuotaFunc func(ctx context.Context, tenantID string) (*Quotas, error)

// StorageFlushFunc persists a signed storage-byte delta for a tenant. A
// positive delta increases the tenant's persisted CurrentStorageBytes; a
// negative delta decreases it (clamped to zero by the implementation).
type StorageFlushFunc func(ctx context.Context, tenantID string, delta int64) error

// AccountantConfig configures a storage-byte Accountant.
type AccountantConfig struct {
	// Quotas resolves a tenant's limits and persisted usage. Required.
	Quotas StorageQuotaFunc
	// Flush persists accumulated per-tenant byte deltas. Required.
	Flush StorageFlushFunc
	// Logger is used for best-effort warnings. nil falls back to slog.Default().
	Logger *slog.Logger
}

// storageCounter holds the in-memory storage-byte accounting state for a single
// tenant. base is the persisted CurrentStorageBytes at the last load or flush;
// pending is the net signed delta accumulated since the last flush. The
// effective current usage is base+pending, floored at zero.
//
// baseGen is a generation counter bumped every time SetBase overwrites base with
// freshly-walked on-disk truth. Flush snapshots baseGen before it releases the
// lock for its persist I/O and re-checks it afterward: if a SetBase landed during
// the I/O window the recomputed base is already authoritative, so Flush must not
// re-add the flushed delta on top of it (which would double-count). See Flush.
type storageCounter struct {
	mu      sync.Mutex
	base    int64
	pending int64
	baseGen uint64
	loaded  bool
}

// effectiveLocked returns the tenant's effective usage (base+pending) floored at
// zero so an over-subtraction (more deletes accounted than the base reflects)
// can never report negative usage. The caller must hold c.mu.
func (c *storageCounter) effectiveLocked() int64 {
	e := c.base + c.pending
	if e < 0 {
		return 0
	}
	return e
}

// Accountant tracks per-tenant storage-byte usage in memory and enforces a
// tenant's MaxStorageBytes limit on the write path. Writes are admitted via
// Reserve (which rejects with *ExceededError when the limit would be exceeded)
// and accounted in an in-memory accumulator; a background reconciler invokes
// Flush periodically to persist the accumulated deltas to the durable
// CurrentStorageBytes counter and SetBase to correct drift. This trades bounded
// staleness of the persisted counter for a hot path that never blocks on
// storage I/O. The Accountant is safe for concurrent use.
type Accountant struct {
	quotas  StorageQuotaFunc
	flush   StorageFlushFunc
	logger  *slog.Logger
	tenants sync.Map // tenantID -> *storageCounter
}

// NewAccountant creates an Accountant from cfg. It returns ErrNilQuotaFunc or
// ErrNilFlushFunc when the required functions are missing.
func NewAccountant(cfg AccountantConfig) (*Accountant, error) {
	if cfg.Quotas == nil {
		return nil, ErrNilQuotaFunc
	}
	if cfg.Flush == nil {
		return nil, ErrNilFlushFunc
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Accountant{
		quotas: cfg.Quotas,
		flush:  cfg.Flush,
		logger: logger,
	}, nil
}

// counter returns the per-tenant storageCounter, creating it on first use.
func (a *Accountant) counter(tenantID string) *storageCounter {
	if v, ok := a.tenants.Load(tenantID); ok {
		return v.(*storageCounter)
	}
	actual, _ := a.tenants.LoadOrStore(tenantID, &storageCounter{})
	return actual.(*storageCounter)
}

// loadBase reads the persisted CurrentStorageBytes for a tenant. A lookup
// failure is logged and treated as zero so a transient error never blocks the
// write path; the reconciler corrects any resulting drift.
func (a *Accountant) loadBase(ctx context.Context, tenantID string) int64 {
	q, err := a.quotas(ctx, tenantID)
	if err != nil || q == nil {
		a.logger.Warn("quota accountant: base usage load failed",
			slog.String("tenant_id", tenantID), slog.Any("error", err))
		return 0
	}
	return q.CurrentStorageBytes
}

// Reserve verifies that adding n bytes keeps the tenant within limit and, when
// allowed, records the reservation in the in-memory accumulator. A limit <= 0
// is treated as unlimited. n <= 0 is a no-op. It returns *ExceededError when the
// write would exceed the limit; the caller must NOT proceed with the write in
// that case. After a reserved write fails downstream, call Release to roll back.
func (a *Accountant) Reserve(ctx context.Context, tenantID string, limit, n int64) error {
	if n <= 0 {
		return nil
	}
	c := a.counter(tenantID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		c.base = a.loadBase(ctx, tenantID)
		c.loaded = true
	}
	cur := c.effectiveLocked()
	if limit > 0 && cur+n > limit {
		return &ExceededError{
			TenantID: tenantID,
			Resource: ResourceStorageBytes,
			Current:  cur,
			Limit:    limit,
		}
	}
	c.pending += n
	return nil
}

// Release rolls back a reservation of n bytes recorded by a prior Reserve whose
// downstream write subsequently failed. n <= 0 is a no-op.
func (a *Accountant) Release(tenantID string, n int64) {
	if n <= 0 {
		return
	}
	c := a.counter(tenantID)
	c.mu.Lock()
	c.pending -= n
	c.mu.Unlock()
}

// Sub records that n bytes were freed for a tenant (e.g. by a delete),
// decreasing the in-memory accumulator. n <= 0 is a no-op.
func (a *Accountant) Sub(tenantID string, n int64) {
	if n <= 0 {
		return
	}
	c := a.counter(tenantID)
	c.mu.Lock()
	c.pending -= n
	c.mu.Unlock()
}

// SetBase overwrites a tenant's authoritative base usage to bytes, leaving any
// pending (unflushed) delta intact. The reconciler calls this after recomputing
// a volume tenant's true on-disk size so subsequent limit checks use the
// corrected figure without discarding writes that landed during the walk.
func (a *Accountant) SetBase(tenantID string, bytes int64) {
	if bytes < 0 {
		bytes = 0
	}
	c := a.counter(tenantID)
	c.mu.Lock()
	c.base = bytes
	c.baseGen++
	c.loaded = true
	c.mu.Unlock()
}

// Usage returns the effective current usage (base+pending) the Accountant is
// tracking for a tenant. It reports zero for tenants it has never seen.
func (a *Accountant) Usage(tenantID string) int64 {
	v, ok := a.tenants.Load(tenantID)
	if !ok {
		return 0
	}
	c := v.(*storageCounter)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.effectiveLocked()
}

// Flush persists every tenant's accumulated delta via the flush func, advancing
// the tenant's base by the flushed amount. The base+pending invariant is held
// constant across each tenant's flush, so the limit check stays conservative
// while the durable counter catches up. A flush error leaves the delta pending
// for the next cycle. Flush is safe for concurrent use.
//
// Flush snapshots the per-tenant baseGen alongside the pending delta before it
// releases the lock for the (potentially slow) persist I/O. After the I/O it
// always subtracts the flushed delta from pending — the delta is now durable, so
// it must not be flushed again — but only advances base by that delta when
// baseGen is unchanged. A SetBase that landed during the I/O window already reset
// base to the freshly-walked on-disk truth; re-adding the flushed delta on top
// of that authoritative figure would double-count, so in that case Flush drops
// the now-stale base advance and lets SetBase's value stand. A flush error skips
// both mutations, leaving the delta pending for the next cycle.
func (a *Accountant) Flush(ctx context.Context) {
	a.tenants.Range(func(k, v any) bool {
		tenantID := k.(string)
		c := v.(*storageCounter)

		c.mu.Lock()
		delta := c.pending
		gen := c.baseGen
		c.mu.Unlock()
		if delta == 0 {
			return true
		}

		if err := a.flush(ctx, tenantID, delta); err != nil {
			a.logger.Warn("quota accountant: flush failed",
				slog.String("tenant_id", tenantID),
				slog.Int64("delta", delta),
				slog.Any("error", err))
			return true
		}

		c.mu.Lock()
		c.pending -= delta
		if c.baseGen == gen {
			c.base += delta
		}
		c.mu.Unlock()
		return true
	})
}
