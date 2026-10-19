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
	"errors"
	"fmt"
	"log/slog"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// MaxRecycleAttempts bounds how many times the Reconciler retries a failing
// local recycle for one (scope, generation) before giving up and leaving the
// node's status as a terminal, operator-visible failure instead of retrying
// forever on the fixed poll interval. A structurally invalid migration record
// (e.g. an out-of-range Shamir M-of-N, or an unknown mechanism) fails
// deterministically on every attempt, so retrying it is pure noise; bounding
// the count turns "silent infinite retry with no operator signal" into a
// small, fixed number of attempts followed by a durable, inspectable failure
// (see NodeMigrationStatus.Attempts / Detail). It is a var (not const),
// mirroring clusterMigReconcileInterval in cmd/qrdbd/cluster_migrate.go, so
// tests can shrink it; production never reassigns it.
var MaxRecycleAttempts = 5

// LocalRecycler performs this node's in-process per-node recycle for one
// migration scope: quiesce the local data plane, re-encrypt the at-rest stores
// into the keyed barrier minted under credential, and adopt the keyed root key
// (the proven single-node mechanism). credential is this node's own unlock
// secret from its CredentialSource; it is empty for a Shamir target, whose
// node mints its own key and seals a fresh share set to its own officers. It returns
// nil only when the local stores are fully keyed and readable. Because every node
// holds per-node keys behind a logical snapshot boundary, each node's recycle is
// independent — there is no shared cluster key to coordinate, so the Reconciler
// can drive nodes in any order (rolling).
type LocalRecycler interface {
	// AtTarget reports whether this node's barrier for m's scope is already
	// keyed at m.Target: the recycle ran, and only its ack is owed. The
	// Reconciler asks before it takes a credential, so an owner whose ack was
	// lost -- a SYSTEM-scope recycle reboots the data plane, reconciler and
	// in-memory held credential included, before the ack lands -- acks on
	// the next pass instead of waiting for a credential it no longer needs.
	// It reports false when the barrier is unavailable, so the recycle is
	// attempted rather than skipped.
	AtTarget(m *ClusterMigration) bool
	Recycle(ctx context.Context, m *ClusterMigration, credential []byte) error
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
	source   CredentialSource
	nowUnix  func() int64
	logger   *slog.Logger
}

// NewReconciler constructs a Reconciler for nodeID. source supplies this
// node's own credential for a passphrase target; with a nil source the
// recycler receives an empty credential, for a composer whose recycler needs
// none. logger may be nil (a no-op logger is used). nowUnix stamps status
// updates; pass nil to omit timestamps.
func NewReconciler(store *Store, nodeID string, recycler LocalRecycler, source CredentialSource, nowUnix func() int64, logger *slog.Logger) *Reconciler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Reconciler{store: store, nodeID: nodeID, recycler: recycler, source: source, nowUnix: nowUnix, logger: logger}
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
			var awaiting *awaitingCredentialError
			if errors.As(rerr, &awaiting) {
				r.logger.Warn("local encryption recycle awaiting this node's credential",
					slog.String("scope", scopeLabel(m.TenantID, m.Database)),
					slog.Uint64("generation", m.Generation),
					slog.Any("reason", awaiting.Err))
				continue
			}
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

