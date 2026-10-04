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

package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// clusterMigReconcileInterval is how often each node polls the coordination
// store for migration work it owes (and, on the leader, reaps completed
// migrations). The clustermig records are small and the pass is idempotent, so a
// short interval keeps a rolling migration progressing without a watch. It is a
// var (not const) only so tests can shorten it; production never reassigns it.
var clusterMigReconcileInterval = 3 * time.Second

// runClusterMigReconciler runs the per-node reconciliation loop until ctx is
// cancelled: each tick it performs any local recycle this node owes, then (only
// the leader actually acts) reaps completed migration records so the coordination
// state does not accumulate.
func runClusterMigReconciler(ctx context.Context, r *clustermig.Reconciler, coord *clustermig.Coordinator, store *clustermig.Store) {
	ticker := time.NewTicker(clusterMigReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := r.ReconcileOnce(ctx); err != nil {
				slog.Warn("cluster-migration reconcile pass failed", slog.Any("error", err))
			} else if n > 0 {
				slog.Info("cluster-migration: recycled local scopes", slog.Int("count", n))
			}
			reapCompletedMigrations(ctx, coord, store)
		}
	}
}

// reapCompletedMigrations deletes every migration record that has reached
// cluster-wide completion. Reap is leader-gated and no-ops on a follower or an
// incomplete migration, so this is safe to call on every node each tick.
func reapCompletedMigrations(ctx context.Context, coord *clustermig.Coordinator, store *clustermig.Store) {
	// Followers have nothing to reap (Reap is leader-gated); skip the store scan
	// entirely so a follower does not pay a __sys:encmig/ prefix scan every tick.
	if !coord.IsLeader() {
		return
	}
	migrations, err := store.ListMigrations(ctx)
	if err != nil {
		return
	}
	for _, m := range migrations {
		if _, rerr := coord.Reap(ctx, m.TenantID, m.Database); rerr != nil {
			slog.Warn("cluster-migration reap failed",
				slog.String("tenant", m.TenantID), slog.String("database", m.Database),
				slog.Any("error", rerr))
		}
	}
}

// daemonLocalRecycler implements clustermig.LocalRecycler by delegating to the
// local scoped encryption migrator — the exact in-process recycle the transports
// would have run directly in a single-node deployment. It maps the replicated
// migration record back to a BarrierScope + MigrationOptions, threading this
// node's own credential through.
type daemonLocalRecycler struct {
	migrator   service.EncryptionMigrator
	barrierReg *seal.BarrierRegistry
}

// Compile-time check.
var _ clustermig.LocalRecycler = (*daemonLocalRecycler)(nil)

// alreadyAtTarget reports whether this node's local barrier for the scope is
// already sealed by the migration's target strategy. It makes Recycle idempotent
// across the SYSTEM-scope in-process reboot (which tears down the data plane —
// including the reconciler — before the ack can be written) and across a re-issue:
// after the reboot+unseal the new reconciler sees the keyed barrier and acks
// without re-recycling. Fails open (false → attempt the recycle) when the barrier
// is unavailable.
func (r *daemonLocalRecycler) alreadyAtTarget(m *clustermig.ClusterMigration) bool {
	if r.barrierReg == nil {
		return false
	}
	// Resolve the scope's current strategy once, then compare once.
	var st *seal.BarrierStatus
	if m.TenantID == "" && m.Database == "" {
		if sb := r.barrierReg.SystemBarrier(); sb != nil {
			st = sb.Status()
		}
	} else if tb, err := r.barrierReg.GetTenant(m.TenantID); err == nil && tb != nil {
		st = tb.Status()
	}
	return st != nil && st.Strategy == m.Target
}

// Recycle performs this node's local recycle for the migration record.
func (r *daemonLocalRecycler) Recycle(ctx context.Context, m *clustermig.ClusterMigration, credential []byte) error {
	if r.migrator == nil {
		return service.ErrMigrationProviderUnavailable
	}
	// Idempotency: if this node's barrier is already keyed at the target, the
	// recycle ran (this is a post-reboot/re-issue pass) — ack without redoing it.
	if r.alreadyAtTarget(m) {
		return nil
	}
	// Hand the recycle its OWN credential. The SYSTEM-scope recycle is async
	// across an in-process reboot: EnableEncryption returns early (the boot
	// loop cancels its context) while handleSystemMigration keeps reading the
	// credential to seal the keyed root key. The caller (reconciler) wipes the
	// opened plaintext as soon as Recycle returns, so a credential aliasing that
	// buffer would be zeroed out from under the still-running recycle — sealing
	// the root key under a zeroed passphrase. CredentialsFor decouples the two:
	// it seals its own copy of the material and leaves the reconciler's buffer
	// alone.
	creds, err := seal.CredentialsFor(ctx, credential)
	if err != nil {
		return err
	}
	opts := seal.MigrationOptions{
		TargetStrategy:  m.Target,
		Mechanism:       m.Mechanism,
		Availability:    seal.AvailabilityQuiesced,
		Credentials:     creds,
		ShamirThreshold: m.ShamirThreshold,
		ShamirTotal:     m.ShamirTotal,
	}
	if opts.Mechanism == "" {
		opts.Mechanism = seal.MechanismSwap
	}
	scope := service.BarrierScope{Kind: service.ScopeSystem}
	if m.TenantID != "" || m.Database != "" {
		scope = service.BarrierScope{Kind: service.ScopeTenant, TenantID: m.TenantID}
		// Tenant-scope recycle is SYNCHRONOUS (online, no reboot): EnableEncryption
		// fully consumes the credential before returning, so destroy it here. The
		// system scope is async across the in-process reboot — its credential is
		// consumed by the boot loop (handleSystemMigration), which owns destroying
		// it; destroying here would pull it out from under the still-running
		// recycle.
		defer creds.Secret.Destroy()
	}
	return r.migrator.EnableEncryption(ctx, scope, opts)
}

