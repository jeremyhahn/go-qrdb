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

package credentials

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A scheduled rotation that fails after it persisted the new value and before
// the write that advances the version and settles the schedule is landed by
// the next claimant from the staged value: the value source is called exactly
// once for the interval.
func TestRotateDue_FailureBetweenPersistAndSettleCallsSourceOnce(t *testing.T) {
	current := credentialPrefix + "k"
	for _, tc := range []struct {
		name string
		// inject arms the fault the first pass fails on.
		inject func(b *faultBackend)
		// lapse moves the service clock past the first claim's lease,
		// for a fault that also keeps the claim from being released.
		lapse bool
	}{
		{
			name:   "current write fails after staging",
			inject: func(b *faultBackend) { arm(&b.failPut, current) },
		},
		{
			name: "version write contended after the current write",
			inject: func(b *faultBackend) {
				b.onPut = func(key string) {
					if key == current {
						b.casLosses.Store(rotationMetaRetries)
						arm(&b.loseCAS, metaKey("k"))
					}
				}
			},
		},
		{
			name: "version write fails and the claim lapses",
			inject: func(b *faultBackend) {
				b.onPut = func(key string) {
					if key == current {
						arm(&b.failCAS, metaKey("k"))
					}
				}
			},
			lapse: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, backend := newFaultRotationService(t)
			setFaultPolicy(t, svc, "k", 5)
			var calls atomic.Int32
			countSource(svc, &calls)
			before, err := svc.loadBarrierMeta(t.Context(), "k")
			if err != nil {
				t.Fatalf("loadBarrierMeta: %v", err)
			}
			prev, next := rotationVersions(before.CurrentVersion, true)
			due := rotationEpoch.Add(time.Hour)

			tc.inject(backend)
			if n, err := svc.RotateDue(t.Context(), due); n != 0 || err == nil {
				t.Fatalf("RotateDue under the fault = %d, %v; want 0 and an error", n, err)
			}
			backend.onPut = nil
			backend.failPut.Store(nil)
			backend.failCAS.Store(nil)
			backend.loseCAS.Store(nil)
			if tc.lapse {
				svc.now = func() time.Time { return rotationEpoch.Add(rotationClaimLease + time.Second) }
			}

			if n, err := svc.RotateDue(t.Context(), due); n != 1 || err != nil {
				t.Fatalf("RotateDue after the fault = %d, %v; want 1, nil", n, err)
			}
			if n, err := svc.RotateDue(t.Context(), due); n != 0 || err != nil {
				t.Fatalf("RotateDue once settled = %d, %v; want 0, nil", n, err)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("value source called %d times for one interval, want 1", got)
			}

			meta, err := svc.loadBarrierMeta(t.Context(), "k")
			if err != nil {
				t.Fatalf("loadBarrierMeta: %v", err)
			}
			if meta.CurrentVersion != next || meta.Claim != nil || meta.PendingCleanup != "" ||
				!meta.NextRotation.Equal(due.Add(time.Hour)) {
				t.Fatalf("meta = version %d, claim %+v, cleanup %q, next %v; want %d, none, none, %v",
					meta.CurrentVersion, meta.Claim, meta.PendingCleanup, meta.NextRotation, next, due.Add(time.Hour))
			}
			if len(meta.History) != 1 || meta.History[0].Version != prev {
				t.Fatalf("history = %+v, want exactly v%d", meta.History, prev)
			}
			requireVersionValue(t, svc, next, "k-rotated-1")
			requireVersionValue(t, svc, prev, "k-initial")
			if _, err := svc.store.Get(t.Context(), stagedKey("k", next)); err == nil {
				t.Fatalf("the staged value under v%d survived the settled rotation", next)
			}
		})
	}
}