// owedByThisNode reports whether this node is a named owner of m that has not
// yet acked m's current generation with a terminal-success phase, AND has not
// exhausted its retry budget (MaxRecycleAttempts) for a terminal-failure
// phase. An exhausted node is no longer owed — the Reconciler stops scheduling
// it — but the scope stays incomplete forever (Store.IsComplete only counts
// PhaseComplete), leaving the failure durably visible until the operator
// re-issues the request (a new generation resets the budget, see
// recordFailure/currentStatus). In a rolling migration a node is owed only
// on its turn, while its step is StepRecycling: an exhausted node then stalls
// the rollout at its step rather than letting the next owner go down too.
func (r *Reconciler) owedByThisNode(ctx context.Context, m *ClusterMigration) (bool, error) {
	isOwner := false
	for _, o := range m.Owners {
		if o == r.nodeID {
			isOwner = true
			break
		}
	}
	if !isOwner || !m.turnOf(r.nodeID) {
		return false, nil
	}
	st, err := r.store.GetNodeStatus(ctx, m.TenantID, m.Database, r.nodeID)
	if err != nil {
		return false, err
	}
	// A status for an older generation, or no status at all, means owed: a new
	// generation always starts with a fresh retry budget.
	if st == nil || st.Generation != m.Generation {
		return true, nil
	}
	if st.Phase == seal.PhaseComplete {
		return false, nil
	}
	if st.Phase == seal.PhaseFailed && st.Attempts >= MaxRecycleAttempts {
		return false, nil
	}
	return true, nil
}

// permanentRecycleError reports whether err is a structurally deterministic
// migration-shape failure (invalid target/mechanism/availability, or an
// out-of-range Shamir M-of-N) that will fail identically on every retry.
// Recognizing it lets recordFailure abandon the scope on the FIRST failure
// instead of burning MaxRecycleAttempts identical retries before giving up —
// the exact bug this bound exists to close (a structurally invalid intent
// record retried every poll interval forever with no operator signal).
func permanentRecycleError(err error) bool {
	return errors.Is(err, seal.ErrMigrationInvalidTarget) ||
		errors.Is(err, seal.ErrMigrationInvalidMechanism) ||
		errors.Is(err, seal.ErrMigrationInvalidAvailability) ||
		errors.Is(err, seal.ErrShamirThresholdInvalid)
}

// recycleScope runs the local recycle for one scope, recording phase transitions
// so observers see progress and a crash leaves a non-terminal status that the
// next pass retries.
func (r *Reconciler) recycleScope(ctx context.Context, m *ClusterMigration) error {
	r.logger.Info("starting local encryption recycle",
		slog.String("scope", scopeLabel(m.TenantID, m.Database)),
		slog.String("target", string(m.Target)),
		slog.Uint64("generation", m.Generation))

	current := r.currentStatus(ctx, m)
	prevAttempts := 0
	if current != nil {
		prevAttempts = current.Attempts
	}

	// A barrier already at the target owes only its ack: write it without
	// asking for a credential the recycle no longer needs.
	if r.recycler.AtTarget(m) {
		r.putStatus(ctx, m, seal.PhaseComplete, "", 0)
		r.release(m)
		r.logger.Info("local barrier already at target; acked",
			slog.String("scope", scopeLabel(m.TenantID, m.Database)),
			slog.Uint64("generation", m.Generation))
		return nil
	}

	// Take this node's own credential for the recycle; a Shamir target needs
	// none. Without one the owner waits -- awaiting-credential, not a failed
	// attempt -- and is asked again next pass. The plaintext is wiped after the
	// recycle returns so it never lingers.
	var credential []byte
	if r.source != nil && m.Target != seal.StrategyShamir {
		cred, cerr := r.source.Credential(ctx, m)
		if cerr != nil {
			// Written once per reason, not every pass: an owner may wait a
			// long time, and each write is a Raft proposal.
			if current == nil || current.Phase != PhaseAwaitingCredential || current.Detail != cerr.Error() {
				r.putStatus(ctx, m, PhaseAwaitingCredential, cerr.Error(), prevAttempts)
			}
			return &awaitingCredentialError{Err: cerr}
		}
		credential = cred
	}
	defer wipe(credential)

	// Checkpoint with attempts=prevAttempts, NOT 0: this write lands before
	// Recycle runs, so a crash during Recycle leaves it as the last-persisted
	// status. Zeroing the counter here would let a reliably-crashing recycle
	// retry forever across reboots — each restart would read Attempts=0 from
	// this checkpoint and reopen a fresh MaxRecycleAttempts budget, forever
	// resetting the bound recordFailure (and owedByThisNode) rely on to stop
	// retrying. Carrying prevAttempts forward means an attempt interrupted by
	// a crash still counts against the budget once recordFailure runs on the
	// next pass.
	r.putStatus(ctx, m, seal.PhaseCopying, "", prevAttempts)
	if err := r.recycler.Recycle(ctx, m, credential); err != nil {
		if r.recordFailure(ctx, m, prevAttempts, err.Error(), permanentRecycleError(err)) {
			r.release(m)
		}
		return err
	}
	r.putStatus(ctx, m, seal.PhaseComplete, "", 0)
	r.release(m)
	r.logger.Info("local encryption recycle complete",
		slog.String("scope", scopeLabel(m.TenantID, m.Database)),
		slog.Uint64("generation", m.Generation))
	return nil
}

