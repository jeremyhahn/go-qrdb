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
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
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

// migrationCredentialTTL is the freshness window for an in-record sealed
// migration credential. It is far longer than the tenant-key-delivery default
// (5 min) so a node killed mid-migration can resume and re-open its envelope from
// the still-pending record after a restart. The migration receiver is built with
// keydelivery.WithoutReplayGuard (see start.go): the same envelope is legitimately
// re-opened on every reconciler retry pass, so a replay guard would brick a
// retryable per-node recycle. Replay is bounded instead by HPKE scope/node
// binding + the migration generation guard + consensus-only write access to the
// replicated record.
const migrationCredentialTTL = 24 * time.Hour

// sentinel scope fields satisfy keydelivery.Meta.validate() for a system-scope
// migration credential, which has no tenant/database/shard of its own. They only
// bind the HPKE context; the receiver matches them from the envelope.
const (
	systemCredMetaTenant   = "__system__"
	systemCredMetaDatabase = "__barrier__"
)

// migrationCredMeta builds the keydelivery context binding for one owner's sealed
// migration credential. Every field binds into the HPKE info, tying the sealed
// secret to the scope + target node; the receiver re-derives it from the
// envelope. System scope uses sentinels for the empty tenant/database/shard.
func migrationCredMeta(m *clustermig.ClusterMigration, owner string) keydelivery.Meta {
	meta := keydelivery.Meta{
		TenantID:     systemCredMetaTenant,
		Database:     systemCredMetaDatabase,
		ShardID:      1,
		ReplicaID:    1,
		TargetNodeID: owner,
		Mode:         keydelivery.ModeSelfManaged,
	}
	if m.TenantID != "" {
		meta.TenantID = m.TenantID
	}
	if m.Database != "" {
		meta.Database = m.Database
	}
	if m.ShardID != 0 {
		meta.ShardID = m.ShardID
	}
	return meta
}

// daemonCredentialSealer implements clustermig.CredentialSealer: it seals the
// migration unlock credential individually to each owning node's authenticated
// public key, returning per-node HPKE envelope blobs for the coordinator to embed
// in the replicated record. The local node is sealed to directly from its own
// public key (no network round-trip to itself); peers are sealed via the
// clusterkeys Sender, which fetches + authenticates each peer's delivery cert.
type daemonCredentialSealer struct {
	sender   *clusterkeys.Sender
	selfNode string
	selfPub  *ecdsa.PublicKey
}

// Compile-time check.
var _ clustermig.CredentialSealer = (*daemonCredentialSealer)(nil)

// SealFor seals credential to each owner, returning NodeID -> envelope JSON.
func (s *daemonCredentialSealer) SealFor(ctx context.Context, m *clustermig.ClusterMigration, owners []string, credential []byte) (map[string][]byte, error) {
	out := make(map[string][]byte, len(owners))
	for _, owner := range owners {
		meta := migrationCredMeta(m, owner)
		var (
			env *keydelivery.Envelope
			err error
		)
		switch {
		case owner == s.selfNode && s.selfPub != nil:
			env, err = keydelivery.Seal(s.selfPub, meta, credential)
		case s.sender != nil:
			// NodeID == RaftAddr in this deployment (placement NodeID convention).
			env, err = s.sender.SealFor(ctx, placement.Replica{NodeID: owner, RaftAddr: owner}, meta, credential)
		default:
			return nil, clustermig.ErrNoOwners
		}
		if err != nil {
			return nil, err
		}
		blob, merr := json.Marshal(env)
		if merr != nil {
			return nil, merr
		}
		out[owner] = blob
	}
	return out, nil
}

// daemonCredentialOpener implements clustermig.CredentialOpener: it opens THIS
// node's sealed migration credential from a record using the node's HPKE
// receiver (built over the node's private key). It returns (nil, nil) when no
// credential was sealed for this node.
type daemonCredentialOpener struct {
	receiver *keydelivery.Receiver
	selfNode string
}

// Compile-time check.
var _ clustermig.CredentialOpener = (*daemonCredentialOpener)(nil)

// Open decrypts this node's envelope from m.Credentials.
func (o *daemonCredentialOpener) Open(_ context.Context, m *clustermig.ClusterMigration) ([]byte, error) {
	if o.receiver == nil || m == nil || len(m.Credentials) == 0 {
		return nil, nil
	}
	blob, ok := m.Credentials[o.selfNode]
	if !ok || len(blob) == 0 {
		return nil, nil
	}
	var env keydelivery.Envelope
	if err := json.Unmarshal(blob, &env); err != nil {
		return nil, err
	}
	return o.receiver.Open(&env)
}

// daemonLocalRecycler implements clustermig.LocalRecycler by delegating to the
// local scoped encryption migrator — the exact in-process recycle the transports
// would have run directly in a single-node deployment. It maps the replicated
// migration record back to a BarrierScope + MigrationOptions, threading the
// opened per-node credential through.
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
	creds, err := seal.CredentialsFor(credential)
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
// a read-only view of the plaintext, valid only for the call; fn must not
// retain or write through it (the pages are frozen). A credential with no
// secret yields fn(nil). The opened enclave is destroyed on return, so the
// plaintext exists only in locked memory and only for the width of fn.
func withCredentialBytes(creds seal.Credentials, fn func([]byte) error) error {
	if creds.Secret == nil {
		return fn(nil)
	}
	opened, err := creds.Secret.Open()
	if err != nil {
		return err
	}
	defer opened.Destroy()
	return opened.WithBytes(fn)
}

// clusterCoordinatorMigrator implements service.EncryptionMigrator for the
// transports: an enable-encryption request becomes a replicated migration intent
// (with per-node sealed credentials) that the per-node reconcilers act on. The
// request returns once the intent is durably written; the operator observes
// per-node/cluster completion via barrier status. It maps the BarrierScope to the
// coordinator's (tenantID, database) — a tenant scope migrates the whole tenant
// barrier (empty database) since all of a tenant's dedicated-raft databases share
// one barrier.
type clusterCoordinatorMigrator struct {
	coord      *clustermig.Coordinator
	barrierReg *seal.BarrierRegistry // dedicated-barrier authority check; may be nil
	modeCheck  barrierModeChecker    // target-strategy entitlement check; may be nil
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
// synchronously — before any credential is sealed or any intent is persisted —
// mirroring the scoped local migrator's gates (fail-fast + defense-in-depth).
func (m *clusterCoordinatorMigrator) EnableEncryption(ctx context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	if err := service.CheckSystemMigrationTarget(scope, opts); err != nil {
		return err
	}
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
	// The plaintext unlock credential (passphrase / Shamir secret) is sealed per
	// owner by the coordinator's CredentialSealer and never persisted in clear.
	// It is opened out of protected memory only for the Request call itself.
	return withCredentialBytes(opts.Credentials, func(credential []byte) error {
		_, err := m.coord.Request(ctx, tenantID, "", opts, credential)
		return err
	})
}