// A manual rotation removes a staged value an unfinished scheduled rotation
// left under the version it takes, so the stale value is neither kept nor
// later landed in place of a fresh one.
func TestRotateCredential_ClearsAStaleStagedValue(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	var calls atomic.Int32
	countSource(svc, &calls)
	before, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	_, next := rotationVersions(before.CurrentVersion, true)

	arm(&backend.failPut, credentialPrefix+"k")
	if _, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour)); err == nil {
		t.Fatal("RotateDue under the fault succeeded, want an error")
	}
	backend.failPut.Store(nil)
	if _, err := svc.RotateCredential(t.Context(), "k", RotateOptions{NewValue: []byte("k-manual")}); err != nil {
		t.Fatalf("RotateCredential: %v", err)
	}
	if _, err := svc.store.Get(t.Context(), stagedKey("k", next)); err == nil {
		t.Fatalf("the stale staged value under v%d survived the manual rotation", next)
	}
	requireVersionValue(t, svc, next, "k-manual")

	if n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour)); n != 1 || err != nil {
		t.Fatalf("RotateDue = %d, %v; want 1, nil", n, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("value source called %d times, want 2: the stale staged value must not be landed", got)
	}
	requireVersionValue(t, svc, next+1, "k-rotated-2")
}

// A scheduled rotation whose staging write fails rotates nothing: the
// current value and the version stay as they were, and the next pass draws a
// fresh value because nothing was staged.
func TestRotateDue_StagingWriteFailureRotatesNothing(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	var calls atomic.Int32
	countSource(svc, &calls)
	before, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	_, next := rotationVersions(before.CurrentVersion, true)
	due := rotationEpoch.Add(time.Hour)

	arm(&backend.failPut, stagedKey("k", next))
	_, err = svc.RotateDue(t.Context(), due)
	requireOperationError(t, err, "stage rotation")
	backend.failPut.Store(nil)
	if got, err := svc.loadBarrierMeta(t.Context(), "k"); err != nil || got.CurrentVersion != before.CurrentVersion {
		t.Fatalf("meta after the failed stage = %+v, %v; want version %d", got, err, before.CurrentVersion)
	}
	requireCurrentValue(t, svc, "k-initial")

	if n, err := svc.RotateDue(t.Context(), due); n != 1 || err != nil {
		t.Fatalf("RotateDue after the fault = %d, %v; want 1, nil", n, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("value source called %d times, want 2: nothing was staged to resume from", got)
	}
	requireVersionValue(t, svc, next, "k-rotated-2")
}

// A spent staged value whose delete fails after the version write is left
// for the rotation's cleanup, which reports the failure and reaps the record
// on the next pass.
func TestRotateDue_SpentStagedValueReapedByCleanup(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	var calls atomic.Int32
	countSource(svc, &calls)
	before, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	_, next := rotationVersions(before.CurrentVersion, true)
	due := rotationEpoch.Add(time.Hour)

	arm(&backend.failDelete, stagedKey("k", next))
	_, err = svc.RotateDue(t.Context(), due)
	requireOperationError(t, err, "delete staged rotation")
	if meta, err := svc.loadBarrierMeta(t.Context(), "k"); err != nil || meta.CurrentVersion != next || meta.PendingCleanup == "" {
		t.Fatalf("meta after the failed cleanup = %+v, %v; want a pending cleanup", meta, err)
	}
	backend.failDelete.Store(nil)

	if n, err := svc.RotateDue(t.Context(), due); n != 0 || err != nil {
		t.Fatalf("RotateDue once settled = %d, %v; want 0, nil", n, err)
	}
	if meta, err := svc.loadBarrierMeta(t.Context(), "k"); err != nil || meta.PendingCleanup != "" {
		t.Fatalf("meta after the cleanup = %+v, %v; want no pending cleanup", meta, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("value source called %d times, want 1", got)
	}
	requireVersionValue(t, svc, next, "k-rotated-1")
}

// Deleting a credential removes a staged value an unfinished scheduled
// rotation left, and reports a failure to do so.
func TestDeleteCredential_RemovesAStagedValue(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	var calls atomic.Int32
	countSource(svc, &calls)
	before, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	_, next := rotationVersions(before.CurrentVersion, true)
	arm(&backend.failPut, credentialPrefix+"k")
	if _, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour)); err == nil {
		t.Fatal("RotateDue under the fault succeeded, want an error")
	}
	backend.failPut.Store(nil)

	arm(&backend.failDelete, stagedKey("k", next))
	requireOperationError(t, svc.DeleteCredential(t.Context(), "k"), "delete staged rotation")
	backend.failDelete.Store(nil)
	if err := svc.DeleteCredential(t.Context(), "k"); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	if _, err := svc.store.Get(t.Context(), stagedKey("k", next)); err == nil {
		t.Fatalf("the staged value under v%d survived the delete", next)
	}
}

