// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// recorder is a concurrency-safe, ordered event log shared by the fakes in
// this file and the test assertions that read it.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

// fakeLockable is a volume.Lockable double. It records every Unlock/Lock
// call under name, optionally failing one or the other, and -- for the
// "lock happens before the key is wiped" assertion -- records whether the
// system barrier's master key was still readable at the moment Lock ran.
type fakeLockable struct {
	name   string
	rec    *recorder
	system *StorageBarrier

	failUnlock error
	failLock   error

	keyAvailableDuringLock bool
}

func (f *fakeLockable) Unlock(_ context.Context, _ volume.VolumeKey) error {
	f.rec.record("unlock:" + f.name)
	return f.failUnlock
}

func (f *fakeLockable) Lock(ctx context.Context) error {
	if f.system != nil {
		_, err := f.system.GetMasterKey(ctx)
		f.keyAvailableDuringLock = err == nil
	}
	f.rec.record("lock:" + f.name)
	return f.failLock
}

// newUnsealedTestBarrier builds an initialized StorageBarrier for this
// file's tests and returns it SEALED -- Initialize itself leaves a fresh
// barrier unsealed, so this seals it back before returning, leaving the
// caller free to attach a VolumeLifecycle and then call Unseal itself to
// observe the ordering under test.
func newUnsealedTestBarrier(t *testing.T) (*StorageBarrier, Credentials, *mockAuditLogger) {
	t.Helper()
	audit := &mockAuditLogger{}
	cfg := BarrierConfig{RootKeyPath: "test/root-key", AuditLogger: audit}
	b, err := NewStorageBarrier(testLogger(), NewMemoryBackend(), cfg, testSoftwareStrategy(t))
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	creds := initBarrier(t, b)
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal (reset after Initialize): %v", err)
	}
	return b, creds, audit
}

func TestVolumeLifecycle_TrackUntrackTracked(t *testing.T) {
	b, _, _ := newUnsealedTestBarrier(t)
	vl := NewVolumeLifecycle(b, "node-1", testLogger())

	if got := vl.Tracked(); len(got) != 0 {
		t.Fatalf("Tracked() on a fresh VolumeLifecycle = %v, want empty", got)
	}

	rec := &recorder{}
	if err := vl.Track("bulk", &fakeLockable{name: "bulk", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if got, want := vl.Tracked(), []string{"bulk", "fast"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Tracked() = %v, want %v", got, want)
	}

	vl.Untrack("bulk")
	if got, want := vl.Tracked(), []string{"fast"}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Tracked() after Untrack = %v, want %v", got, want)
	}
	// Untrack of an absent pool is a no-op.
	vl.Untrack("bulk")
}

func TestVolumeLifecycle_TrackInvalid(t *testing.T) {
	b, _, _ := newUnsealedTestBarrier(t)
	vl := NewVolumeLifecycle(b, "node-1", testLogger())

	if err := vl.Track("", &fakeLockable{}); err == nil {
		t.Fatal("Track(empty pool) should fail")
	}
	if err := vl.Track("fast", nil); err == nil {
		t.Fatal("Track(nil lockable) should fail")
	}
}

func TestVolumeLifecycle_TrackDuplicateRefused(t *testing.T) {
	b, _, _ := newUnsealedTestBarrier(t)
	vl := NewVolumeLifecycle(b, "node-1", testLogger())
	rec := &recorder{}

	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	err := vl.Track("fast", &fakeLockable{name: "fast-2", rec: rec})
	var target *VolumeAlreadyTrackedError
	if !errors.As(err, &target) {
		t.Fatalf("Track(duplicate) error = %v, want *VolumeAlreadyTrackedError", err)
	}
	if target.Pool != "fast" {
		t.Fatalf("VolumeAlreadyTrackedError.Pool = %q, want fast", target.Pool)
	}
}

// TestVolumeLifecycle_UnlockRunsBeforeLaterUnsealObservers proves the
// ordering decision D6 requires: unlock happens after the system barrier
// unseals, and before whatever opens tenant/database stores on the volume.
//
// VolumeLifecycle enforces this the same way the real composition does: by
// registering its own OnUnsealed observer at construction, BEFORE anything
// that restores tenant stores (in production, BarrierRegistry's own
// OnUnsealed observer) is built over the same system barrier. This test
// stands a minimal "store open" observer in for that later registration,
// to isolate the ordering mechanism VolumeLifecycle itself is responsible
// for.
func TestVolumeLifecycle_UnlockRunsBeforeLaterUnsealObservers(t *testing.T) {
	b, creds, _ := newUnsealedTestBarrier(t)
	rec := &recorder{}

	vl := NewVolumeLifecycle(b, "node-1", testLogger())
	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// Stands in for BarrierRegistry's unsealSystemManagedTenants, which in
	// production is registered when NewBarrierRegistry is built -- always
	// after the node's VolumeLifecycle, by construction order.
	b.OnUnsealed(func(context.Context) { rec.record("store-open") })

	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	got := rec.all()
	want := []string{"unlock:fast", "store-open"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("event order = %v, want %v", got, want)
	}
}

// TestVolumeLifecycle_UnlockFailureDoesNotBlockOtherPoolsOrUnseal proves the
// per-pool blast radius: one pool's failed Unlock neither stops the other
// tracked pools from being attempted nor fails the barrier's Unseal call
// itself. The node still boots; only that pool's tenants are refused later
// (by the resolution layer, not this type).
func TestVolumeLifecycle_UnlockFailureDoesNotBlockOtherPoolsOrUnseal(t *testing.T) {
	b, creds, audit := newUnsealedTestBarrier(t)
	rec := &recorder{}

	vl := NewVolumeLifecycle(b, "node-1", testLogger())
	if err := vl.Track("broken", &fakeLockable{name: "broken", rec: rec, failUnlock: errors.New("device absent")}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal should succeed despite a pool's failed unlock, got: %v", err)
	}

	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("expected both pools' Unlock to be attempted, got %v", got)
	}

	denied := false
	for _, e := range audit.Events() {
		if e.Action == "volume_unlock" && e.Outcome == "deny" && e.Details["pool"] == "broken" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("expected an audited deny for the broken pool's failed unlock")
	}
}

