// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// scriptedRunner is a MoveStepRunner double: it records every step and
// answers attempt k of a node's step with fail[node][k-1] (nil past the
// script's end).
type scriptedRunner struct {
	fail  map[string][]error
	steps []MoveStep
}

func (r *scriptedRunner) RunMoveStep(_ context.Context, step MoveStep) error {
	r.steps = append(r.steps, step)
	script := r.fail[step.NodeID]
	if step.Attempt-1 < len(script) {
		return script[step.Attempt-1]
	}
	return nil
}

func newTestReconciler(t *testing.T, cat *Catalog, runner MoveStepRunner, opts ...MoveReconcilerOption) *MoveReconciler {
	t.Helper()
	r, err := NewMoveReconciler(cat, runner, opts...)
	if err != nil {
		t.Fatalf("NewMoveReconciler: %v", err)
	}
	return r
}

func TestMoveReconciler_TwoPassesCopyBothNodes(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner)
	ctx := context.Background()
	for pass := 0; pass < 2; pass++ {
		if err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("ReconcileOnce pass %d: %v", pass, err)
		}
	}
	want := []MoveStep{
		{TenantID: "acme", NodeID: "n1", FromPool: "fast", ToPool: "bulk", Attempt: 1},
		{TenantID: "acme", NodeID: "n2", FromPool: "fast", ToPool: "bulk", Attempt: 1},
	}
	if !reflect.DeepEqual(runner.steps, want) {
		t.Fatalf("steps = %+v, want %+v", runner.steps, want)
	}
	if m := storedMove(t, kv, "acme"); m == nil || m.State != MoveStateCopied {
		t.Fatalf("journal = %+v, want copied", m)
	}
	// A copied move is left alone.
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce on copied: %v", err)
	}
	if len(runner.steps) != 2 {
		t.Fatalf("steps after a pass over a copied move = %d, want 2", len(runner.steps))
	}
}

func TestMoveReconciler_AbortsAfterBoundedAttempts(t *testing.T) {
	boom := errors.New("copy failed")
	for name, tc := range map[string]struct {
		opts []MoveReconcilerOption
		runs int
	}{
		"default":  {runs: DefaultMoveStepAttempts},
		"two":      {opts: []MoveReconcilerOption{WithMoveStepAttempts(2)}, runs: 2},
		"ignored0": {opts: []MoveReconcilerOption{WithMoveStepAttempts(0)}, runs: DefaultMoveStepAttempts},
	} {
		t.Run(name, func(t *testing.T) {
			cat, kv, _ := newAdmittedMove(t)
			runner := &scriptedRunner{fail: map[string][]error{"n1": {boom, boom, boom, boom}}}
			r := newTestReconciler(t, cat, runner, tc.opts...)
			for pass := 0; pass < 5; pass++ {
				if err := r.ReconcileOnce(context.Background()); err != nil {
					t.Fatalf("ReconcileOnce pass %d: %v", pass, err)
				}
			}
			if len(runner.steps) != tc.runs {
				t.Fatalf("runs = %d, want %d", len(runner.steps), tc.runs)
			}
			for i, s := range runner.steps {
				if s.NodeID != "n1" || s.Attempt != i+1 {
					t.Fatalf("step %d = %+v, want n1 attempt %d", i, s, i+1)
				}
			}
			m := storedMove(t, kv, "acme")
			if m == nil || m.State != MoveStateAborting || m.Error != boom.Error() ||
				!reflect.DeepEqual(m.NodesPending, []string{"n1", "n2"}) {
				t.Fatalf("journal = %+v, want aborting with n1 pending", m)
			}
		})
	}
}

func TestMoveReconciler_FailureThenSuccess(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	runner := &scriptedRunner{fail: map[string][]error{"n1": {errors.New("transient")}}}
	r := newTestReconciler(t, cat, runner)
	ctx := context.Background()
	for pass := 0; pass < 2; pass++ {
		if err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("ReconcileOnce pass %d: %v", pass, err)
		}
	}
	if len(runner.steps) != 2 || runner.steps[1].NodeID != "n1" || runner.steps[1].Attempt != 2 {
		t.Fatalf("steps = %+v, want n1 attempts 1 and 2", runner.steps)
	}
	m := storedMove(t, kv, "acme")
	if m == nil || m.State != MoveStateCopying || !reflect.DeepEqual(m.NodesCopied, []string{"n1"}) || m.Error != "" {
		t.Fatalf("journal = %+v, want n1 copied", m)
	}
}

