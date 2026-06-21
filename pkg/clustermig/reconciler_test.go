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
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// recordingRecycler records each scope it was asked to recycle and can be made
// to fail a configurable number of times before succeeding.
type recordingRecycler struct {
	mu        sync.Mutex
	calls     []string
	lastCred  []byte
	failFirst int
	failErr   error
}

func (r *recordingRecycler) Recycle(_ context.Context, m *ClusterMigration, cred []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, scopeLabel(m.TenantID, m.Database))
	r.lastCred = append([]byte(nil), cred...)
	if r.failFirst > 0 {
		r.failFirst--
		if r.failErr == nil {
			r.failErr = errors.New("recycle boom")
		}
		return r.failErr
	}
	return nil
}

func (r *recordingRecycler) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// TestReconciler_RecyclesOwnedScope proves an owner node runs the recycle once
// and acks PhaseComplete, satisfying the migration's completion check.
func TestReconciler_RecyclesOwnedScope(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2"})
	rec := &recordingRecycler{}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)

	n, err := r.ReconcileOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("ReconcileOnce = %d, %v; want 1, nil", n, err)
	}
	if rec.count() != 1 {
		t.Fatalf("recycler called %d times, want 1", rec.count())
	}
	st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
	if st == nil || st.Phase != seal.PhaseComplete || st.Generation != 1 {
		t.Fatalf("n1 status = %+v, want gen1 complete", st)
	}
}

// TestReconciler_Idempotent proves a second pass does not re-run the recycle once
// this node has acked the current generation.
func TestReconciler_Idempotent(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)

	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	n, err := r.ReconcileOnce(ctx)
	if err != nil || n != 0 {
		t.Fatalf("second pass = %d, %v; want 0, nil", n, err)
	}
	if rec.count() != 1 {
		t.Fatalf("recycler called %d times across two passes, want 1", rec.count())
	}
}

// TestReconciler_NotOwnerSkips proves a node absent from the owner set never
// recycles, even though the intent record exists.
func TestReconciler_NotOwnerSkips(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2"})
	rec := &recordingRecycler{}
	r := NewReconciler(s, "n9", rec, nil, nil, nil)

	n, err := r.ReconcileOnce(ctx)
	if err != nil || n != 0 {
		t.Fatalf("ReconcileOnce = %d, %v; want 0, nil", n, err)
	}
	if rec.count() != 0 {
		t.Fatal("non-owner must not recycle")
	}
	if st, _ := s.GetNodeStatus(ctx, tid, db, "n9"); st != nil {
		t.Fatalf("non-owner must not write a status, got %+v", st)
	}
}

// TestReconciler_RetriesAfterFailure proves a failed recycle records PhaseFailed
// (keeping the scope incomplete) and is retried on the next pass until it
// succeeds.
func TestReconciler_RetriesAfterFailure(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{failFirst: 1}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)

	// First pass: recycle fails, status is failed, scope incomplete.
	if n, err := r.ReconcileOnce(ctx); err != nil || n != 0 {
		t.Fatalf("failing pass = %d, %v; want 0, nil", n, err)
	}
	st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
	if st == nil || st.Phase != seal.PhaseFailed || st.Detail == "" {
		t.Fatalf("after failure n1 status = %+v, want failed with detail", st)
	}
	if ok, _ := s.IsComplete(ctx, tid, db); ok {
		t.Fatal("scope must be incomplete after a failed recycle")
	}
	// Second pass: recycle succeeds, scope completes.
	if n, err := r.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("retry pass = %d, %v; want 1, nil", n, err)
	}
	if ok, _ := s.IsComplete(ctx, tid, db); !ok {
		t.Fatal("scope must complete after the retry succeeds")
	}
	if rec.count() != 2 {
		t.Fatalf("recycler called %d times, want 2 (fail + retry)", rec.count())
	}
}

// TestReconciler_NewGenerationReRecycles proves a re-issued migration (new
// generation) makes a previously-complete node owe again.
func TestReconciler_NewGenerationReRecycles(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)

	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("gen1 pass: %v", err)
	}
	// Re-issue as gen 2.
	seedMigration(t, s, 2, []string{"n1"})
	n, err := r.ReconcileOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("gen2 pass = %d, %v; want 1, nil", n, err)
	}
	st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
	if st == nil || st.Generation != 2 || st.Phase != seal.PhaseComplete {
		t.Fatalf("n1 status = %+v, want gen2 complete", st)
	}
	if rec.count() != 2 {
		t.Fatalf("recycler called %d times, want 2 (per generation)", rec.count())
	}
}