// TestVolumeLifecycle_LockRunsBeforeKeyWipe proves the other half of
// decision D6: Lock is called while the system barrier's root key is still
// readable, and the key is wiped only once every tracked pool has been
// given the chance to lock.
func TestVolumeLifecycle_LockRunsBeforeKeyWipe(t *testing.T) {
	b, creds, _ := newUnsealedTestBarrier(t)
	rec := &recorder{}
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	vl := NewVolumeLifecycle(b, "node-1", testLogger())
	fake := &fakeLockable{name: "fast", rec: rec, system: b}
	if err := vl.Track("fast", fake); err != nil {
		t.Fatalf("Track: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if !fake.keyAvailableDuringLock {
		t.Fatal("Lock ran after the root key was wiped, not before")
	}
	if !b.IsSealed() {
		t.Fatal("Seal did not complete: barrier is not sealed")
	}
}

// TestVolumeLifecycle_LockFailureIsReportedButWipeStillCompletes proves the
// ruled failure semantics: a failed Lock is surfaced (as a typed, audited
// error) from Seal, but Seal still finishes wiping the barrier's root key,
// and every other tracked pool is still given the chance to lock.
func TestVolumeLifecycle_LockFailureIsReportedButWipeStillCompletes(t *testing.T) {
	b, creds, audit := newUnsealedTestBarrier(t)
	rec := &recorder{}
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	vl := NewVolumeLifecycle(b, "node-1", testLogger())
	brokenErr := errors.New("device busy")
	if err := vl.Track("broken", &fakeLockable{name: "broken", rec: rec, failLock: brokenErr}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	err := b.Seal()
	if err == nil {
		t.Fatal("Seal should report the failed Lock")
	}
	var target *VolumeLockError
	if !errors.As(err, &target) {
		t.Fatalf("Seal error = %v, want it to wrap *VolumeLockError", err)
	}
	if target.Pool != "broken" {
		t.Fatalf("VolumeLockError.Pool = %q, want broken", target.Pool)
	}

	if !b.IsSealed() {
		t.Fatal("a failed Lock must not prevent the key wipe from completing")
	}

	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("expected both pools' Lock to be attempted, got %v", got)
	}

	denied := false
	for _, e := range audit.Events() {
		if e.Action == "volume_lock" && e.Outcome == "deny" && e.Details["pool"] == "broken" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("expected an audited deny for the broken pool's failed lock")
	}
}

// fakeKeyProvider is a volume.KeyProvider double that always returns a
// fixed key, recording whether it was ever called.
type fakeKeyProvider struct {
	key    volume.VolumeKey
	called bool
}

func (f *fakeKeyProvider) VolumeKey(context.Context, string, string, string) (volume.VolumeKey, error) {
	f.called = true
	return f.key, nil
}

// keyCapturingLockable is a volume.Lockable double that records exactly
// the key its Unlock received, so a test can prove it came from an
// injected KeyProvider rather than the default software derivation.
type keyCapturingLockable struct {
	gotKey volume.VolumeKey
}

func (l *keyCapturingLockable) Unlock(_ context.Context, key volume.VolumeKey) error {
	l.gotKey = key
	return nil
}
func (l *keyCapturingLockable) Lock(context.Context) error { return nil }

// TestVolumeLifecycle_WithVolumeKeyProvider_UsedInsteadOfSoftwareDerivation
// proves decision D6's hardware-custody seam: VolumeLifecycle obtains its
// Unlock key from WHATEVER volume.KeyProvider is configured, never by
// deriving one itself -- a composer's own (potentially hardware-rooted)
// implementation is used exactly as given, not merely consulted alongside
// the software default.
func TestVolumeLifecycle_WithVolumeKeyProvider_UsedInsteadOfSoftwareDerivation(t *testing.T) {
	b, creds, _ := newUnsealedTestBarrier(t)
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	var wantKey volume.VolumeKey
	for i := range wantKey {
		wantKey[i] = byte(i + 1)
	}
	fakeProvider := &fakeKeyProvider{key: wantKey}
	vl := NewVolumeLifecycle(b, "node-1", testLogger(), WithVolumeKeyProvider(fakeProvider))

	captured := &keyCapturingLockable{}
	if err := vl.Track("fast", captured); err != nil {
		t.Fatalf("Track: %v", err)
	}

	// unlockAll only fires on an actual Unseal transition; drive one.
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	if !fakeProvider.called {
		t.Fatal("the injected KeyProvider was never called")
	}
	if captured.gotKey != wantKey {
		t.Fatalf("Unlock received %v, want the injected KeyProvider's key %v", captured.gotKey, wantKey)
	}
}

// TestVolumeLifecycle_WithCloseStoresFunc_RunsBeforeLock proves decision
// D6's actual ordering: closeStores is called for a pool BEFORE Lock, not
// merely documented as if BarrierRegistry.SealAll already closed
// everything by the time lockAll runs (it does not -- TenantBarrier.Seal
// only releases the encryptor, it never closes the underlying store).
func TestVolumeLifecycle_WithCloseStoresFunc_RunsBeforeLock(t *testing.T) {
	b, creds, _ := newUnsealedTestBarrier(t)
	rec := &recorder{}
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	closeFn := func(_ context.Context, pool string) error {
		rec.record("close:" + pool)
		return nil
	}
	vl := NewVolumeLifecycle(b, "node-1", testLogger(), WithCloseStoresFunc(closeFn))
	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	got := rec.all()
	want := []string{"close:fast", "lock:fast"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("event order = %v, want %v", got, want)
	}
}

// TestVolumeLifecycle_WithCloseStoresFunc_FailureSkipsLockButWipeCompletes
// proves a closeStores failure refuses that pool's Lock (never run against
// a volume whose stores may still be open) while still reporting the
// failure and completing the key wipe, matching the existing failed-Lock
// semantics.
func TestVolumeLifecycle_WithCloseStoresFunc_FailureSkipsLockButWipeCompletes(t *testing.T) {
	b, creds, audit := newUnsealedTestBarrier(t)
	rec := &recorder{}
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	closeErr := errors.New("close failed: store busy")
	closeFn := func(_ context.Context, pool string) error {
		rec.record("close:" + pool)
		return closeErr
	}
	vl := NewVolumeLifecycle(b, "node-1", testLogger(), WithCloseStoresFunc(closeFn))
	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	err := b.Seal()
	if err == nil {
		t.Fatal("Seal should report the failed closeStores")
	}
	var target *VolumeLockError
	if !errors.As(err, &target) {
		t.Fatalf("Seal error = %v, want it to wrap *VolumeLockError", err)
	}
	if !b.IsSealed() {
		t.Fatal("a failed closeStores must not prevent the key wipe from completing")
	}

	got := rec.all()
	if len(got) != 1 || got[0] != "close:fast" {
		t.Fatalf("events = %v, want only [close:fast] -- Lock must never run when closeStores fails", got)
	}

	denied := false
	for _, e := range audit.Events() {
		if e.Action == "volume_lock" && e.Outcome == "deny" && e.Details["pool"] == "fast" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("expected an audited deny for the pool's failed closeStores")
	}
}

// hangingLockable is a volume.Lockable whose Lock never returns and
// ignores ctx entirely -- a device driver that truly hangs, not merely
// one that is slow to notice cancellation.
type hangingLockable struct{}

func (hangingLockable) Unlock(context.Context, volume.VolumeKey) error { return nil }
func (hangingLockable) Lock(context.Context) error {
	var never chan struct{}
	<-never // blocks forever regardless of ctx
	return nil
}

// TestVolumeLifecycle_HungLockTimesOutAndWipeStillCompletes proves a Lock
// call that never returns, and never even looks at ctx, cannot hang Seal
// forever: lockOne bounds it in its own goroutine, reports
// *VolumeLockTimeoutError, and the caller (StorageBarrier.Seal) still
// wipes the root key.
func TestVolumeLifecycle_HungLockTimesOutAndWipeStillCompletes(t *testing.T) {
	b, creds, _ := newUnsealedTestBarrier(t)
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	vl := NewVolumeLifecycle(b, "node-1", testLogger(), WithObserverTimeout(50*time.Millisecond))
	if err := vl.Track("stuck", hangingLockable{}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- b.Seal() }()

	select {
	case err := <-done:
		var target *VolumeLockTimeoutError
		if !errors.As(err, &target) {
			t.Fatalf("Seal error = %v, want it to wrap *VolumeLockTimeoutError", err)
		}
		if target.Pool != "stuck" {
			t.Fatalf("VolumeLockTimeoutError.Pool = %q, want stuck", target.Pool)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Seal did not return within 5s of a hung Lock -- the observer timeout bound did not work")
	}

	if !b.IsSealed() {
		t.Fatal("a hung Lock must not prevent the key wipe from completing")
	}
}

// TestVolumeLifecycle_SealIdempotentWithNoLockables proves a node that
// tracks no Lockable volumes (every pool is "dir") seals exactly as it did
// before this feature existed.
func TestVolumeLifecycle_SealIdempotentWithNoLockables(t *testing.T) {
	b, creds, _ := newUnsealedTestBarrier(t)
	_ = NewVolumeLifecycle(b, "node-1", testLogger())
	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// Seal is idempotent.
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal (second): %v", err)
	}
}

// failingKeyProvider is a volume.KeyProvider whose every VolumeKey fails.
type failingKeyProvider struct{ err error }

func (p failingKeyProvider) VolumeKey(context.Context, string, string, string) (volume.VolumeKey, error) {
	return volume.VolumeKey{}, p.err
}

// TestVolumeLifecycle_KeyProviderFailureLeavesThePoolLocked: when the
// injected KeyProvider cannot produce a pool's key, Unlock is never called
// with a zero key -- the pool stays locked, the failure is audited against
// that pool, and the node unseal still completes. A nil logger is accepted
// and defaulted, so the failure path's logging cannot panic.
func TestVolumeLifecycle_KeyProviderFailureLeavesThePoolLocked(t *testing.T) {
	b, creds, audit := newUnsealedTestBarrier(t)
	errCustody := errors.New("key custody unavailable")
	rec := &recorder{}

	vl := NewVolumeLifecycle(b, "node-1", nil, WithVolumeKeyProvider(failingKeyProvider{err: errCustody}))
	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: rec}); err != nil {
		t.Fatalf("Track: %v", err)
	}

	if err := b.Unseal(context.Background(), creds); err != nil {
		t.Fatalf("Unseal should succeed despite the key provider failing, got: %v", err)
	}
	if got := rec.all(); len(got) != 0 {
		t.Fatalf("Lockable saw %v; Unlock must not run without a key", got)
	}
	denied := false
	for _, e := range audit.Events() {
		if e.Action == "volume_unlock" && e.Outcome == "deny" && e.Details["pool"] == "fast" {
			denied = true
		}
	}
	if !denied {
		t.Fatal("expected an audited deny for the pool whose key could not be obtained")
	}

	// The per-pool error itself names the pool and keeps the provider's cause.
	err := vl.unlockOne(context.Background(), "fast")
	var unlockErr *VolumeUnlockError
	if !errors.As(err, &unlockErr) {
		t.Fatalf("unlockOne() = %v, want *VolumeUnlockError", err)
	}
	if unlockErr.Pool != "fast" || !errors.Is(err, errCustody) {
		t.Fatalf("VolumeUnlockError = %+v, want Pool=fast wrapping the provider's error", unlockErr)
	}
}

