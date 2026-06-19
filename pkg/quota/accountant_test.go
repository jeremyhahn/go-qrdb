// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package quota

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// fakeUsage is an in-memory quota/usage backend used to drive Accountant tests.
// It records persisted CurrentStorageBytes per tenant and acts as both the
// quota-lookup and flush target.
type fakeUsage struct {
	mu       sync.Mutex
	limit    map[string]int64
	current  map[string]int64
	flushErr error
}

func newFakeUsage() *fakeUsage {
	return &fakeUsage{limit: map[string]int64{}, current: map[string]int64{}}
}

func (f *fakeUsage) quotas(_ context.Context, tenantID string) (*Quotas, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &Quotas{
		MaxStorageBytes:     f.limit[tenantID],
		CurrentStorageBytes: f.current[tenantID],
	}, nil
}

func (f *fakeUsage) flush(_ context.Context, tenantID string, delta int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.flushErr != nil {
		return f.flushErr
	}
	next := f.current[tenantID] + delta
	if next < 0 {
		next = 0
	}
	f.current[tenantID] = next
	return nil
}

func (f *fakeUsage) persisted(tenantID string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current[tenantID]
}

func newTestAccountant(t *testing.T, fu *fakeUsage) *Accountant {
	t.Helper()
	a, err := NewAccountant(AccountantConfig{Quotas: fu.quotas, Flush: fu.flush})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}
	return a
}

func TestNewAccountant_NilFuncs(t *testing.T) {
	if _, err := NewAccountant(AccountantConfig{Flush: func(context.Context, string, int64) error { return nil }}); !errors.Is(err, ErrNilQuotaFunc) {
		t.Fatalf("expected ErrNilQuotaFunc, got %v", err)
	}
	if _, err := NewAccountant(AccountantConfig{Quotas: func(context.Context, string) (*Quotas, error) { return nil, nil }}); !errors.Is(err, ErrNilFlushFunc) {
		t.Fatalf("expected ErrNilFlushFunc, got %v", err)
	}
}

func TestAccountant_ReserveUnderLimit(t *testing.T) {
	fu := newFakeUsage()
	fu.limit["t1"] = 100
	a := newTestAccountant(t, fu)
	ctx := context.Background()

	if err := a.Reserve(ctx, "t1", 100, 40); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	if err := a.Reserve(ctx, "t1", 100, 60); err != nil {
		t.Fatalf("second reserve (at limit): %v", err)
	}
	if got := a.Usage("t1"); got != 100 {
		t.Fatalf("usage = %d, want 100", got)
	}
}

func TestAccountant_ReserveRejectsOverLimit(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	ctx := context.Background()

	if err := a.Reserve(ctx, "t1", 100, 90); err != nil {
		t.Fatalf("reserve 90: %v", err)
	}
	err := a.Reserve(ctx, "t1", 100, 20)
	var ee *ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExceededError, got %v", err)
	}
	if ee.Resource != ResourceStorageBytes {
		t.Fatalf("resource = %v, want storage_bytes", ee.Resource)
	}
	if ee.Current != 90 || ee.Limit != 100 {
		t.Fatalf("current/limit = %d/%d, want 90/100", ee.Current, ee.Limit)
	}
	// The rejected bytes must not have been recorded.
	if got := a.Usage("t1"); got != 90 {
		t.Fatalf("usage = %d, want 90 (rejection must not account)", got)
	}
}

func TestAccountant_ReserveHonorsPersistedBase(t *testing.T) {
	fu := newFakeUsage()
	fu.limit["t1"] = 100
	fu.current["t1"] = 80 // already 80 persisted
	a := newTestAccountant(t, fu)
	ctx := context.Background()

	if err := a.Reserve(ctx, "t1", 100, 20); err != nil {
		t.Fatalf("reserve 20 (to limit): %v", err)
	}
	if err := a.Reserve(ctx, "t1", 100, 1); err == nil {
		t.Fatal("expected rejection past persisted base+pending")
	}
}

