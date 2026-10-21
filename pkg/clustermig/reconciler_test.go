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
	"strings"
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
	// atTarget is what AtTarget answers: the recycle already ran.
	atTarget bool
}

var _ LocalRecycler = (*recordingRecycler)(nil)

func (r *recordingRecycler) AtTarget(*ClusterMigration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.atTarget
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
	requireNodeStatusAbsent(t, ctx, s, "n9", "non-owner must not write a status")
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
	requireComplete(t, ctx, s, false, "after a failed recycle")
	// Second pass: recycle succeeds, scope completes.
	if n, err := r.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("retry pass = %d, %v; want 1, nil", n, err)
	}
	requireComplete(t, ctx, s, true, "after the retry succeeds")
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
	putMigration(t, s, &ClusterMigration{
		Target: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Owners: []string{"n1"}, Generation: 1,
	})
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

// staticSource is a CredentialSource that returns a fixed credential, or err
// when set, and records what it was asked for and what was released.
type staticSource struct {
	mu       sync.Mutex
	cred     []byte
	err      error
	asked    int
	released []uint64
}

func (o *staticSource) Credential(_ context.Context, _ *ClusterMigration) ([]byte, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.asked++
	if o.err != nil {
		return nil, o.err
	}
	return append([]byte(nil), o.cred...), nil
}

func (o *staticSource) Release(m *ClusterMigration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.released = append(o.released, m.Generation)
}

func (o *staticSource) set(cred []byte, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cred, o.err = cred, err
}

var _ CredentialSource = (*staticSource)(nil)

// TestReconciler_PassesThisNodesCredential proves the reconciler takes this
// node's credential from its local source and hands it to the recycler, and
// releases it once the recycle completes.
func TestReconciler_PassesThisNodesCredential(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{}
	src := &staticSource{cred: []byte("unlock-secret")}
	r := NewReconciler(s, "n1", rec, src, nil, nil)

	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if string(rec.lastCred) != "unlock-secret" {
		t.Fatalf("recycler got credential %q, want unlock-secret", rec.lastCred)
	}
	if len(src.released) != 1 || src.released[0] != 1 {
		t.Fatalf("released = %v, want [1]", src.released)
	}
}

// TestReconciler_AwaitsCredentialWithoutCountingAttempts proves an owner
// without its credential records phase awaiting-credential, runs no recycle,
// and is not charged an attempt: it waits through more passes than the retry
// budget allows, then completes as soon as the credential is there.
func TestReconciler_AwaitsCredentialWithoutCountingAttempts(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{}
	refused := &MigrationCredentialFileError{Path: "/creds/system", Reason: "mode 0644 is readable by others"}
	src := &staticSource{err: ErrCredentialAwaited}
	r := NewReconciler(s, "n1", rec, src, nil, nil)

	for i := 0; i < MaxRecycleAttempts+2; i++ {
		if i == 2 {
			src.set(nil, refused)
		}
		if n, err := r.ReconcileOnce(ctx); err != nil || n != 0 {
			t.Fatalf("pass %d: ReconcileOnce = %d, %v; want 0, nil", i, n, err)
		}
		st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
		if st == nil || st.Phase != PhaseAwaitingCredential || st.Attempts != 0 {
			t.Fatalf("pass %d: status = %+v, want awaiting-credential with 0 attempts", i, st)
		}
	}
	if st, _ := s.GetNodeStatus(ctx, tid, db, "n1"); st.Detail != refused.Error() {
		t.Fatalf("detail = %q, want the refusal %q", st.Detail, refused.Error())
	}
	if rec.count() != 0 {
		t.Fatal("recycled without a credential")
	}

	src.set([]byte("arrived"), nil)
	if n, err := r.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("ReconcileOnce after the credential arrived = %d, %v; want 1, nil", n, err)
	}
	if st, _ := s.GetNodeStatus(ctx, tid, db, "n1"); st.Phase != seal.PhaseComplete {
		t.Fatalf("status = %+v, want complete", st)
	}
}

