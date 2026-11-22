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
// time through a MoveStepRunner (decisions Q1 B and Q6 A,
// docs/decisions/volume-pool-tenant-moves.md).
type MoveReconciler struct {
	cat         *Catalog
	runner      MoveStepRunner
	maxAttempts int
	logger      *slog.Logger
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

// ReconcileOnce runs one pass over every move journal, in tenant order, and
// advances each move by one transition:
//   - admitted or copying: one copy step. It grants the step (a step
//     already granted is re-run, which is how a new leader resumes after a
//     crash), runs it through the MoveStepRunner, and completes it on
//     success or records the failed attempt otherwise;
//   - copied: Catalog.CutOverMove, only when a MoveCutoverRecorder is
//     wired; unwired, the move stays copied and the pass logs once that
//     cut over waits for the tenant recorder;
//   - cutover, tenant record not yet moved: the wired recorder, then
//     Catalog.MarkMoveTenantRecorded; a recorder error is logged and
//     returned, and the move stays cut over for the next pass;
//   - cutover, tenant recorded: one retire step;
//   - aborting: one abort step.
//
// A step's failure is recorded in the journal, not returned. A catalog
// error for one tenant is collected and the pass goes on; the joined errors
// are returned. A cancelled ctx stops the pass with ctx.Err().
//
// It runs on the system shard's leader only (decision Q6); the composition
// that schedules it there is a later slice.
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
			err = r.copyStep(ctx, m.TenantID)
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
			case m.TenantRecorded:
				err = r.retireStep(ctx, m.TenantID)
			default:
				err = r.recordTenant(ctx, recorder, m)
			}
		case MoveStateAborting:
			err = r.abortStep(ctx, m.TenantID)
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

// moveStepPhase is one phase's catalog transitions.
type moveStepPhase struct {
	name     string
	grant    func(ctx context.Context, tenantID string) (*Move, error)
	complete func(ctx context.Context, tenantID, nodeID string) (*Move, error)
	fail     func(ctx context.Context, tenantID, nodeID, reason string) (*Move, error)
}

// copyStep grants, runs and settles one copy step of tenantID's move.
func (r *MoveReconciler) copyStep(ctx context.Context, tenantID string) error {
	return r.runStep(ctx, tenantID, moveStepPhase{
		name:     MoveStepPhaseCopy,
		grant:    r.cat.GrantMoveStep,
		complete: r.cat.CompleteMoveStep,
		fail: func(ctx context.Context, tenantID, nodeID, reason string) (*Move, error) {
			return r.cat.FailMoveStep(ctx, tenantID, nodeID, reason, r.maxAttempts)
		},
	})
}

// retireStep grants, runs and settles one retire step of tenantID's move.
func (r *MoveReconciler) retireStep(ctx context.Context, tenantID string) error {
	return r.runStep(ctx, tenantID, moveStepPhase{
		name:     MoveStepPhaseRetire,
		grant:    r.cat.GrantRetireStep,
		complete: r.cat.CompleteRetireStep,
		fail:     r.cat.FailRetireStep,
	})
}

// abortStep grants, runs and settles one abort step of tenantID's move.
func (r *MoveReconciler) abortStep(ctx context.Context, tenantID string) error {
	return r.runStep(ctx, tenantID, moveStepPhase{
		name:     MoveStepPhaseAbort,
		grant:    r.cat.GrantAbortStep,
		complete: r.cat.CompleteAbortStep,
		fail:     r.cat.FailAbortStep,
	})
}

// runStep grants, runs and settles one node step of tenantID's move in
// phase p.
func (r *MoveReconciler) runStep(ctx context.Context, tenantID string, p moveStepPhase) error {
	m, err := p.grant(ctx, tenantID)
	if err != nil {
		return err
	}
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
		_, err := p.fail(ctx, tenantID, step.NodeID, runErr.Error())
		return err
	}
	_, err = p.complete(ctx, tenantID, step.NodeID)
	return err
}
