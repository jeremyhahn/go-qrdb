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
				_, err = r.cat.GrantMoveStep(ctx, m.TenantID)
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

// RunLocalSteps runs the node pass for nodeID: for every journalled move
// whose granted step is held by nodeID, in tenant order, it builds the
// MoveStep (the phase from the move's state, Copied for an abort), runs it
// through the MoveStepRunner and settles it with the phase's Complete or
// Fail transition. A step already granted to nodeID when the node starts
// is run again, which is how a node resumes after a crash. A step held by
// another node is skipped.
//
// A step's failure is recorded in the journal, not returned. A catalog
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
	fail     func(ctx context.Context, tenantID, nodeID, reason string) (*Move, error)
}

// copyPhase settles a copy step; a failure counts against maxAttempts.
func (r *MoveReconciler) copyPhase() moveStepPhase {
	return moveStepPhase{
		name:     MoveStepPhaseCopy,
		complete: r.cat.CompleteMoveStep,
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
		fail:     r.cat.FailAbortStep,
	}
}

// runStep runs the step m holds in phase p and settles it.
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
		// A settling abort step is always a copied node's: an uncopied
		// node's abort releases its step at completion.
		step.Copied = m.StepSettling || contains(m.NodesCopied, m.StepNode)
	}
	if m.StepSettling {
		// The step is recorded but its settle has not succeeded yet:
		// settle again, never redo the step.
		return r.settleStep(ctx, step, true)
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
	settling := false
	if next, err := p.complete(ctx, m.TenantID, step.NodeID); err != nil {
		if !r.copyRecorded(ctx, m.TenantID, step) {
			// The journal does not name the destination: it stays closed
			// and the next tick redoes the copy from the source.
			return err
		}
		// The write landed though its reply failed: the journal names the
		// destination and holds the step settling, so settle on it.
		settling = true
	} else {
		settling = next != nil && next.StepSettling && next.StepNode == step.NodeID
		r.recordTransition(ctx, MoveTransitionComplete, m.TenantID, step.NodeID, step.Phase)
	}
	return r.settleStep(ctx, step, settling)
}

// settleStep runs SettleMoveStep for step and, when the journal holds the
// step settling (ack), releases it with AckMoveStep only after the settle
// succeeded. A failed settle leaves the step settling: the holder's next
// RunLocalSteps pass settles it again, and meanwhile no other node is
// granted a step, nothing cuts over and no abort releases.
func (r *MoveReconciler) settleStep(ctx context.Context, step MoveStep, ack bool) error {
	if settler, ok := r.runner.(MoveStepSettler); ok {
		if err := settler.SettleMoveStep(ctx, step); err != nil {
			r.logger.Warn("volume: settling the move step after its journal write failed",
				slog.String("tenant", step.TenantID), slog.String("node", step.NodeID),
				slog.String("phase", step.Phase), slog.String("error", err.Error()))
			return err
		}
	}
	if !ack {
		return nil
	}
	_, err := r.cat.AckMoveStep(ctx, step.TenantID, step.NodeID)
	return err
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