// TestReconciler_AtTargetAcksWithoutCredential proves an owner whose barrier
// is already at the target acks complete without asking for a credential or
// recycling again, and releases any credential it held.
func TestReconciler_AtTargetAcksWithoutCredential(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{atTarget: true}
	src := &staticSource{err: ErrCredentialAwaited}
	r := NewReconciler(s, "n1", rec, src, nil, nil)

	if n, err := r.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("ReconcileOnce = %d, %v; want 1, nil", n, err)
	}
	if src.asked != 0 || rec.count() != 0 {
		t.Fatalf("asked %d times, recycled %d times; an owner at the target needs neither", src.asked, rec.count())
	}
	if st, _ := s.GetNodeStatus(ctx, tid, db, "n1"); st == nil || st.Phase != seal.PhaseComplete || st.Generation != 1 {
		t.Fatalf("status = %+v, want gen1 complete", st)
	}
	if len(src.released) != 1 || src.released[0] != 1 {
		t.Fatalf("released = %v, want [1]", src.released)
	}
}

// TestReconciler_AwaitingOwnerAcksOnceAtTarget proves the SYSTEM-scope reboot
// case: the recycle reached the target but its ack and the held credential
// were lost, so the owner was left awaiting a credential. Once its barrier
// reports the target, the next pass acks complete without one.
func TestReconciler_AwaitingOwnerAcksOnceAtTarget(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{}
	src := &staticSource{err: ErrCredentialAwaited}
	r := NewReconciler(s, "n1", rec, src, nil, nil)

	if n, err := r.ReconcileOnce(ctx); err != nil || n != 0 {
		t.Fatalf("first pass = %d, %v; want 0, nil", n, err)
	}
	if st, _ := s.GetNodeStatus(ctx, tid, db, "n1"); st == nil || st.Phase != PhaseAwaitingCredential {
		t.Fatalf("status = %+v, want awaiting-credential", st)
	}
	asked := src.asked

	rec.mu.Lock()
	rec.atTarget = true
	rec.mu.Unlock()
	if n, err := r.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("pass at target = %d, %v; want 1, nil", n, err)
	}
	if src.asked != asked || rec.count() != 0 {
		t.Fatalf("asked %d more times, recycled %d times; want neither", src.asked-asked, rec.count())
	}
	if st, _ := s.GetNodeStatus(ctx, tid, db, "n1"); st.Phase != seal.PhaseComplete || st.Attempts != 0 {
		t.Fatalf("status = %+v, want complete with 0 attempts", st)
	}
}

// TestReconciler_ShamirTargetNeedsNoCredential proves a Shamir target never
// asks for a credential: each node mints its own key and seals a fresh share
// set to its own officers.
func TestReconciler_ShamirTargetNeedsNoCredential(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	putMigration(t, s, &ClusterMigration{
		TenantID: tid, Database: db, Target: seal.StrategyShamir, Mechanism: seal.MechanismSwap,
		ShamirThreshold: 2, ShamirTotal: 3, Owners: []string{"n1"}, Generation: 1,
	})
	rec := &recordingRecycler{}
	src := &staticSource{err: ErrCredentialAwaited}
	r := NewReconciler(s, "n1", rec, src, nil, nil)
	if n, err := r.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("ReconcileOnce = %d, %v; want 1, nil", n, err)
	}
	if src.asked != 0 || len(rec.lastCred) != 0 {
		t.Fatalf("asked %d times, recycler got %q; a Shamir target takes no credential", src.asked, rec.lastCred)
	}
}

// TestReconciler_ReleasesCredentialWhenAbandoned proves the credential is
// released when the retry budget runs out, not held for a recycle that will
// never run again.
func TestReconciler_ReleasesCredentialWhenAbandoned(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{failFirst: 1, failErr: seal.ErrMigrationInvalidMechanism}
	src := &staticSource{cred: []byte("unlock-secret")}
	r := NewReconciler(s, "n1", rec, src, nil, nil)
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.released) != 1 {
		t.Fatalf("released = %v, want the abandoned generation released", src.released)
	}
}

// TestReconciler_KeepsCredentialForARetry proves a retryable failure keeps the
// credential: the next pass needs it.
func TestReconciler_KeepsCredentialForARetry(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{failFirst: 1}
	src := &staticSource{cred: []byte("unlock-secret")}
	r := NewReconciler(s, "n1", rec, src, nil, nil)
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(src.released) != 0 {
		t.Fatalf("released = %v after a retryable failure", src.released)
	}
}

