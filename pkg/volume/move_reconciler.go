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
	"log/slog"
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
		step.Copied = contains(m.NodesCopied, m.StepNode)
	}
	if runErr := r.runner.RunMoveStep(ctx, step); runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.logger.Warn("volume: move step failed",
			slog.String("tenant", step.TenantID), slog.String("node", step.NodeID),
			slog.String("phase", step.Phase), slog.Int("attempt", step.Attempt),
			slog.String("error", runErr.Error()))
		if _, err := p.fail(ctx, m.TenantID, step.NodeID, runErr.Error()); err != nil {
			return err
		}
		r.recordTransition(ctx, MoveTransitionFail, m.TenantID, step.NodeID, step.Phase)
		return nil
	}
	if _, err := p.complete(ctx, m.TenantID, step.NodeID); err != nil {
		return err
	}
	r.recordTransition(ctx, MoveTransitionComplete, m.TenantID, step.NodeID, step.Phase)
	return nil
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