// withCredentialBytes opens a credential's protected secret and calls fn with
// a view of the plaintext, valid only for the call; fn must not retain it. A
// credential with no secret yields fn(nil). The opened enclave is destroyed
// on return, so the plaintext exists only in locked memory and only for the
// width of fn.
func withCredentialBytes(ctx context.Context, creds seal.Credentials, fn func([]byte) error) error {
	if creds.Secret == nil {
		return fn(nil)
	}
	opened, err := creds.Secret.Open(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if derr := opened.Destroy(); derr != nil {
			slog.Warn("cluster migration: releasing an opened credential enclave reported a non-fatal problem",
				slog.String("error", derr.Error()))
		}
	}()
	return opened.WithBytes(fn)
}

// clusterCoordinatorMigrator implements service.EncryptionMigrator for the
// transports: an enable-encryption request becomes a replicated migration intent
// that the per-node reconcilers act on. The intent carries no credential: this
// node holds the request's credential in memory for its own recycle, and every
// other owner supplies its own. The request returns once the intent is durably
// written; the operator observes per-node/cluster completion via barrier
// status. It maps the BarrierScope to the coordinator's (tenantID, database) —
// a tenant scope migrates the whole tenant barrier (empty database) since all
// of a tenant's dedicated-raft databases share one barrier.
type clusterCoordinatorMigrator struct {
	coord       *clustermig.Coordinator
	credentials *clustermig.LocalCredentials // this node's credential source
	self        string                       // this node's ID among the owners
	barrierReg  *seal.BarrierRegistry        // dedicated-barrier authority check; may be nil
	modeCheck   barrierModeChecker           // target-strategy entitlement check; may be nil
}

// barrierModeChecker is the narrow slice of the tenant service the coordinator
// migrator needs to gate a tenant-scope request's target strategy against the
// tenant's entitlements (same governance as barrier-init). Satisfied by
// tenant.TenantServicer.
type barrierModeChecker interface {
	CheckBarrierMode(ctx context.Context, tenantID string, mode seal.StrategyID) error
}

// Compile-time check.
var _ service.EncryptionMigrator = (*clusterCoordinatorMigrator)(nil)

// EnableEncryption validates authority + entitlement, then writes the replicated
// migration intent for the scope. The gates run at REQUEST time (not just at
// per-node recycle time) so an unauthorized or unentitled request is rejected
// synchronously — before any credential is held or any intent is persisted —
// mirroring the scoped local migrator's gates (fail-fast + defense-in-depth).
func (m *clusterCoordinatorMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	tenantID := ""
	if scope.Kind == service.ScopeTenant {
		tenantID = scope.TenantID
		// Authority: a tenant may only migrate its OWN dedicated barrier. A tenant
		// without a registered dedicated barrier is on the shared system barrier and
		// cannot change its encryption.
		if m.barrierReg != nil {
			if _, err := m.barrierReg.GetTenant(tenantID); err != nil {
				if errors.Is(err, seal.ErrTenantNotFound) {
					return service.ErrSharedBarrierImmutable
				}
				return err
			}
		}
		// Governance: the tenant must be entitled to the requested target strategy.
		if m.modeCheck != nil {
			if err := m.modeCheck.CheckBarrierMode(ctx, tenantID, opts.TargetStrategy); err != nil {
				return err
			}
		}
	}
	// This node recycles with the request's own credential, held in protected
	// memory for the request's generation and never written anywhere. It is
	// held before the record is written, so this node's reconciler can never
	// read the record first and reach for a file instead. It is held only
	// when this node owns the scope and the target takes one: a Shamir target
	// mints its own key on every node.
	hold := func(rec *clustermig.ClusterMigration) (func(), error) {
		if rec.Target == seal.StrategyShamir || !slices.Contains(rec.Owners, m.self) {
			return nil, nil
		}
		held := false
		err := withCredentialBytes(ctx, opts.Credentials, func(credential []byte) error {
			if len(credential) == 0 {
				return nil
			}
			held = true
			return m.credentials.Hold(ctx, rec, credential)
		})
		if err != nil || !held {
			return nil, err
		}
		return func() { m.credentials.Release(rec) }, nil
	}
	_, err := m.coord.Request(ctx, tenantID, "", opts, clustermig.WithPrepare(hold))
	return err
}