// TestReconciler_BoundedRetry_StopsAfterMaxAttempts proves a persistently
// failing recycle is retried up to MaxRecycleAttempts times and then stops:
// owedByThisNode reports the node no longer owed, so ReconcileOnce recycles 0
// scopes on the pass that would have been the (MaxRecycleAttempts+1)th retry,
// and the terminal status's Detail records the exhaustion. This is the bound
// that turns the previously-infinite 3s-interval retry loop (the shamir
// field-drop bug's symptom) into a small, fixed number of attempts followed by
// a durable, operator-visible failure.
func TestReconciler_BoundedRetry_StopsAfterMaxAttempts(t *testing.T) {
	orig := MaxRecycleAttempts
	MaxRecycleAttempts = 3
	defer func() { MaxRecycleAttempts = orig }()

	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{failFirst: 1000, failErr: errors.New("deterministic boom")}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)

	// Passes 1..MaxRecycleAttempts each recycle once and fail.
	for i := 1; i <= MaxRecycleAttempts; i++ {
		n, err := r.ReconcileOnce(ctx)
		if err != nil || n != 0 {
			t.Fatalf("pass %d: ReconcileOnce = %d, %v; want 0, nil", i, n, err)
		}
	}
	if rec.count() != MaxRecycleAttempts {
		t.Fatalf("recycler called %d times, want %d (bounded)", rec.count(), MaxRecycleAttempts)
	}
	st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
	if st == nil || st.Phase != seal.PhaseFailed || st.Attempts != MaxRecycleAttempts {
		t.Fatalf("status after exhaustion = %+v, want Failed/Attempts=%d", st, MaxRecycleAttempts)
	}
	if !strings.Contains(st.Detail, "abandoned") {
		t.Fatalf("terminal Detail = %q, want it to explain the retries were exhausted", st.Detail)
	}

	// One MORE pass must NOT recycle again — the node is no longer owed.
	n, err := r.ReconcileOnce(ctx)
	if err != nil || n != 0 {
		t.Fatalf("post-exhaustion pass = %d, %v; want 0, nil", n, err)
	}
	if rec.count() != MaxRecycleAttempts {
		t.Fatalf("recycler called %d times after exhaustion, want still %d (no further retries)", rec.count(), MaxRecycleAttempts)
	}
	// The scope never reports complete: it failed, it did not silently vanish.
	requireComplete(t, ctx, s, false, "an abandoned, never-succeeded migration")
}

// TestReconciler_BoundedRetry_PermanentErrorAbandonsImmediately proves a
// structurally deterministic failure (e.g. an out-of-range Shamir M-of-N that
// somehow reached this node, or any other seal.MigrationOptions.Validate
// error) is abandoned on the FIRST attempt rather than burning
// MaxRecycleAttempts identical, pointless retries first.
func TestReconciler_BoundedRetry_PermanentErrorAbandonsImmediately(t *testing.T) {
	orig := MaxRecycleAttempts
	MaxRecycleAttempts = 5
	defer func() { MaxRecycleAttempts = orig }()

	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{failFirst: 1000, failErr: seal.ErrShamirThresholdInvalid}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)

	if n, err := r.ReconcileOnce(ctx); err != nil || n != 0 {
		t.Fatalf("ReconcileOnce = %d, %v; want 0, nil", n, err)
	}
	if rec.count() != 1 {
		t.Fatalf("recycler called %d times, want exactly 1 (permanent error abandons immediately)", rec.count())
	}
	st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
	if st == nil || st.Attempts != MaxRecycleAttempts {
		t.Fatalf("status after a permanent error = %+v, want Attempts=%d immediately", st, MaxRecycleAttempts)
	}

	// A further pass must not retry.
	n, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("post-abandon pass: %v -- the pass failed, so it never established that nothing was recycled", err)
	}
	if n != 0 {
		t.Fatalf("post-abandon pass recycled %d, want 0", n)
	}
	if rec.count() != 1 {
		t.Fatalf("recycler called %d times after abandonment, want still 1", rec.count())
	}
}