func TestMoveReconciler_CorruptJournalDoesNotStopAnother(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	ctx := context.Background()
	if err := kv.Put(ctx, MoveKey("aaa"), []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner)
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if len(runner.steps) != 1 || runner.steps[0].TenantID != "acme" {
		t.Fatalf("steps = %+v, want acme's step", runner.steps)
	}
}

func TestMoveReconciler_CatalogErrorReturned(t *testing.T) {
	cat, _, tx := newAdmittedMove(t)
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner)
	tx.forceFailures = maxCASRetries
	err := r.ReconcileOnce(context.Background())
	var target *MoveJournalConflictError
	if !errors.As(err, &target) {
		t.Fatalf("ReconcileOnce error = %v, want *MoveJournalConflictError", err)
	}
	if len(runner.steps) != 0 {
		t.Fatalf("steps = %+v, want none: the grant failed", runner.steps)
	}
}

func TestMoveReconciler_NilDependencies(t *testing.T) {
	cat, _, _ := newMoveTestCatalog(t)
	var target *InvalidCatalogDependencyError
	if _, err := NewMoveReconciler(nil, &scriptedRunner{}); !errors.As(err, &target) {
		t.Fatalf("nil catalog error = %v, want *InvalidCatalogDependencyError", err)
	}
	if _, err := NewMoveReconciler(cat, nil); !errors.As(err, &target) {
		t.Fatalf("nil runner error = %v, want *InvalidCatalogDependencyError", err)
	}
}

func TestMoveReconciler_CancelledContext(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner, WithMoveReconcilerLogger(nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.ReconcileOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconcileOnce error = %v, want context.Canceled", err)
	}
	if len(runner.steps) != 0 {
		t.Fatalf("steps = %+v, want none", runner.steps)
	}
	if m := storedMove(t, kv, "acme"); m == nil || m.State != MoveStateAdmitted {
		t.Fatalf("journal = %+v, want admitted", m)
	}
}

func TestMoveReconciler_ScanFailure(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner)
	boom := errors.New("scan failed")
	kv.failScan = boom
	if err := r.ReconcileOnce(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("ReconcileOnce error = %v, want the scan failure", err)
	}
	if len(runner.steps) != 0 {
		t.Fatalf("steps = %+v, want none", runner.steps)
	}
}

// cancellingRunner cancels the reconcile pass's context mid-step and
// reports the cancellation, as a runner interrupted by shutdown does.
type cancellingRunner struct {
	cancel context.CancelFunc
	steps  []MoveStep
}

func (r *cancellingRunner) RunMoveStep(ctx context.Context, step MoveStep) error {
	r.steps = append(r.steps, step)
	r.cancel()
	return ctx.Err()
}

func TestMoveReconciler_RunnerCancelsTheContext(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &cancellingRunner{cancel: cancel}
	r := newTestReconciler(t, cat, runner)
	if err := r.ReconcileOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconcileOnce error = %v, want context.Canceled", err)
	}
	if len(runner.steps) != 1 {
		t.Fatalf("steps = %+v, want one", runner.steps)
	}
	m := storedMove(t, kv, "acme")
	if m == nil || m.StepNode != "n1" || m.StepAttempts != 0 || m.Error != "" {
		t.Fatalf("journal = %+v, want n1 granted with no failure recorded", m)
	}
}

func TestMoveReconciler_LoggerReceivesTheFailure(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cat.logger = logger
	ctx := context.Background()
	if err := kv.Put(ctx, MoveKey("aaa"), []byte("{not json")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	runner := &scriptedRunner{fail: map[string][]error{"n1": {errors.New("disk full")}}}
	r := newTestReconciler(t, cat, runner, WithMoveReconcilerLogger(logger))
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"move step failed", "node=n1", "disk full", "skipping corrupt move journal"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q lacks %q", out, want)
		}
	}
}