// TestSoftwareVolumeKeyProvider_SealedBarrierYieldsNoKey: the default
// provider derives a pool key from the system barrier's root key, so while
// the barrier is sealed there is no key to derive -- it fails rather than
// handing out a key derived from nothing. Once unsealed it derives the same
// key DeriveVolumeKey does for that node and pool.
func TestSoftwareVolumeKeyProvider_SealedBarrierYieldsNoKey(t *testing.T) {
	b, creds, _ := newUnsealedTestBarrier(t) // returned sealed
	p := &softwareVolumeKeyProvider{system: b}
	ctx := context.Background()

	if _, err := p.VolumeKey(ctx, "node-1", "fast", "fast"); err == nil {
		t.Fatal("VolumeKey on a sealed system barrier returned a key")
	}

	if err := b.Unseal(ctx, creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	got, err := p.VolumeKey(ctx, "node-1", "fast", "fast")
	if err != nil {
		t.Fatalf("VolumeKey on an unsealed barrier: %v", err)
	}
	defer got.Wipe()
	master, err := b.GetMasterKey(ctx)
	if err != nil {
		t.Fatalf("GetMasterKey: %v", err)
	}
	want, err := volume.DeriveVolumeKey(master, "node-1", "fast")
	if err != nil {
		t.Fatalf("DeriveVolumeKey: %v", err)
	}
	defer want.Wipe()
	if got != want {
		t.Fatal("software provider's key differs from DeriveVolumeKey for the same node and pool")
	}
}

// TestVolumeLifecycle_TrackRefusalsNameTheirCause: Track's refusals say what
// was wrong -- the bad argument, or the pool already tracked -- so a composer
// misconfiguring pools gets an actionable boot error.
func TestVolumeLifecycle_TrackRefusalsNameTheirCause(t *testing.T) {
	b, _, _ := newUnsealedTestBarrier(t)
	vl := NewVolumeLifecycle(b, "node-1", testLogger())

	err := vl.Track("", &fakeLockable{})
	var invalid *InvalidVolumeLifecycleError
	if !errors.As(err, &invalid) {
		t.Fatalf("Track(empty) = %v, want *InvalidVolumeLifecycleError", err)
	}
	if want := "qrdb/seal: invalid volume lifecycle registration: empty pool name"; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}

	if err := vl.Track("fast", &fakeLockable{name: "fast", rec: &recorder{}}); err != nil {
		t.Fatalf("Track: %v", err)
	}
	err = vl.Track("fast", &fakeLockable{name: "fast-2", rec: &recorder{}})
	if want := "qrdb/seal: volume pool already tracked: fast"; err == nil || err.Error() != want {
		t.Fatalf("Track(duplicate) error = %v, want %q", err, want)
	}
}