// TestReconciler_MultipleScopes proves one pass progresses every scope this node
// owns.
func TestReconciler_MultipleScopes(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	// Scope 1: tenant/db (this node owns).
	seedMigration(t, s, 1, []string{"n1"})
	// Scope 2: system (this node owns).
	if err := s.PutMigration(ctx, &ClusterMigration{
		Target: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Owners: []string{"n1"}, Generation: 1,
	}); err != nil {
		t.Fatalf("PutMigration(system): %v", err)
	}
	rec := &recordingRecycler{}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)

	n, err := r.ReconcileOnce(ctx)
	if err != nil || n != 2 {
		t.Fatalf("ReconcileOnce = %d, %v; want 2, nil", n, err)
	}
	if ok, _ := s.IsComplete(ctx, tid, db); !ok {
		t.Fatal("tenant scope must complete")
	}
	if ok, _ := s.IsComplete(ctx, "", ""); !ok {
		t.Fatal("system scope must complete")
	}
}

// staticOpener returns a fixed credential and records whether it was asked, or
// errors when err is set.
type staticOpener struct {
	cred []byte
	err  error
}

func (o staticOpener) Open(_ context.Context, _ *ClusterMigration) ([]byte, error) {
	if o.err != nil {
		return nil, o.err
	}
	return append([]byte(nil), o.cred...), nil
}

// TestReconciler_OpensAndPassesCredential proves the reconciler opens this node's
// sealed credential and hands the plaintext to the recycler.
func TestReconciler_OpensAndPassesCredential(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{}
	r := NewReconciler(s, "n1", rec, staticOpener{cred: []byte("unlock-secret")}, nil, nil)

	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if string(rec.lastCred) != "unlock-secret" {
		t.Fatalf("recycler got credential %q, want unlock-secret", rec.lastCred)
	}
}

// TestReconciler_OpenError records PhaseFailed and does not recycle when the
// credential cannot be opened.
func TestReconciler_OpenError(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{}
	r := NewReconciler(s, "n1", rec, staticOpener{err: errors.New("decrypt failed")}, nil, nil)

	if n, err := r.ReconcileOnce(ctx); err != nil || n != 0 {
		t.Fatalf("ReconcileOnce = %d, %v; want 0, nil (open error recorded, not fatal to pass)", n, err)
	}
	if rec.count() != 0 {
		t.Fatal("recycler must not run when the credential cannot be opened")
	}
	st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
	if st == nil || st.Phase != seal.PhaseFailed {
		t.Fatalf("status = %+v, want PhaseFailed after open error", st)
	}
}

// TestReconciler_RollingTwoNodes simulates two nodes reconciling the same scope
// independently (rolling): the scope is complete only once both have acked,
// proving per-node recycles converge without a shared key or ordering.
func TestReconciler_RollingTwoNodes(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1", "n2"})
	rec1, rec2 := &recordingRecycler{}, &recordingRecycler{}
	r1 := NewReconciler(s, "n1", rec1, nil, nil, nil)
	r2 := NewReconciler(s, "n2", rec2, nil, nil, nil)

	// n1 migrates first; scope still incomplete (n2 outstanding).
	if _, err := r1.ReconcileOnce(ctx); err != nil {
		t.Fatalf("n1 pass: %v", err)
	}
	if ok, _ := s.IsComplete(ctx, tid, db); ok {
		t.Fatal("scope must be incomplete with only n1 done")
	}
	if a, total, _ := s.Progress(ctx, tid, db); a != 1 || total != 2 {
		t.Fatalf("Progress = %d/%d, want 1/2", a, total)
	}
	// n2 migrates later; now complete.
	if _, err := r2.ReconcileOnce(ctx); err != nil {
		t.Fatalf("n2 pass: %v", err)
	}
	if ok, _ := s.IsComplete(ctx, tid, db); !ok {
		t.Fatal("scope must complete after both nodes done")
	}
}