// A staged value that cannot be read is not absent: the claimant reports the
// read failure and does not call the value source for a second value.
func TestRotateDue_StagedReadFailurePropagates(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	var calls atomic.Int32
	countSource(svc, &calls)
	before, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	_, next := rotationVersions(before.CurrentVersion, true)
	due := rotationEpoch.Add(time.Hour)

	arm(&backend.failPut, credentialPrefix+"k")
	if _, err := svc.RotateDue(t.Context(), due); err == nil {
		t.Fatal("RotateDue under the fault succeeded, want an error")
	}
	backend.failPut.Store(nil)

	arm(&backend.failGet, stagedKey("k", next))
	_, err = svc.RotateDue(t.Context(), due)
	if !errors.Is(err, errInjectedFault) {
		t.Fatalf("RotateDue with an unreadable staged value = %v, want the read failure", err)
	}
	requireOperationError(t, err, "read staged rotation")
	if got := calls.Load(); got != 1 {
		t.Fatalf("value source called %d times, want 1: a staged value exists", got)
	}
	backend.failGet.Store(nil)

	if n, err := svc.RotateDue(t.Context(), due); n != 1 || err != nil {
		t.Fatalf("RotateDue after the fault = %d, %v; want 1, nil", n, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("value source called %d times for one interval, want 1", got)
	}
	requireVersionValue(t, svc, next, "k-rotated-1")
}

// TestRotateDue_CleanupKeepsVersionRetainedSinceItsRead: a manual rotation
// with a grace period runs between the scheduled cleanup's read of the
// record and its delete of the spent staged value. The manual rotation
// retains the scheduled value under the version number the cleanup read
// as current; the cleanup's delete must not remove it.
func TestRotateDue_CleanupKeepsVersionRetainedSinceItsRead(t *testing.T) {
	svc, backend := newFaultRotationService(t)
	setFaultPolicy(t, svc, "k", 5)
	var calls atomic.Int32
	countSource(svc, &calls)
	before, err := svc.loadBarrierMeta(t.Context(), "k")
	if err != nil {
		t.Fatalf("loadBarrierMeta: %v", err)
	}
	_, next := rotationVersions(before.CurrentVersion, true)

	var spent, interleaved atomic.Bool
	backend.onDelete = func(key string) {
		if strings.HasSuffix(key, stagedKey("k", next)) {
			spent.Store(true)
		}
	}
	backend.onGet = func(ctx context.Context, key string) {
		if !strings.HasSuffix(key, metaKey("k")) || !spent.Load() || !interleaved.CompareAndSwap(false, true) {
			return
		}
		if _, err := svc.RotateCredential(ctx, "k", RotateOptions{NewValue: []byte("k-manual"), GracePeriod: time.Hour}); err != nil {
			t.Errorf("interleaved RotateCredential: %v", err)
		}
	}

	if n, err := svc.RotateDue(t.Context(), rotationEpoch.Add(time.Hour)); n != 1 || err != nil {
		t.Fatalf("RotateDue = %d, %v; want 1, nil", n, err)
	}
	if !interleaved.Load() {
		t.Fatal("the manual rotation did not run between the cleanup's read and its delete")
	}
	requireVersionValue(t, svc, next, "k-rotated-1")
	requireCurrentValue(t, svc, "k-manual")
}

func requireCurrentValue(t *testing.T, svc *CredentialService, want string) {
	t.Helper()
	got, err := svc.store.Get(t.Context(), credentialPrefix+"k")
	if err != nil {
		t.Fatalf("Get current: %v", err)
	}
	if string(got) != want {
		t.Fatalf("current = %q, want %q", got, want)
	}
}

func requireVersionValue(t *testing.T, svc *CredentialService, version uint64, want string) {
	t.Helper()
	buf, err := svc.RetrieveCredentialVersionBuffer(t.Context(), "k", version)
	if err != nil {
		t.Fatalf("RetrieveCredentialVersionBuffer(v%d): %v", version, err)
	}
	defer buf.Destroy()
	if err := buf.WithBytes(func(b []byte) error {
		if string(b) != want {
			return errors.New(string(b))
		}
		return nil
	}); err != nil {
		t.Fatalf("v%d = %q, want %q", version, err.Error(), want)
	}
}
