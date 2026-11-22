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

// ReconcileOnce runs one pass over every move journal, in tenant order. For
// each move admitted or copying it runs exactly one node step: it grants the
// step (a step already granted is re-run, which is how a new leader resumes
// after a crash), runs it through the MoveStepRunner, and completes it on
// success or records the failed attempt otherwise; the step's failure is
// recorded in the journal, not returned. Moves copied or aborting are left
// alone (cut over and abort are a later slice). A catalog error for one
// tenant is collected and the pass goes on; the joined errors are returned.
// A cancelled ctx stops the pass with ctx.Err().
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
	for _, m := range moves {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.State != MoveStateAdmitted && m.State != MoveStateCopying {
			continue
		}
		if err := r.step(ctx, m.TenantID); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// step grants, runs and settles one node step of tenantID's move.
func (r *MoveReconciler) step(ctx context.Context, tenantID string) error {
	m, err := r.cat.GrantMoveStep(ctx, tenantID)
	if err != nil {
		return err
	}
	step := MoveStep{
		TenantID: m.TenantID,
		NodeID:   m.StepNode,
		FromPool: m.FromPool,
		ToPool:   m.ToPool,
		Attempt:  m.StepAttempts + 1,
	}
	if runErr := r.runner.RunMoveStep(ctx, step); runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		r.logger.Warn("volume: move step failed",
			slog.String("tenant", step.TenantID), slog.String("node", step.NodeID),
			slog.Int("attempt", step.Attempt), slog.String("error", runErr.Error()))
		_, err := r.cat.FailMoveStep(ctx, tenantID, step.NodeID, runErr.Error(), r.maxAttempts)
		return err
	}
	_, err = r.cat.CompleteMoveStep(ctx, tenantID, step.NodeID)
	return err
}
