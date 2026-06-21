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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// LocalRecycler performs this node's in-process per-node recycle for one
// migration scope: quiesce the local data plane, re-encrypt the at-rest stores
// into the keyed barrier minted under credential, and adopt the keyed root key
// (the proven single-node mechanism). credential is this node's plaintext unlock
// secret (passphrase / Shamir U), opened from the record's per-node sealed entry
// by the CredentialOpener; it is empty when no credential was sealed. It returns
// nil only when the local stores are fully keyed and readable. Because every node
// holds per-node keys behind a logical snapshot boundary, each node's recycle is
// independent — there is no shared cluster key to coordinate, so the Reconciler
// can drive nodes in any order (rolling).
type LocalRecycler interface {
	Recycle(ctx context.Context, m *ClusterMigration, credential []byte) error
}

// CredentialOpener opens this node's per-node sealed unlock credential from a
// migration record (the inverse of CredentialSealer), returning the plaintext
// credential or (nil, nil) when none was sealed for this node. It is the seam
// through which the daemon injects this node's private key + crypto, keeping
// clustermig free of key material. The returned plaintext must be wiped by the
// caller after use.
type CredentialOpener interface {
	Open(ctx context.Context, m *ClusterMigration) ([]byte, error)
}

// Reconciler runs on every node. Each pass it lists the active intent records,
// and for any scope where this node is a named owner that has not yet acked the
// current generation, it runs the LocalRecycler once and writes back its
// NodeMigrationStatus. The generation guard makes a pass idempotent: a node that
// already acked the current generation is skipped, so repeated passes (poll or
// watch-driven) never re-recycle. A recycle failure is recorded as seal.PhaseFailed
// and retried on the next pass — the cluster never declares the scope complete
// until every owner reaches seal.PhaseComplete.
type Reconciler struct {
	store    *Store
	nodeID   string
	recycler LocalRecycler
	opener   CredentialOpener
	nowUnix  func() int64
	logger   *slog.Logger
}

// NewReconciler constructs a Reconciler for nodeID. opener opens this node's
// sealed unlock credential from each migration record; pass nil for deployments
// that supply credentials out of band (the recycler then receives an empty
// credential). logger may be nil (a no-op logger is used). nowUnix stamps status
// updates; pass nil to omit timestamps.
func NewReconciler(store *Store, nodeID string, recycler LocalRecycler, opener CredentialOpener, nowUnix func() int64, logger *slog.Logger) *Reconciler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Reconciler{store: store, nodeID: nodeID, recycler: recycler, opener: opener, nowUnix: nowUnix, logger: logger}
}

// ReconcileOnce runs a single reconciliation pass over all active migrations.
// It returns the number of scopes this node recycled in this pass (0 when there
// is nothing owed). A per-scope recycle failure is logged and recorded but does
// not abort the pass — other scopes still progress.
func (r *Reconciler) ReconcileOnce(ctx context.Context) (recycled int, err error) {
	migrations, err := r.store.ListMigrations(ctx)
	if err != nil {
		return 0, err
	}
	for _, m := range migrations {
		owed, oerr := r.owedByThisNode(ctx, m)
		if oerr != nil {
			return recycled, oerr
		}
		if !owed {
			continue
		}
		if rerr := r.recycleScope(ctx, m); rerr != nil {
			// Record the failure (retried next pass) but keep going.
			r.logger.Error("local encryption recycle failed; will retry",
				slog.String("scope", scopeLabel(m.TenantID, m.Database)),
				slog.Uint64("generation", m.Generation),
				slog.Any("error", rerr))
			continue
		}
		recycled++
	}
	return recycled, nil
}

// owedByThisNode reports whether this node is a named owner of m that has not yet
// acked m's current generation with a terminal-success phase.
func (r *Reconciler) owedByThisNode(ctx context.Context, m *ClusterMigration) (bool, error) {
	isOwner := false
	for _, o := range m.Owners {
		if o == r.nodeID {
			isOwner = true
			break
		}
	}
	if !isOwner {
		return false, nil
	}
	st, err := r.store.GetNodeStatus(ctx, m.TenantID, m.Database, r.nodeID)
	if err != nil {
		return false, err
	}
	// Owed unless we already completed THIS generation. A status for an older
	// generation, a non-terminal phase, or no status at all all mean owed.
	if st != nil && st.Generation == m.Generation && st.Phase == seal.PhaseComplete {
		return false, nil
	}
	return true, nil
}

// recycleScope runs the local recycle for one scope, recording phase transitions
// so observers see progress and a crash leaves a non-terminal status that the
// next pass retries.
func (r *Reconciler) recycleScope(ctx context.Context, m *ClusterMigration) error {
	r.logger.Info("starting local encryption recycle",
		slog.String("scope", scopeLabel(m.TenantID, m.Database)),
		slog.String("target", string(m.Target)),
		slog.Uint64("generation", m.Generation))

	// Open this node's sealed unlock credential (if any) for the recycle. The
	// plaintext is wiped after the recycle returns so it never lingers.
	var credential []byte
	if r.opener != nil {
		cred, oerr := r.opener.Open(ctx, m)
		if oerr != nil {
			r.putStatus(ctx, m, seal.PhaseFailed, "open credential: "+oerr.Error())
			return oerr
		}
		credential = cred
	}
	defer wipe(credential)

	r.putStatus(ctx, m, seal.PhaseCopying, "")
	if err := r.recycler.Recycle(ctx, m, credential); err != nil {
		r.putStatus(ctx, m, seal.PhaseFailed, err.Error())
		return err
	}
	r.putStatus(ctx, m, seal.PhaseComplete, "")
	r.logger.Info("local encryption recycle complete",
		slog.String("scope", scopeLabel(m.TenantID, m.Database)),
		slog.Uint64("generation", m.Generation))
	return nil
}

// wipe zeroes a plaintext credential buffer so it does not linger in memory
// after the recycle consumes it. It uses the compiler-elision-resistant
// primitive: a plain zero loop on a buffer never read afterward can be optimized
// away, leaving the secret resident in the heap.
func wipe(b []byte) { secure.Wipe(b) }

// putStatus writes this node's status for the scope, swallowing (logging) write
// errors so a transient KV failure does not abort the recycle outcome — the next
// pass re-derives owed-ness from the persisted status.
func (r *Reconciler) putStatus(ctx context.Context, m *ClusterMigration, phase seal.MigrationPhase, detail string) {
	st := &NodeMigrationStatus{
		NodeID:     r.nodeID,
		Generation: m.Generation,
		Phase:      phase,
		Detail:     detail,
	}
	if r.nowUnix != nil {
		st.UpdatedAtUnix = r.nowUnix()
	}
	if err := r.store.PutNodeStatus(ctx, m.TenantID, m.Database, st); err != nil {
		r.logger.Error("failed to record node migration status",
			slog.String("scope", scopeLabel(m.TenantID, m.Database)),
			slog.String("phase", string(phase)), slog.Any("error", err))
	}
}
