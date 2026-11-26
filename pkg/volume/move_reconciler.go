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
	"errors"
	"io/fs"
	"log/slog"
	"syscall"
)

// MoveReconciler advances every tenant move's journal one node step at a
// time (decisions Q1 B and Q6 A, docs/decisions/volume-pool-tenant-moves.md)
// in two passes: ReconcileOnce, on the system shard's leader, grants steps
// and cuts over; RunLocalSteps, on every node, runs the step granted to that
// node through the MoveStepRunner and settles it.
type MoveReconciler struct {
	cat         *Catalog
	runner      MoveStepRunner
	maxAttempts int
	logger      *slog.Logger
	transitions MoveTransitionRecorder
	nodes       MoveNodeSource
}

// MoveNodeSource lists the move node IDs of a tenant's current replicas.
// The reconciler resyncs a copying move's node set from it before each
// grant and before cut over, so a replica added or removed after admit is
// copied or dropped (Catalog.SyncMoveNodes).
type MoveNodeSource interface {
	MoveReplicaNodes(ctx context.Context, tenantID string) ([]string, error)
}

// WithMoveNodeSource wires the replica-set source a copying move resyncs
// from; nil keeps the node set fixed at admit.
func WithMoveNodeSource(src MoveNodeSource) MoveReconcilerOption {
	return func(r *MoveReconciler) {
		r.nodes = src
	}
}

// MoveReconcilerOption configures a MoveReconciler.
type MoveReconcilerOption func(*MoveReconciler)

// WithMoveStepAttempts overrides DefaultMoveStepAttempts. A non-positive
// value is ignored.
func WithMoveStepAttempts(n int) MoveReconcilerOption {
	return func(r *MoveReconciler) {
		if n > 0 {
			r.maxAttempts = n
		}
	}
}

// WithMoveReconcilerLogger sets the reconciler's logger. A nil logger is
// ignored.
func WithMoveReconcilerLogger(l *slog.Logger) MoveReconcilerOption {
	return func(r *MoveReconciler) {
		if l != nil {
			r.logger = l
		}
	}
}