func TestAccountant_UnlimitedPassthrough(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	ctx := context.Background()

	// limit <= 0 means unlimited: reserve must always succeed and still account.
	for i := 0; i < 5; i++ {
		if err := a.Reserve(ctx, "t1", 0, 1_000_000); err != nil {
			t.Fatalf("unlimited reserve: %v", err)
		}
	}
	if got := a.Usage("t1"); got != 5_000_000 {
		t.Fatalf("usage = %d, want 5000000", got)
	}
	// A negative limit is also treated as unlimited.
	if err := a.Reserve(ctx, "t1", -1, 9_999); err != nil {
		t.Fatalf("negative-limit reserve: %v", err)
	}
}

func TestAccountant_ZeroAndNegativeNAreNoOps(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	ctx := context.Background()
	if err := a.Reserve(ctx, "t1", 10, 0); err != nil {
		t.Fatalf("reserve 0: %v", err)
	}
	if err := a.Reserve(ctx, "t1", 10, -5); err != nil {
		t.Fatalf("reserve -5: %v", err)
	}
	a.Release("t1", 0)
	a.Sub("t1", -3)
	if got := a.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0", got)
	}
}

func TestAccountant_ReleaseRollsBack(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	ctx := context.Background()
	if err := a.Reserve(ctx, "t1", 100, 40); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	a.Release("t1", 40)
	if got := a.Usage("t1"); got != 0 {
		t.Fatalf("usage after release = %d, want 0", got)
	}
}

func TestAccountant_SubDecrements(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	ctx := context.Background()
	if err := a.Reserve(ctx, "t1", 1000, 100); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	a.Sub("t1", 30)
	if got := a.Usage("t1"); got != 70 {
		t.Fatalf("usage = %d, want 70", got)
	}
}

func TestAccountant_FlushPersistsDeltas(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	ctx := context.Background()

	if err := a.Reserve(ctx, "t1", 1000, 200); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	a.Sub("t1", 50) // net +150
	a.Flush(ctx)

	if got := fu.persisted("t1"); got != 150 {
		t.Fatalf("persisted = %d, want 150", got)
	}
	// After flush the in-memory effective usage is unchanged (base advanced,
	// pending cleared) and a second flush is a no-op.
	if got := a.Usage("t1"); got != 150 {
		t.Fatalf("usage = %d, want 150", got)
	}
	a.Flush(ctx)
	if got := fu.persisted("t1"); got != 150 {
		t.Fatalf("persisted after empty flush = %d, want 150", got)
	}
}

func TestAccountant_FlushErrorRetainsPending(t *testing.T) {
	fu := newFakeUsage()
	fu.flushErr = errors.New("boom")
	a := newTestAccountant(t, fu)
	ctx := context.Background()
	if err := a.Reserve(ctx, "t1", 1000, 100); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	a.Flush(ctx) // fails; delta retained
	if got := fu.persisted("t1"); got != 0 {
		t.Fatalf("persisted = %d, want 0 (flush failed)", got)
	}
	if got := a.Usage("t1"); got != 100 {
		t.Fatalf("usage = %d, want 100 (pending retained)", got)
	}
	// Recover and flush again succeeds.
	fu.flushErr = nil
	a.Flush(ctx)
	if got := fu.persisted("t1"); got != 100 {
		t.Fatalf("persisted after recovery = %d, want 100", got)
	}
}

func TestAccountant_SetBaseCorrectsAndKeepsPending(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	ctx := context.Background()
	if err := a.Reserve(ctx, "t1", 0, 10); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	// Reconciler corrects the base to the true on-disk size; pending stays.
	a.SetBase("t1", 500)
	if got := a.Usage("t1"); got != 510 {
		t.Fatalf("usage = %d, want 510 (base 500 + pending 10)", got)
	}
	a.SetBase("t1", -7) // clamped to zero
	if got := a.Usage("t1"); got != 10 {
		t.Fatalf("usage = %d, want 10", got)
	}
}

// blockingFlushUsage drives an Accountant whose flush func blocks mid-I/O so a
// test can deterministically interleave a concurrent SetBase (or Reserve) with
// the persist window. The flush signals enter once it is inside the I/O and then
// waits on release before completing.
type blockingFlushUsage struct {
	mu        sync.Mutex
	persisted int64
	enter     chan struct{}
	release   chan struct{}
}

