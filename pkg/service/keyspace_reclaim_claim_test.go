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

package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
)

// errProposeTimeout stands in for a propose whose outcome is unknown: the
// caller's deadline passed, and the command may still be applied.
var errProposeTimeout = errors.New("quicraft: timeout")

// unknownOutcomeKV is a ReclaimKV whose claim of the drop-fence marker
// fails with errProposeTimeout. With landed set the claim IS applied before
// the error returns -- a propose that timed out at the caller and committed
// anyway -- otherwise it never reaches the store. A release (any swap to the
// tombstone) is recorded with the context it ran under, and fails with
// releaseErr when that is set.
type unknownOutcomeKV struct {
	*memoryKVSvc
	failPutIfAbsent bool
	failClaimSwap   bool
	landed          bool
	releaseErr      error

	releases        int
	releaseCtxError error
}

func (k *unknownOutcomeKV) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if !k.failPutIfAbsent {
		return k.memoryKVSvc.PutIfAbsent(ctx, key, value)
	}
	if k.landed {
		if _, err := k.memoryKVSvc.PutIfAbsent(context.Background(), key, value); err != nil {
			return false, err
		}
	}
	return false, errProposeTimeout
}

func (k *unknownOutcomeKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if bytes.Equal(next, dropFenceTombstone) {
		k.releases++
		k.releaseCtxError = ctx.Err()
		if k.releaseErr != nil {
			return false, k.releaseErr
		}
		return k.memoryKVSvc.CompareAndSwap(ctx, key, expected, next)
	}
	if !k.failClaimSwap {
		return k.memoryKVSvc.CompareAndSwap(ctx, key, expected, next)
	}
	if k.landed {
		if _, err := k.memoryKVSvc.CompareAndSwap(context.Background(), key, expected, next); err != nil {
			return false, err
		}
	}
	return false, errProposeTimeout
}

// expiredContext returns a context whose deadline has already passed, the
// state a create's context is in when its claim times out.
func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// assertMarker fails unless the drop-fence marker of acme/orders holds want.
func assertMarker(t *testing.T, kv *memoryKVSvc, want []byte) {
	t.Helper()
	assertMarkerAt(t, kv, DropFenceKey("acme", "orders"), want)
}

// assertMarkerAt fails unless the drop-fence marker at key holds want.
func assertMarkerAt(t *testing.T, kv *memoryKVSvc, key string, want []byte) {
	t.Helper()
	got, err := kv.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get marker: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("marker = %q, want %q", got, want)
	}
}

// TestUnfence_TimedOutClaimThatLandsIsReleased: a create's claim of the
// marker times out but is applied anyway. The create reports the timeout,
// and the marker is back at the tombstone rather than holding an
// incarnation no catalog entry names -- so the retried create, with a new
// incarnation, claims it instead of being refused as existing elsewhere.
func TestUnfence_TimedOutClaimThatLandsIsReleased(t *testing.T) {
	cases := map[string]struct {
		dropped bool // the name was dropped before: the claim swaps the tombstone
	}{
		"claim from absent":    {dropped: false},
		"claim from tombstone": {dropped: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kv := &unknownOutcomeKV{memoryKVSvc: newMemoryKVSvc(), landed: true}
			svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
			if tc.dropped {
				// The dropped database held the marker as its incarnation,
				// and its drop fenced that incarnation.
				if err := svc.UnfenceDatabaseKeyspace(context.Background(), "acme", "orders", "dropped-incarnation", ""); err != nil {
					t.Fatalf("UnfenceDatabaseKeyspace: %v", err)
				}
				if err := svc.FenceDatabaseKeyspace(context.Background(), "acme", "orders", "dropped-incarnation", ""); err != nil {
					t.Fatalf("FenceDatabaseKeyspace: %v", err)
				}
				assertMarker(t, kv.memoryKVSvc, dropFenceTombstone)
				// The drop's fence swapped to the tombstone too; count only
				// the releases of the create under test.
				kv.releases, kv.releaseCtxError = 0, nil
				kv.failClaimSwap = true
			} else {
				kv.failPutIfAbsent = true
			}

			err := svc.UnfenceDatabaseKeyspace(expiredContext(t), "acme", "orders", "first-incarnation", "")
			var fence *DropFenceError
			if !errors.As(err, &fence) || fence.Op != "mark live" {
				t.Fatalf("err = %v, want a mark-live *DropFenceError", err)
			}
			if !errors.Is(err, errProposeTimeout) {
				t.Fatalf("err = %v, want the propose timeout as its cause", err)
			}
			if kv.releases != 1 || kv.releaseCtxError != nil {
				t.Fatalf("releases = %d under ctx error %v, want one under a live context", kv.releases, kv.releaseCtxError)
			}
			assertMarker(t, kv.memoryKVSvc, dropFenceTombstone)

			kv.failPutIfAbsent, kv.failClaimSwap = false, false
			if err := svc.UnfenceDatabaseKeyspace(context.Background(), "acme", "orders", "retry-incarnation", ""); err != nil {
				t.Fatalf("the retried create must claim the released marker: %v", err)
			}
			assertMarker(t, kv.memoryKVSvc, []byte("retry-incarnation"))
		})
	}
}

