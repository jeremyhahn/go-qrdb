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
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"
)

// DefaultReconcileInterval is the fallback period between reconciliation cycles
// when none is configured.
const DefaultReconcileInterval = 5 * time.Minute

// walkDirFn is filepath.WalkDir, indirected so a test can inject a walk
// failure. Making a subdirectory unreadable is not a substitute: CI job
// containers run as root, and root walks a mode-0000 directory successfully
// (conventions/testing-guidelines.md, Fault Injection).
var walkDirFn = filepath.WalkDir

// StorageTenant is the projection of a tenant the Reconciler needs to correct
// its storage-byte usage. Volume tenants store all their data under a dedicated
// DataRoot, which can be walked for an accurate on-disk size; shared/prefix
// tenants have no separable on-disk footprint and are left to the in-memory
// accumulator's flushed value.
type StorageTenant struct {
	// ID is the tenant identifier.
	ID string
	// DataRoot is the tenant's dedicated filesystem root (volume tenants only).
	DataRoot string
	// Volume reports whether the tenant uses IsolationVolume (dedicated DataRoot).
	Volume bool
}

// TenantListFunc enumerates the tenants the Reconciler should reconcile.
type TenantListFunc func(ctx context.Context) ([]StorageTenant, error)

// UsageSetFunc persists a tenant's authoritative storage-byte usage, replacing
// (not adjusting) the durable counter and bypassing limit enforcement so true
// over-limit usage can be recorded.
type UsageSetFunc func(ctx context.Context, tenantID string, bytes int64) error

// ReconcilerConfig configures a storage-quota Reconciler.
type ReconcilerConfig struct {
	// Accountant is the in-memory accountant whose deltas are flushed and whose
	// base usage is corrected each cycle. Required.
	Accountant *Accountant
	// Tenants enumerates the tenants to reconcile. Required.
	Tenants TenantListFunc
	// SetUsage persists a tenant's corrected on-disk usage. Required.
	SetUsage UsageSetFunc
	// Interval is the period between cycles. <= 0 uses DefaultReconcileInterval.
	Interval time.Duration
	// Logger is used for best-effort warnings. nil falls back to slog.Default().
	Logger *slog.Logger
}

// Reconciler periodically corrects per-tenant storage-byte usage. Each cycle it
// first flushes the Accountant's accumulated deltas to the durable counters,
// then for every IsolationVolume tenant it walks the tenant's DataRoot, sums the
// file sizes, and writes that authoritative figure back (correcting any drift
// introduced by the bounded-staleness accumulator, overwrite over-counting, or
// out-of-band changes). Shared/prefix tenants have no cleanly separable on-disk
// footprint within the shared store, so their usage is left at the accumulator's
// flushed approximation rather than paying for an expensive full prefix scan
// every cycle.
type Reconciler struct {
	cfg      ReconcilerConfig
	interval time.Duration
	logger   *slog.Logger
	running  atomic.Bool
}

// NewReconciler validates cfg and returns a Reconciler. It returns
// ErrNilAccountant, ErrNilTenantLister, or ErrNilUsageSetter when a required
// dependency is missing.
func NewReconciler(cfg ReconcilerConfig) (*Reconciler, error) {
	if cfg.Accountant == nil {
		return nil, ErrNilAccountant
	}
	if cfg.Tenants == nil {
		return nil, ErrNilTenantLister
	}
	if cfg.SetUsage == nil {
		return nil, ErrNilUsageSetter
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultReconcileInterval
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{cfg: cfg, interval: interval, logger: logger}, nil
}

// Run drives reconciliation on the configured interval until ctx is cancelled.
// It blocks, so callers typically invoke it in a goroutine. Cycles never
// overlap: each tick runs to completion before the next is serviced. On ctx
// cancellation it performs one final flush so in-flight accumulator deltas are
// not lost on shutdown.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.cfg.Accountant.Flush(context.Background())
			return
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

// RunOnce performs a single reconciliation cycle synchronously: it flushes the
// accountant and corrects each volume tenant's on-disk usage. It is a no-op if
// a cycle is already in progress (the overlap guard). It is exposed for
// operator-triggered reconciliation and deterministic testing.
func (r *Reconciler) RunOnce(ctx context.Context) {
	r.runOnce(ctx)
}

// runOnce performs a single reconciliation cycle. The atomic guard makes it a
// no-op if a previous cycle (or a concurrent caller) is still in progress.
func (r *Reconciler) runOnce(ctx context.Context) {
	if !r.running.CompareAndSwap(false, true) {
		return
	}
	defer r.running.Store(false)

	// Persist pending accumulator deltas first so the durable counters reflect
	// recent writes before we reconcile volume tenants against their disk.
	r.cfg.Accountant.Flush(ctx)

	tenants, err := r.cfg.Tenants(ctx)
	if err != nil {
		r.logger.Warn("quota reconciler: tenant enumeration failed", slog.Any("error", err))
		return
	}

	for _, t := range tenants {
		if !t.Volume || t.DataRoot == "" {
			// Shared/prefix tenants rely on the flushed accumulator value
			// (documented approximation; no per-tenant on-disk scan).
			continue
		}
		size, err := dirSize(t.DataRoot)
		if err != nil {
			r.logger.Warn("quota reconciler: data root walk failed",
				slog.String("tenant_id", t.ID),
				slog.String("data_root", t.DataRoot),
				slog.Any("error", err))
			continue
		}
		if err := r.cfg.SetUsage(ctx, t.ID, size); err != nil {
			r.logger.Warn("quota reconciler: usage update failed",
				slog.String("tenant_id", t.ID),
				slog.Int64("bytes", size),
				slog.Any("error", err))
			continue
		}
		// Realign the in-memory base to the persisted truth so subsequent
		// limit checks use the corrected figure.
		r.cfg.Accountant.SetBase(t.ID, size)
		r.logger.Debug("quota reconciler: corrected volume tenant usage",
			slog.String("tenant_id", t.ID), slog.Int64("bytes", size))
	}
}

// dirSize walks root and returns the total size in bytes of all regular files
// beneath it. A non-existent root reports zero (an un-provisioned tenant has no
// on-disk footprint). Directory entries and symlinks contribute nothing.
func dirSize(root string) (int64, error) {
	var total int64
	err := walkDirFn(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Tolerate a missing root entirely; surface other walk errors.
			if path == root {
				return filepath.SkipAll
			}
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}