// TestReconciler_BoundedRetry_NewGenerationResetsAttempts proves a re-issued
// migration (new generation) gets a fresh retry budget even after the prior
// generation exhausted MaxRecycleAttempts — an operator re-issuing the same
// request (e.g. after fixing whatever caused it to fail) must not be
// permanently blocked by a stale exhaustion record.
func TestReconciler_BoundedRetry_NewGenerationResetsAttempts(t *testing.T) {
	orig := MaxRecycleAttempts
	MaxRecycleAttempts = 2
	defer func() { MaxRecycleAttempts = orig }()

	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{failFirst: 1000, failErr: errors.New("boom")}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)

	for i := 0; i < MaxRecycleAttempts; i++ {
		if _, err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("gen1 pass %d: %v", i, err)
		}
	}
	n, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("gen1 post-exhaustion pass: %v -- the pass failed, so it never established that the budget was exhausted", err)
	}
	if n != 0 {
		t.Fatalf("gen1 exhausted but still recycled %d", n)
	}

	// Re-issue as generation 2, now let the recycle succeed.
	rec.mu.Lock()
	rec.failFirst = 0
	rec.mu.Unlock()
	seedMigration(t, s, 2, []string{"n1"})
	n, err = r.ReconcileOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("gen2 pass = %d, %v; want 1, nil (fresh retry budget)", n, err)
	}
	st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
	if st == nil || st.Generation != 2 || st.Phase != seal.PhaseComplete {
		t.Fatalf("gen2 status = %+v, want gen2 complete", st)
	}
}

// recycleFunc adapts a plain function to LocalRecycler, for tests that need
// to observe reconciler-internal state (e.g. a checkpoint write) from inside
// the Recycle call rather than merely record its arguments.
type recycleFunc func(ctx context.Context, m *ClusterMigration, credential []byte) error

func (f recycleFunc) Recycle(ctx context.Context, m *ClusterMigration, credential []byte) error {
	return f(ctx, m, credential)
}

func (recycleFunc) AtTarget(*ClusterMigration) bool { return false }

// TestReconciler_AcknowledgesAtTargetWithoutACredential pins the system
// scope's reboot path: the node that accepted the request recycles with the
// credential it held, comes back from the in-process reboot with a new
// reconciler and no held credential, and has no file for it. Its barrier is
// already at the target, so it acknowledges without asking for one, rather
// than waiting in awaiting-credential forever and holding the migration
// short of completion.
func TestReconciler_AcknowledgesAtTargetWithoutACredential(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	// The pass before the reboot: credential held, recycle started, no ack.
	if err := s.PutNodeStatus(ctx, tid, db, &NodeMigrationStatus{NodeID: "n1", Generation: 1, Phase: seal.PhaseCopying}); err != nil {
		t.Fatal(err)
	}
	rec := &recordingRecycler{atTarget: true}
	src := &staticSource{err: ErrCredentialAwaited}
	r := NewReconciler(s, "n1", rec, src, nil, nil)

	if n, err := r.ReconcileOnce(ctx); err != nil || n != 1 {
		t.Fatalf("ReconcileOnce = %d, %v; want 1, nil", n, err)
	}
	if st, _ := s.GetNodeStatus(ctx, tid, db, "n1"); st == nil || st.Phase != seal.PhaseComplete || st.Generation != 1 {
		t.Fatalf("status = %+v, want generation 1 complete", st)
	}
	if src.asked != 0 || rec.count() != 0 {
		t.Fatalf("asked for a credential %d times and recycled %d times; an owner at target does neither", src.asked, rec.count())
	}
	if len(src.released) != 1 || src.released[0] != 1 {
		t.Fatalf("released = %v, want [1]", src.released)
	}
	requireComplete(t, ctx, s, true, "the only owner acknowledged")
}

// TestReconciler_NotAtTargetStillWaitsForItsCredential proves the at-target
// check does not let an owner that has not recycled acknowledge: without its
// credential it waits, and nothing is acknowledged.
func TestReconciler_NotAtTargetStillWaitsForItsCredential(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})
	rec := &recordingRecycler{}
	r := NewReconciler(s, "n1", rec, &staticSource{err: ErrCredentialAwaited}, nil, nil)

	if n, err := r.ReconcileOnce(ctx); err != nil || n != 0 {
		t.Fatalf("ReconcileOnce = %d, %v; want 0, nil", n, err)
	}
	if st, _ := s.GetNodeStatus(ctx, tid, db, "n1"); st == nil || st.Phase != PhaseAwaitingCredential {
		t.Fatalf("status = %+v, want awaiting-credential", st)
	}
	requireComplete(t, ctx, s, false, "the owner has not recycled")
}