// currentStatus returns this node's recorded status for m's CURRENT
// generation, or nil when there is none yet or the recorded status is for a
// stale generation — a new generation always starts its own retry budget
// (mirrors owedByThisNode's generation handling).
func (r *Reconciler) currentStatus(ctx context.Context, m *ClusterMigration) *NodeMigrationStatus {
	st, err := r.store.GetNodeStatus(ctx, m.TenantID, m.Database, r.nodeID)
	if err != nil || st == nil || st.Generation != m.Generation {
		return nil
	}
	return st
}

// recordFailure writes this node's PhaseFailed status for m, bumping the
// attempt counter from prevAttempts. A permanent (structurally deterministic)
// failure jumps straight to MaxRecycleAttempts on its FIRST occurrence, since
// retrying a validation error that can never succeed is pure noise. Once
// attempts reaches MaxRecycleAttempts, Detail is annotated to say so:
// owedByThisNode then stops scheduling further retries for this generation,
// leaving this record as the durable, operator-visible terminal failure. It
// reports whether the generation was abandoned.
func (r *Reconciler) recordFailure(ctx context.Context, m *ClusterMigration, prevAttempts int, detail string, permanent bool) bool {
	attempts := prevAttempts + 1
	if permanent && attempts < MaxRecycleAttempts {
		attempts = MaxRecycleAttempts
	}
	abandoned := attempts >= MaxRecycleAttempts
	if abandoned {
		detail = fmt.Sprintf("abandoned after %d attempt(s) (retries exhausted): %s", attempts, detail)
	}
	r.putStatus(ctx, m, seal.PhaseFailed, detail, attempts)
	return abandoned
}

// release tells the credential source m's generation no longer needs it.
func (r *Reconciler) release(m *ClusterMigration) {
	if r.source != nil {
		r.source.Release(m)
	}
}

// awaitingCredentialError reports that this node's recycle is waiting for its
// credential; it is not a failed attempt.
type awaitingCredentialError struct{ Err error }

func (e *awaitingCredentialError) Error() string {
	return "clustermig: awaiting this node's credential: " + e.Err.Error()
}
func (e *awaitingCredentialError) Unwrap() error { return e.Err }

// wipe zeroes a plaintext credential buffer so it does not linger in memory
// after the recycle consumes it. It uses the compiler-elision-resistant
// primitive: a plain zero loop on a buffer never read afterward can be optimized
// away, leaving the secret resident in the heap.
func wipe(b []byte) { secure.Wipe(b) }

// putStatus writes this node's status for the scope, swallowing (logging) write
// errors so a transient KV failure does not abort the recycle outcome — the next
// pass re-derives owed-ness from the persisted status.
func (r *Reconciler) putStatus(ctx context.Context, m *ClusterMigration, phase seal.MigrationPhase, detail string, attempts int) {
	st := &NodeMigrationStatus{
		NodeID:     r.nodeID,
		Generation: m.Generation,
		Phase:      phase,
		Detail:     detail,
		Attempts:   attempts,
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
