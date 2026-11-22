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
// scriptedRunner fails a step's attempts from a script keyed
// "<phase>/<node>", else "<node>" (every phase), and records every step it
// is handed.
type scriptedRunner struct {
	fail  map[string][]error
	steps []MoveStep
}

func (r *scriptedRunner) RunMoveStep(_ context.Context, step MoveStep) error {
	r.steps = append(r.steps, step)
	script, ok := r.fail[step.Phase+"/"+step.NodeID]
	if !ok {
		script = r.fail[step.NodeID]
	}
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
		{TenantID: "acme", NodeID: "n1", FromPool: "fast", ToPool: "bulk", Attempt: 1, Phase: MoveStepPhaseCopy},
		{TenantID: "acme", NodeID: "n2", FromPool: "fast", ToPool: "bulk", Attempt: 1, Phase: MoveStepPhaseCopy},
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
			runner := &scriptedRunner{fail: map[string][]error{
				"copy/n1":  {boom, boom, boom, boom},
				"abort/n1": {boom, boom, boom, boom},
			}}
			r := newTestReconciler(t, cat, runner, tc.opts...)
			for pass := 0; pass < 5; pass++ {
				if err := r.ReconcileOnce(context.Background()); err != nil {
					t.Fatalf("ReconcileOnce pass %d: %v", pass, err)
				}
			}
			var copies []MoveStep
			for _, s := range runner.steps {
				if s.Phase == MoveStepPhaseCopy {
					copies = append(copies, s)
				}
			}
			if len(copies) != tc.runs {
				t.Fatalf("copy runs = %d, want %d", len(copies), tc.runs)
			}
			for i, s := range copies {
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

// fakeRecorder records each call and fails the first len(fail) of them.
type fakeRecorder struct {
	calls [][3]string
	fail  []error
}

func (f *fakeRecorder) RecordTenantPoolMoved(_ context.Context, tenantID, fromPool, toPool string) error {
	f.calls = append(f.calls, [3]string{tenantID, fromPool, toPool})
	if len(f.calls) <= len(f.fail) {
		return f.fail[len(f.calls)-1]
	}
	return nil
}

func reconcilePasses(t *testing.T, r *MoveReconciler, n int) {
	t.Helper()
	for pass := 0; pass < n; pass++ {
		if err := r.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("ReconcileOnce pass %d: %v", pass, err)
		}
	}
}

func TestMoveReconciler_FullMoveCutsOverAndRetires(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	recorder := &fakeRecorder{}
	cat.SetMoveCutoverRecorder(recorder)
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner)
	pathBefore := storedAssignment(t, kv, "acme").Path
	reconcilePasses(t, r, 6)
	want := []string{"copy/n1", "copy/n2", "retire/n1", "retire/n2"}
	var got []string
	for _, s := range runner.steps {
		got = append(got, s.Phase+"/"+s.NodeID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(recorder.calls, [][3]string{{"acme", "fast", "bulk"}}) {
		t.Fatalf("recorder calls = %v, want one (acme, fast, bulk)", recorder.calls)
	}
	if m := storedMove(t, kv, "acme"); m != nil {
		t.Fatalf("journal = %+v, want deleted", m)
	}
	a := storedAssignment(t, kv, "acme")
	if a.Pool != "bulk" || a.Path != pathBefore || a.ReservedBytes != 200 {
		t.Fatalf("assignment = %+v, want bulk", a)
	}
	if reservedOn(t, cat, "fast") != 0 || reservedOn(t, cat, "bulk") != 200 {
		t.Fatal("reservations not moved to the destination")
	}
	// A pass with no journal left does nothing.
	reconcilePasses(t, r, 1)
	if len(runner.steps) != len(want) {
		t.Fatalf("steps after the move = %d", len(runner.steps))
	}
}

func TestMoveReconciler_CopiedWaitsForRecorder(t *testing.T) {
	cat, kv, _ := copiedMove(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner, WithMoveReconcilerLogger(logger))
	reconcilePasses(t, r, 2)
	if m := storedMove(t, kv, "acme"); m.State != MoveStateCopied {
		t.Fatalf("journal = %+v, want copied", m)
	}
	if n := strings.Count(buf.String(), "waits for the tenant recorder"); n != 2 {
		t.Fatalf("wait logged %d times over 2 passes, want once per pass", n)
	}
	if len(runner.steps) != 0 {
		t.Fatalf("steps = %+v, want none", runner.steps)
	}
	// Cut over but the recorder unwired again before the tenant is recorded:
	// the move waits; once recorded, retire needs no recorder.
	recorder := &fakeRecorder{}
	cat.SetMoveCutoverRecorder(recorder)
	reconcilePasses(t, r, 1)
	cat.SetMoveCutoverRecorder(nil)
	reconcilePasses(t, r, 1)
	if m := storedMove(t, kv, "acme"); m.State != MoveStateCutover || m.TenantRecorded || len(recorder.calls) != 0 {
		t.Fatalf("journal = %+v, calls %v, want cutover waiting", m, recorder.calls)
	}
	cat.SetMoveCutoverRecorder(recorder)
	reconcilePasses(t, r, 1)
	cat.SetMoveCutoverRecorder(nil)
	reconcilePasses(t, r, 2)
	if m := storedMove(t, kv, "acme"); m != nil {
		t.Fatalf("journal = %+v, want retired and deleted", m)
	}
}

func TestMoveReconciler_RecorderErrorRetried(t *testing.T) {
	cat, kv, _ := copiedMove(t)
	boom := errors.New("tenant store unavailable")
	recorder := &fakeRecorder{fail: []error{boom}}
	cat.SetMoveCutoverRecorder(recorder)
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner)
	reconcilePasses(t, r, 1) // cut over
	if err := r.ReconcileOnce(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("ReconcileOnce error = %v, want the recorder failure", err)
	}
	if m := storedMove(t, kv, "acme"); m.State != MoveStateCutover || m.TenantRecorded {
		t.Fatalf("journal = %+v, want cutover, not recorded", m)
	}
	reconcilePasses(t, r, 1)
	if m := storedMove(t, kv, "acme"); !m.TenantRecorded || len(recorder.calls) != 2 {
		t.Fatalf("journal = %+v, calls %d, want recorded on the retry", m, len(recorder.calls))
	}
}

func TestMoveReconciler_RetireFailureRetried(t *testing.T) {
	cat, kv, _ := cutOverMove(t)
	boom := errors.New("source busy")
	runner := &scriptedRunner{fail: map[string][]error{"retire/n1": {boom, boom, boom, boom}}}
	r := newTestReconciler(t, cat, runner)
	reconcilePasses(t, r, 4)
	m := storedMove(t, kv, "acme")
	if m.State != MoveStateCutover || m.StepNode != "n1" || m.StepAttempts != 4 || m.Error != boom.Error() {
		t.Fatalf("journal = %+v, want cutover, n1 retrying", m)
	}
	reconcilePasses(t, r, 2)
	if m := storedMove(t, kv, "acme"); m != nil {
		t.Fatalf("journal = %+v, want deleted", m)
	}
}

func TestMoveReconciler_CancelThenAbort(t *testing.T) {
	cat, kv, _ := newAdmittedMove(t)
	runner := &scriptedRunner{}
	r := newTestReconciler(t, cat, runner)
	ctx := context.Background()
	assignBefore := mustGet(t, kv, AssignmentKey("acme"))
	reconcilePasses(t, r, 1) // n1 copied
	if _, err := cat.CancelMove(ctx, "acme"); err != nil {
		t.Fatalf("CancelMove: %v", err)
	}
	reconcilePasses(t, r, 2)
	want := []MoveStep{
		{TenantID: "acme", NodeID: "n1", FromPool: "fast", ToPool: "bulk", Attempt: 1, Phase: MoveStepPhaseCopy},
		{TenantID: "acme", NodeID: "n1", FromPool: "fast", ToPool: "bulk", Attempt: 1, Phase: MoveStepPhaseAbort, Copied: true},
		{TenantID: "acme", NodeID: "n2", FromPool: "fast", ToPool: "bulk", Attempt: 1, Phase: MoveStepPhaseAbort},
	}
	if !reflect.DeepEqual(runner.steps, want) {
		t.Fatalf("steps = %+v, want %+v", runner.steps, want)
	}
	if m := storedMove(t, kv, "acme"); m != nil {
		t.Fatalf("journal = %+v, want deleted", m)
	}
	if !bytes.Equal(assignBefore, mustGet(t, kv, AssignmentKey("acme"))) {
		t.Fatal("abort changed the assignment")
	}
	if reservedOn(t, cat, "bulk") != 0 || reservedOn(t, cat, "fast") != 200 {
		t.Fatal("abort did not release the destination only")
	}
}

func TestMoveReconciler_AbortFailureRetried(t *testing.T) {
	cat, kv, _ := copiedMove(t)
	if _, err := cat.CancelMove(context.Background(), "acme"); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("remove failed")
	runner := &scriptedRunner{fail: map[string][]error{"abort/n1": {boom}}}
	r := newTestReconciler(t, cat, runner)
	reconcilePasses(t, r, 1)
	if m := storedMove(t, kv, "acme"); m.StepNode != "n1" || m.StepAttempts != 1 || len(m.NodesCopied) != 2 {
		t.Fatalf("journal = %+v, want n1 kept", m)
	}
	reconcilePasses(t, r, 2)
	if m := storedMove(t, kv, "acme"); m != nil {
		t.Fatalf("journal = %+v, want deleted", m)
	}
}

func TestMoveReconciler_StepPhases(t *testing.T) {
	ctx := context.Background()
	phases := map[string]bool{}
	// Copy then retire on one move.
	cat, _, _ := newAdmittedMove(t)
	cat.SetMoveCutoverRecorder(&fakeRecorder{})
	runner := &scriptedRunner{}
	reconcilePasses(t, newTestReconciler(t, cat, runner), 6)
	// Abort on another.
	cat2, _, _ := copiedMove(t)
	if _, err := cat2.CancelMove(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	reconcilePasses(t, newTestReconciler(t, cat2, runner), 1)
	for _, s := range runner.steps {
		phases[s.Phase] = true
	}
	for _, p := range []string{MoveStepPhaseCopy, MoveStepPhaseRetire, MoveStepPhaseAbort} {
		if !phases[p] {
			t.Errorf("runner never saw phase %q (saw %v)", p, phases)
		}
	}
}

func TestMoveReconciler_PhaseCatalogErrorReturned(t *testing.T) {
	cat, kv, _ := cutOverMove(t)
	m := storedMove(t, kv, "acme")
	m.NodesRetired = []string{"n1", "n2"}
	putMove(t, kv, m)
	r := newTestReconciler(t, cat, &scriptedRunner{})
	err := r.ReconcileOnce(context.Background())
	var target *MoveConflictError
	if !errors.As(err, &target) {
		t.Fatalf("ReconcileOnce error = %v, want the grant's *MoveConflictError", err)
	}
	// An unknown state is skipped.
	m.State = "unknown"
	putMove(t, kv, m)
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce over an unknown state: %v", err)
	}
}