// TestReconciler_CopyingCheckpoint_CarriesPriorAttempts pins the L1 fix: the
// PhaseCopying checkpoint recycleScope writes immediately before calling
// Recycle must carry the generation's PRIOR attempt count, not 0. That write
// lands before Recycle runs, so it is the last-persisted status if the node
// crashes mid-recycle. Zeroing it there would let a reliably-crashing recycle
// retry forever across reboots: each restart would read Attempts=0 from this
// checkpoint (via currentStatus) and reopen a fresh MaxRecycleAttempts
// budget, forever defeating the bound recordFailure/owedByThisNode rely on.
func TestReconciler_CopyingCheckpoint_CarriesPriorAttempts(t *testing.T) {
	ctx := context.Background()
	s := NewStore(newMemKV())
	seedMigration(t, s, 1, []string{"n1"})

	// First pass fails once, leaving a persisted Attempts=1 for generation 1.
	rec := &recordingRecycler{failFirst: 1}
	r := NewReconciler(s, "n1", rec, nil, nil, nil)
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("first (failing) pass: %v", err)
	}
	st, _ := s.GetNodeStatus(ctx, tid, db, "n1")
	if st == nil || st.Phase != seal.PhaseFailed || st.Attempts != 1 {
		t.Fatalf("after first failure = %+v, want Failed/Attempts=1", st)
	}

	// Second pass: a recycler that inspects the PhaseCopying checkpoint
	// recycleScope wrote just before invoking it, then "crashes" (returns an
	// error) so the pass never reaches PhaseComplete — mirroring a node
	// dying mid-Recycle, after the checkpoint but before completion.
	var checkpointed *NodeMigrationStatus
	crashRec := recycleFunc(func(ctx context.Context, m *ClusterMigration, _ []byte) error {
		checkpointed, _ = s.GetNodeStatus(ctx, m.TenantID, m.Database, "n1")
		return errors.New("simulated crash")
	})
	r2 := NewReconciler(s, "n1", crashRec, nil, nil, nil)
	if _, err := r2.ReconcileOnce(ctx); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if checkpointed == nil || checkpointed.Phase != seal.PhaseCopying || checkpointed.Attempts != 1 {
		t.Fatalf("PhaseCopying checkpoint = %+v, want Phase=Copying Attempts=1 (prior attempts carried forward, not reset to 0)", checkpointed)
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
	requireComplete(t, ctx, s, false, "only n1 done of n1,n2")
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

// TestAwaitingCredentialError names the wait and unwraps to its reason.
func TestAwaitingCredentialError(t *testing.T) {
	err := &awaitingCredentialError{Err: ErrCredentialAwaited}
	if !errors.Is(err, ErrCredentialAwaited) || err.Error() == "" {
		t.Fatalf("err = %v", err)
	}
}

// countingKV counts the writes made through it.
type countingKV struct {
	*memKV
	puts int
}

func (k *countingKV) Put(ctx context.Context, key string, v []byte) error {
	k.puts++
	return k.memKV.Put(ctx, key, v)
}

// TestReconciler_AwaitingStatusIsWrittenOnlyWhenItChanges proves an owner
// waiting for its credential writes its awaiting-credential status once, and
// again only when the reason changes -- not on every pass, which would put a
// Raft proposal per owner per poll into the log for as long as it waits.
func TestReconciler_AwaitingStatusIsWrittenOnlyWhenItChanges(t *testing.T) {
	ctx := context.Background()
	kv := &countingKV{memKV: newMemKV()}
	s := NewStore(kv)
	seedMigration(t, s, 1, []string{"n1"})
	src := &staticSource{err: ErrCredentialAwaited}
	r := NewReconciler(s, "n1", &recordingRecycler{}, src, nil, nil)

	before := kv.puts
	for range 3 {
		if _, err := r.ReconcileOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := kv.puts - before; got != 1 {
		t.Fatalf("%d status writes over three passes with the same reason, want 1", got)
	}
	src.set(nil, &MigrationCredentialFileError{Path: "/c/system", Reason: "mode 0644"})
	if _, err := r.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := kv.puts - before; got != 2 {
		t.Fatalf("%d status writes after the reason changed, want 2", got)
	}
}