func (b *blockingFlushUsage) quotas(context.Context, string) (*Quotas, error) {
	return &Quotas{}, nil
}

func (b *blockingFlushUsage) flush(_ context.Context, _ string, delta int64) error {
	close(b.enter)
	<-b.release
	b.mu.Lock()
	b.persisted += delta
	b.mu.Unlock()
	return nil
}

// TestAccountant_FlushSetBaseRace_NoDoubleCount pins finding M-2 case 1: a
// SetBase that lands while Flush is mid-I/O must win, and Flush must NOT re-add
// the flushed delta on top of the freshly-walked base (which double-counts).
func TestAccountant_FlushSetBaseRace_NoDoubleCount(t *testing.T) {
	bf := &blockingFlushUsage{enter: make(chan struct{}), release: make(chan struct{})}
	a, err := NewAccountant(AccountantConfig{Quotas: bf.quotas, Flush: bf.flush})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}
	ctx := context.Background()

	// pending = 100, base = 0.
	if err := a.Reserve(ctx, "t1", 0, 100); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	done := make(chan struct{})
	go func() {
		a.Flush(ctx)
		close(done)
	}()

	<-bf.enter           // Flush has snapshotted delta=100 + baseGen and is in I/O.
	a.SetBase("t1", 500) // Authoritative on-disk truth lands during the window.
	close(bf.release)    // Let the flush complete.
	<-done

	// Effective usage must be the SetBase truth (500), not 500+100=600.
	if got := a.Usage("t1"); got != 500 {
		t.Fatalf("usage = %d, want 500 (no double-count of flushed delta)", got)
	}
	if got := bf.persisted; got != 100 {
		t.Fatalf("persisted delta = %d, want 100 (delta still durably flushed once)", got)
	}
}

// TestAccountant_FlushReserveRace_NoLostUpdate pins finding M-2 case 1's other
// direction: when no SetBase intervenes, a Reserve that lands during the flush
// I/O window must be preserved — the flushed delta is cleared from pending and
// folded into base, while the new reservation remains accounted.
func TestAccountant_FlushReserveRace_NoLostUpdate(t *testing.T) {
	bf := &blockingFlushUsage{enter: make(chan struct{}), release: make(chan struct{})}
	a, err := NewAccountant(AccountantConfig{Quotas: bf.quotas, Flush: bf.flush})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}
	ctx := context.Background()

	if err := a.Reserve(ctx, "t1", 0, 100); err != nil { // pending=100
		t.Fatalf("reserve: %v", err)
	}

	done := make(chan struct{})
	go func() {
		a.Flush(ctx)
		close(done)
	}()

	<-bf.enter                                          // delta=100 snapshotted.
	if err := a.Reserve(ctx, "t1", 0, 30); err != nil { // lands during I/O: pending=130
		t.Fatalf("concurrent reserve: %v", err)
	}
	close(bf.release)
	<-done

	// base advances by 100 (gen unchanged), pending -= 100 -> 30: effective 130.
	if got := a.Usage("t1"); got != 130 {
		t.Fatalf("usage = %d, want 130 (concurrent reserve not lost)", got)
	}
}

// TestAccountant_UsageFlooredAtZero pins finding M-2 case 2's floor: an
// over-subtraction (more freed than the base reflects) must never report
// negative effective usage.
func TestAccountant_UsageFlooredAtZero(t *testing.T) {
	fu := newFakeUsage()
	a := newTestAccountant(t, fu)
	ctx := context.Background()
	if err := a.Reserve(ctx, "t1", 0, 20); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	a.Sub("t1", 100) // frees more than is accounted: base+pending = -80
	if got := a.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0 (floored, never negative)", got)
	}
	// A subsequent reservation is admitted against the floored (0) usage.
	if err := a.Reserve(ctx, "t1", 50, 50); err != nil {
		t.Fatalf("reserve after floor: %v", err)
	}
}