// TestUnfence_TimedOutClaimThatNeverLandsLeavesTheMarker: the release only
// swaps out the create's own incarnation, so a claim that never reached the
// store leaves the marker exactly as it was.
func TestUnfence_TimedOutClaimThatNeverLandsLeavesTheMarker(t *testing.T) {
	kv := &unknownOutcomeKV{memoryKVSvc: newMemoryKVSvc(), failPutIfAbsent: true}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	err := svc.UnfenceDatabaseKeyspace(expiredContext(t), "acme", "orders", "first-incarnation", "")
	if !errors.Is(err, errProposeTimeout) {
		t.Fatalf("err = %v, want the propose timeout", err)
	}
	if _, gerr := kv.memoryKVSvc.Get(context.Background(), DropFenceKey("acme", "orders")); gerr == nil {
		t.Fatal("a claim that never landed must leave the marker absent")
	}
	kv.failPutIfAbsent = false
	if err := svc.UnfenceDatabaseKeyspace(context.Background(), "acme", "orders", "retry-incarnation", ""); err != nil {
		t.Fatalf("retried create: %v", err)
	}
	assertMarker(t, kv.memoryKVSvc, []byte("retry-incarnation"))
}

// TestUnfence_FailedReleaseIsReported: when the release fails too, the
// error names both failures and still unwraps to the claim's, so the create
// is classified by the timeout and the operator is told the marker may be
// left holding the incarnation.
func TestUnfence_FailedReleaseIsReported(t *testing.T) {
	releaseErr := errors.New("release refused")
	kv := &unknownOutcomeKV{memoryKVSvc: newMemoryKVSvc(), failPutIfAbsent: true, landed: true, releaseErr: releaseErr}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	err := svc.UnfenceDatabaseKeyspace(context.Background(), "acme", "orders", "first-incarnation", "")
	var release *MarkerReleaseError
	if !errors.As(err, &release) {
		t.Fatalf("err = %v, want *MarkerReleaseError", err)
	}
	if release.Key != DropFenceKey("acme", "orders") || !errors.Is(release.ReleaseErr, releaseErr) {
		t.Fatalf("release error = %+v", release)
	}
	if !errors.Is(err, errProposeTimeout) {
		t.Fatalf("err = %v, want it to unwrap to the claim's timeout", err)
	}
	for _, part := range []string{errProposeTimeout.Error(), releaseErr.Error()} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("Error() = %q, want it to name %q", err.Error(), part)
		}
	}
	var exists *database.AlreadyExistsError
	if errors.As(err, &exists) {
		t.Fatal("a failed release is not an already-exists refusal")
	}
}

// TestUnfence_PerDatabaseTimedOutClaimThatLandsIsReleased pins the release
// on the path of a database with a store of its own: its incarnation is
// claimed on the marker in the store its namespaced keyspace is reclaimed
// from, and a claim there that times out yet lands is released the same way.
func TestUnfence_PerDatabaseTimedOutClaimThatLandsIsReleased(t *testing.T) {
	kv := &unknownOutcomeKV{memoryKVSvc: newMemoryKVSvc(), failPutIfAbsent: true, landed: true}
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	svc.SetStoreResolver(&dedicatedRaftResolver{shard: newMemoryKVSvc()})

	err := svc.UnfenceDatabaseKeyspace(expiredContext(t), "acme", "orders", "first-incarnation", "")
	var fence *DropFenceError
	if !errors.As(err, &fence) || fence.Op != "mark live" {
		t.Fatalf("err = %v, want a mark-live *DropFenceError", err)
	}
	if !errors.Is(err, errProposeTimeout) {
		t.Fatalf("err = %v, want the propose timeout as its cause", err)
	}
	if kv.releases != 1 || kv.releaseCtxError != nil {
		t.Fatalf("releases = %d under ctx error %v, want one under a live context", kv.releases, kv.releaseCtxError)
	}
	assertMarker(t, kv.memoryKVSvc, dropFenceTombstone)

	kv.failPutIfAbsent = false
	if err := svc.UnfenceDatabaseKeyspace(context.Background(), "acme", "orders", "retry-incarnation", ""); err != nil {
		t.Fatalf("the retried create must claim the released marker: %v", err)
	}
	assertMarker(t, kv.memoryKVSvc, []byte("retry-incarnation"))
}

// TestDatabaseCreate_TimedOutMarkLiveLeavesNoHalfState drives the whole
// create stack -- DatabaseService, database.Manager, the keyspace reclaimer
// -- through a mark-live propose that times out at the caller and commits
// anyway. The create fails with the timeout as its cause, the catalog does
// not hold the name, and the drop fence is the tombstone, not an orphaned
// incarnation: the two agree that no database exists, and a retried create
// succeeds and marks its own incarnation live.
func TestDatabaseCreate_TimedOutMarkLiveLeavesNoHalfState(t *testing.T) {
	kv := &unknownOutcomeKV{memoryKVSvc: newMemoryKVSvc(), failPutIfAbsent: true, landed: true}
	mgr := databasetest.NewManager(nil, t.TempDir())
	mgr.SetKeyspaceReclaimer(NewKeyspaceReclaimService(ReclaimTarget{KV: kv}))
	t.Cleanup(func() { _ = mgr.Close() })
	svc := NewDatabaseService(mgr)
	cfg := database.DatabaseConfig{Name: "orders", Backend: database.BackendMemory}
	key := DropFenceKey(cfg.TenantID, cfg.Name)

	_, err := svc.Create(cfg)
	if !errors.Is(err, errProposeTimeout) {
		t.Fatalf("err = %v, want the mark-live timeout as its cause", err)
	}
	if _, gerr := mgr.Get(cfg.Name); gerr == nil {
		t.Fatal("a failed create must not leave the name in the catalog")
	}
	assertMarkerAt(t, kv.memoryKVSvc, key, dropFenceTombstone)

	kv.failPutIfAbsent = false
	if _, err := svc.Create(cfg); err != nil {
		t.Fatalf("the retried create must succeed: %v", err)
	}
	db, err := mgr.Get(cfg.Name)
	if err != nil {
		t.Fatalf("Get after the retried create: %v", err)
	}
	assertMarkerAt(t, kv.memoryKVSvc, key, []byte(db.Incarnation()))
}