// NewMoveReconciler returns a reconciler over cat's move journals that runs
// node steps through runner. A nil cat or runner returns
// *InvalidCatalogDependencyError.
func NewMoveReconciler(cat *Catalog, runner MoveStepRunner, opts ...MoveReconcilerOption) (*MoveReconciler, error) {
	if cat == nil {
		return nil, &InvalidCatalogDependencyError{Reason: "nil Catalog"}
	}
	if runner == nil {
		return nil, &InvalidCatalogDependencyError{Reason: "nil MoveStepRunner"}
	}
	r := &MoveReconciler{
		cat:         cat,
		runner:      runner,
		maxAttempts: DefaultMoveStepAttempts,
		logger:      cat.logger,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// ReconcileOnce runs the leader pass over every move journal, in tenant
// order, and advances each move by one transition. It never runs a node
// step; the node that holds a granted step runs it in RunLocalSteps:
//   - admitted or copying, no step held: Catalog.GrantMoveStep grants the
//     copy step to the next pending node;
//   - copied: Catalog.CutOverMove, only when a MoveCutoverRecorder is set;
//     without one the move stays copied and the pass logs once that cut
//     over waits for the tenant recorder;
//   - cutover, tenant record not yet moved: the recorder, then
//     Catalog.MarkMoveTenantRecorded; a recorder error is logged and
//     returned, and the move stays cut over for the next pass;
//   - cutover, tenant recorded, no step held: Catalog.GrantRetireStep;
//   - aborting, no step held: Catalog.GrantAbortStep.
//
// A catalog error for one tenant is collected and the pass goes on; the
// joined errors are returned. A cancelled ctx stops the pass with
// ctx.Err(). It runs on the system shard's leader only (decision Q6 A); a
// new leader resumes from the journal, since every grant is in it.
func (r *MoveReconciler) ReconcileOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	moves, err := r.cat.scanMoves(ctx)
	if err != nil {
		return err
	}
	var errs []error
	waitLogged := false
	for _, m := range moves {
		if err := ctx.Err(); err != nil {
			return err
		}
		recorder := r.cat.currentMoveCutoverRecorder()
		var err error
		switch m.State {
		case MoveStateAdmitted, MoveStateCopying:
			if m.StepNode == "" {
				err = r.resyncThenGrant(ctx, &m)
			}
		case MoveStateCopied, MoveStateCutover:
			if recorder == nil && !m.TenantRecorded {
				if !waitLogged {
					r.logger.Info("volume: move cut over waits for the tenant recorder")
					waitLogged = true
				}
				continue
			}
			switch {
			case m.State == MoveStateCopied:
				// No cut over in a pass whose resync changed the journal: a
				// replica that joined is copied first, by a later grant.
				var changed bool
				if _, changed, err = r.resync(ctx, &m); err != nil || changed {
					break
				}
				_, err = r.cat.CutOverMove(ctx, m.TenantID)
				if err == nil {
					r.recordTransition(ctx, MoveTransitionCutover, m.TenantID, "", "")
				}
			case m.TenantRecorded:
				if m.StepNode == "" {
					_, err = r.cat.GrantRetireStep(ctx, m.TenantID)
				}
			default:
				err = r.recordTenant(ctx, recorder, m)
			}
		case MoveStateAborting:
			if m.StepNode == "" {
				_, err = r.cat.GrantAbortStep(ctx, m.TenantID)
			}
		default:
			continue
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// resync brings m's node set up to the tenant's current replica set
// through the wired MoveNodeSource (Catalog.SyncMoveNodes). With no source
// wired the journal is left as admitted.
func (r *MoveReconciler) resync(ctx context.Context, m *Move) (*Move, bool, error) {
	if r.nodes == nil {
		return m, false, nil
	}
	nodes, err := r.nodes.MoveReplicaNodes(ctx, m.TenantID)
	if err != nil {
		return nil, false, err
	}
	return r.cat.SyncMoveNodes(ctx, m.TenantID, nodes)
}

// resyncThenGrant resyncs m's node set and grants the next copy step when
// the synced move still has one to grant.
func (r *MoveReconciler) resyncThenGrant(ctx context.Context, m *Move) error {
	synced, _, err := r.resync(ctx, m)
	if err != nil {
		return err
	}
	if synced.StepNode != "" || (synced.State != MoveStateAdmitted && synced.State != MoveStateCopying) {
		return nil
	}
	_, err = r.cat.GrantMoveStep(ctx, m.TenantID)
	return err
}

// RunLocalSteps runs the node pass for nodeID: for every journalled move
// whose granted step is held by nodeID, in tenant order, it builds the
// MoveStep (the phase from the move's state, Copied for an abort), runs it
// through the MoveStepRunner and settles it with the phase's Complete or
// Fail transition. A step already granted to nodeID when the node starts
// is run again, which is how a node resumes after a crash. A step held by
// another node is skipped.
//
// A step's failure is recorded in the journal, not returned. A copy step
// that cannot run because the node is sealed or its destination pool is
// unavailable waits: the journal records the reason and counts no attempt,
// and the next pass runs the step again (Catalog.WaitMoveStep). A catalog
// error for one tenant is collected and the pass goes on; the joined
// errors are returned. A cancelled ctx stops the pass with ctx.Err().
func (r *MoveReconciler) RunLocalSteps(ctx context.Context, nodeID string) error {
	if nodeID == "" {
		return &InvalidMoveRequestError{Reason: "empty node id"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	moves, err := r.cat.scanMoves(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range moves {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.StepNode != nodeID {
			continue
		}
		var p moveStepPhase
		switch m.State {
		case MoveStateCopying:
			p = r.copyPhase()
		case MoveStateCutover:
			p = r.retirePhase()
		case MoveStateAborting:
			p = r.abortPhase()
		default:
			continue
		}
		if err := r.runStep(ctx, m, p); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// recordTenant makes the tenant record name m.ToPool through recorder, then
// marks the journal.
func (r *MoveReconciler) recordTenant(ctx context.Context, recorder MoveCutoverRecorder, m Move) error {
	if err := recorder.RecordTenantPoolMoved(ctx, m.TenantID, m.FromPool, m.ToPool); err != nil {
		r.logger.Warn("volume: recording the tenant's moved pool failed",
			slog.String("tenant", m.TenantID), slog.String("error", err.Error()))
		return err
	}
	_, err := r.cat.MarkMoveTenantRecorded(ctx, m.TenantID)
	return err
}

// moveStepPhase is one phase's settling catalog transitions.
type moveStepPhase struct {
	name     string
	complete func(ctx context.Context, tenantID, nodeID string) (*Move, error)
	// record, when set, records the step but keeps it granted while the
	// node settles it (RecordMoveStep, RecordAbortStep); the reconciler
	// uses it when its runner is a MoveStepSettler, then ReleaseMoveStep.
	record func(ctx context.Context, tenantID, nodeID string) (*Move, error)
	fail   func(ctx context.Context, tenantID, nodeID, reason string) (*Move, error)
}

// copyPhase settles a copy step; a failure counts against maxAttempts.
func (r *MoveReconciler) copyPhase() moveStepPhase {
	return moveStepPhase{
		name:     MoveStepPhaseCopy,
		complete: r.cat.CompleteMoveStep,
		record:   r.cat.RecordMoveStep,
		fail: func(ctx context.Context, tenantID, nodeID, reason string) (*Move, error) {
			return r.cat.FailMoveStep(ctx, tenantID, nodeID, reason, r.maxAttempts)
		},
	}
}

// retirePhase settles a retire step.
func (r *MoveReconciler) retirePhase() moveStepPhase {
	return moveStepPhase{
		name:     MoveStepPhaseRetire,
		complete: r.cat.CompleteRetireStep,
		fail:     r.cat.FailRetireStep,
	}
}

// abortPhase settles an abort step.
func (r *MoveReconciler) abortPhase() moveStepPhase {
	return moveStepPhase{
		name:     MoveStepPhaseAbort,
		complete: r.cat.CompleteAbortStep,
		record:   r.cat.RecordAbortStep,
		fail:     r.cat.FailAbortStep,
	}
}

// runStep runs the step m holds in phase p and settles it. A copy step the
// runner answers with a waiting error (moveStepWaits) is recorded with
// Catalog.WaitMoveStep: no attempt counted, no fail transition.
func (r *MoveReconciler) runStep(ctx context.Context, m Move, p moveStepPhase) error {
	step := MoveStep{
		TenantID: m.TenantID,
		NodeID:   m.StepNode,
		FromPool: m.FromPool,
		ToPool:   m.ToPool,
		Attempt:  m.StepAttempts + 1,
		Phase:    p.name,
	}
	if p.name == MoveStepPhaseAbort {
		step.Copied = contains(m.NodesCopied, m.StepNode)
	}
	settler, _ := r.runner.(MoveStepSettler)
	if m.StepSettling {
		// The step is recorded but its settle has not been acknowledged
		// (it failed, or the node stopped before it): settle again.
		if p.name == MoveStepPhaseAbort {
			step.Copied = m.StepCopied
		}
		return r.settleAndRelease(ctx, settler, step)
	}
	if p.name == MoveStepPhaseCopy && m.CancelRequested {
		// Cancelled before this node started its copy: the source stays
		// authoritative, so the move aborts with the node uncopied.
		if _, err := p.fail(ctx, m.TenantID, step.NodeID, "cancelled by operator"); err != nil {
			return err
		}
		r.recordTransition(ctx, MoveTransitionFail, m.TenantID, step.NodeID, step.Phase)
		return nil
	}
	if runErr := r.runner.RunMoveStep(ctx, step); runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if p.name == MoveStepPhaseCopy && moveStepWaits(runErr) {
			// The node is sealed or its destination pool unavailable: the
			// step waits for the unseal, counts no attempt and records no
			// fail transition.
			r.logger.Info("volume: move step waiting",
				slog.String("tenant", step.TenantID), slog.String("node", step.NodeID),
				slog.String("phase", step.Phase), slog.String("error", runErr.Error()))
			if _, err := r.cat.WaitMoveStep(ctx, m.TenantID, step.NodeID, waitingMoveStepReason(step.Phase, runErr)); err != nil {
				return err
			}
			return nil
		}
		r.logger.Warn("volume: move step failed",
			slog.String("tenant", step.TenantID), slog.String("node", step.NodeID),
			slog.String("phase", step.Phase), slog.Int("attempt", step.Attempt),
			slog.String("error", runErr.Error()))
		if _, err := p.fail(ctx, m.TenantID, step.NodeID, sanitizeMoveStepError(step.Phase, runErr)); err != nil {
			return err
		}
		r.recordTransition(ctx, MoveTransitionFail, m.TenantID, step.NodeID, step.Phase)
		return nil
	}
	if settler != nil && p.record != nil {
		// Record the step but keep it until the node has settled it: the
		// leader grants no other step meanwhile, and a settle that fails
		// is retried from the journal on the next pass.
		if _, err := p.record(ctx, m.TenantID, step.NodeID); err != nil {
			if !r.stepRecorded(ctx, m.TenantID, step.NodeID) {
				return err
			}
		} else {
			r.recordTransition(ctx, MoveTransitionComplete, m.TenantID, step.NodeID, step.Phase)
		}
		return r.settleAndRelease(ctx, settler, step)
	}
	if _, err := p.complete(ctx, m.TenantID, step.NodeID); err != nil {
		if !r.copyRecorded(ctx, m.TenantID, step) {
			// The journal does not name the destination: it stays closed
			// and the next tick redoes the copy from the source.
			return err
		}
		// The write landed though its reply failed: the journal names the
		// destination, so settle on it.
	} else {
		r.recordTransition(ctx, MoveTransitionComplete, m.TenantID, step.NodeID, step.Phase)
	}
	if settler != nil {
		if err := settler.SettleMoveStep(ctx, step); err != nil {
			r.logger.Warn("volume: settling the move step after its journal write failed",
				slog.String("tenant", step.TenantID), slog.String("node", step.NodeID),
				slog.String("phase", step.Phase), slog.String("error", err.Error()))
			return err
		}
	}
	return nil
}

// settleAndRelease settles a recorded copy or abort step on this node and
// then releases it in the journal. A settle that fails leaves the step
// granted and settling, so the node's next pass retries it and no other
// node is granted a step meanwhile.
func (r *MoveReconciler) settleAndRelease(ctx context.Context, settler MoveStepSettler, step MoveStep) error {
	if settler != nil {
		if err := settler.SettleMoveStep(ctx, step); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			var sealed *TenantSealedError
			if errors.As(err, &sealed) {
				// The replica's tenant reopened sealed: it is not serving
				// and has not caught up, so the step stays settling on it,
				// uncounted, until the administrator's unseal lets the next
				// settle attach it and await its catch-up.
				reason := waitingMoveStepReason(step.Phase, err)
				if _, waitErr := r.cat.WaitSettleStep(ctx, step.TenantID, step.NodeID, reason); waitErr != nil {
					return errors.Join(err, waitErr)
				}
				r.logger.Info("volume: move step settle waits for the tenant's unseal",
					slog.String("tenant", step.TenantID), slog.String("node", step.NodeID),
					slog.String("phase", step.Phase), slog.String("reason", reason))
				return nil
			}
			r.logger.Warn("volume: settling the move step failed; it stays granted and is retried",
				slog.String("tenant", step.TenantID), slog.String("node", step.NodeID),
				slog.String("phase", step.Phase), slog.Int("attempt", step.Attempt),
				slog.String("error", err.Error()))
			// Count the failure in the journal, so a settle that never
			// finishes is bounded: a copy settle aborts past maxAttempts
			// or once a cancel is recorded.
			m, failErr := r.cat.FailSettleStep(ctx, step.TenantID, step.NodeID,
				sanitizeMoveStepError(step.Phase, err), r.maxAttempts)
			if failErr != nil {
				return errors.Join(err, failErr)
			}
			if m != nil && m.StepNode != step.NodeID {
				r.recordTransition(ctx, MoveTransitionFail, step.TenantID, step.NodeID, step.Phase)
			}
			return err
		}
	}
	_, err := r.cat.ReleaseMoveStep(ctx, step.TenantID, step.NodeID)
	return err
}

// stepRecorded reports whether the journal holds nodeID's step as recorded
// and settling: a record whose write landed though its reply failed.
func (r *MoveReconciler) stepRecorded(ctx context.Context, tenantID, nodeID string) bool {
	cur, _, err := r.cat.readMove(ctx, tenantID)
	return err == nil && cur != nil && cur.StepSettling && cur.StepNode == nodeID
}

// copyRecorded reports whether the journal records step's node copied
// after a CompleteMoveStep whose reply failed: a copy step whose write
// landed must still settle, or the node would keep its stores closed.
func (r *MoveReconciler) copyRecorded(ctx context.Context, tenantID string, step MoveStep) bool {
	if step.Phase != MoveStepPhaseCopy {
		return false
	}
	cur, _, err := r.cat.readMove(ctx, tenantID)
	return err == nil && cur != nil && contains(cur.NodesCopied, step.NodeID)
}

// moveStepWaits reports whether err means the step cannot run yet rather
// than that it failed: the node is sealed (*NodeSealedError) or its
// destination pool is locked or not open (*PoolUnavailableError), through
// any wrapping, *MoveStepError included. Such a step waits for the unseal.
func moveStepWaits(err error) bool {
	var (
		sealed      *NodeSealedError
		unavailable *PoolUnavailableError
	)
	return errors.As(err, &sealed) || errors.As(err, &unavailable)
}

// waitingMoveStepReason is the journal's Error for a waiting step:
// "move copy step waiting: node sealed", with the same fixed classes as
// sanitizeMoveStepError and no paths or node detail.
func waitingMoveStepReason(phase string, err error) string {
	return "move " + phase + " step waiting: " + moveStepErrorClass(err)
}

// sanitizeMoveStepError returns the message a failed step persists in the
// replicated journal: the step phase and a typed class with a fixed message.
// The raw error, which may carry node-local mount paths, is only logged on
// the node that ran the step (docs/SPEC.md: no data-root paths leave a node).
func sanitizeMoveStepError(phase string, err error) string {
	return "move " + phase + " step failed: " + moveStepErrorClass(err)
}

// moveStepErrorClass maps err to a fixed, path-free class.
func moveStepErrorClass(err error) string {
	var (
		mismatch    *ManifestMismatchError
		unavailable *PoolUnavailableError
		sealed      *NodeSealedError
		tenant      *TenantSealedError
		closed      *ClosedVolumeError
		open        *OpenError
		notDir      *notADirectoryError
		unsupported *unsupportedEntryError
	)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.As(err, &mismatch):
		return "manifest mismatch"
	case errors.As(err, &unavailable):
		return "pool unavailable"
	case errors.As(err, &sealed):
		return "node sealed"
	case errors.As(err, &tenant):
		return "tenant sealed"
	case errors.As(err, &closed):
		return "volume closed"
	case errors.As(err, &open):
		return "volume open failed"
	case errors.As(err, &notDir):
		return "path is not a directory"
	case errors.As(err, &unsupported):
		return "unsupported directory entry"
	case errors.Is(err, syscall.ENOSPC):
		return "no space left on the pool"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "path does not exist"
	case errors.Is(err, fs.ErrExist):
		return "path already exists"
	default:
		return "internal error (see the node log)"
	}
}

// MoveStepSettler is the optional half of a MoveStepRunner that runs after
// the step's Complete transition is in the journal: the work that must not
// precede the CAS, such as reopening from the source after an abort's
// copy-back. It is not called after a Fail. A node that crashes between
// the CAS and the settle does that work at boot from the journal.
type MoveStepSettler interface {
	SettleMoveStep(ctx context.Context, step MoveStep) error
}

// Move transitions a MoveTransitionRecorder is told of.
const (
	MoveTransitionComplete = "complete"
	MoveTransitionFail     = "fail"
	MoveTransitionCutover  = "cutover"
	MoveTransitionCancel   = "cancel"
	// MoveTransitionCancelRefused is an operator's cancel that CancelMove
	// refused because the move is past its commit point.
	MoveTransitionCancelRefused = "cancel-refused"
)

// MoveTransitionRecorder records a tenant move's journal transition (the
// volume_pool.move_<transition> audit actions). nodeID and phase are empty
// for a cut over or a cancel. A recorder failure is logged, never returned:
// the journal transition has already been written.
type MoveTransitionRecorder interface {
	RecordMoveTransition(ctx context.Context, transition, tenantID, nodeID, phase string) error
}

// WithMoveTransitionRecorder sets the recorder told of each Complete, Fail
// and CutOver transition the reconciler writes. A nil recorder is ignored.
func WithMoveTransitionRecorder(rec MoveTransitionRecorder) MoveReconcilerOption {
	return func(r *MoveReconciler) {
		if rec != nil {
			r.transitions = rec
		}
	}
}

// recordTransition tells the transition recorder, when set, of one
// transition.
func (r *MoveReconciler) recordTransition(ctx context.Context, transition, tenantID, nodeID, phase string) {
	if r.transitions == nil {
		return
	}
	if err := r.transitions.RecordMoveTransition(ctx, transition, tenantID, nodeID, phase); err != nil {
		r.logger.Warn("volume: recording the move transition failed",
			slog.String("tenant", tenantID), slog.String("transition", transition),
			slog.String("error", err.Error()))
	}
}
