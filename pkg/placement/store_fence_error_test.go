// Copyright 2026 Jeremy Hahn
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

package placement_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

var errInjectedKV = errors.New("injected kv failure")

// faultyFenceKV is the fence fixture with one failing capability: each
// field, when set, makes that KV call fail (or, for neverApply, report a
// guard mismatch) without touching the data.
type faultyFenceKV struct {
	*fenceGuardedKV
	getErrKey  string // Get of this key fails
	putErr     bool
	batchErr   bool
	casErr     bool
	neverApply bool // PutAllIfAllUnchanged always reports a changed guard
}

func (k *faultyFenceKV) Get(ctx context.Context, key string) ([]byte, error) {
	if k.getErrKey != "" && key == k.getErrKey {
		return nil, errInjectedKV
	}
	return k.fenceGuardedKV.Get(ctx, key)
}

func (k *faultyFenceKV) Put(ctx context.Context, key string, value []byte) error {
	if k.putErr {
		return errInjectedKV
	}
	return k.fenceGuardedKV.Put(ctx, key, value)
}

func (k *faultyFenceKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if k.casErr {
		return false, errInjectedKV
	}
	return k.fenceGuardedKV.CompareAndSwap(ctx, key, expected, next)
}

func (k *faultyFenceKV) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if k.batchErr {
		return false, errInjectedKV
	}
	if k.neverApply {
		return false, nil
	}
	return k.fenceGuardedKV.PutAllIfAllUnchanged(ctx, guards, writes)
}

func placementKeyOf(p *placement.Placement) string {
	return placement.PlacementPrefix + p.TenantID + "/" + p.DatabaseID
}

func requireStoreErr(t *testing.T, err error, target error) {
	t.Helper()
	var storeErr *placement.StoreError
	if !errors.As(err, &storeErr) || !errors.Is(err, target) {
		t.Fatalf("error = %v, want a *StoreError wrapping %v", err, target)
	}
}

func TestStoreFence_KVFailuresSurfaceAsStoreErrors(t *testing.T) {
	ctx := context.Background()
	cur := fencePlacement("n1")
	grown := withReplica(cur, "n2", placement.ReplicaStateActive)
	tests := []struct {
		name  string
		fault func(k *faultyFenceKV)
		write func(s *placement.Store) error
	}{
		{
			name:  "put cannot read the current placement",
			fault: func(k *faultyFenceKV) { k.getErrKey = placementKeyOf(cur) },
			write: func(s *placement.Store) error { return s.Put(ctx, grown) },
		},
		{
			name:  "put without a replica add fails its plain write",
			fault: func(k *faultyFenceKV) { k.putErr = true },
			write: func(s *placement.Store) error { return s.Put(ctx, cur) },
		},
		{
			name:  "put's guarded batch fails",
			fault: func(k *faultyFenceKV) { k.batchErr = true },
			write: func(s *placement.Store) error { return s.Put(ctx, grown) },
		},
		{
			name: "declined put cannot read the fence",
			fault: func(k *faultyFenceKV) {
				k.neverApply = true
				k.getErrKey = fenceTestKey("t1")
			},
			write: func(s *placement.Store) error { return s.Put(ctx, grown) },
		},
		{
			name:  "replica-adding CAS's guarded batch fails",
			fault: func(k *faultyFenceKV) { k.batchErr = true },
			write: func(s *placement.Store) error {
				_, err := s.CompareAndSwapPlacement(ctx, cur, grown)
				return err
			},
		},
		{
			name:  "CAS adding no replica fails its compare-and-swap",
			fault: func(k *faultyFenceKV) { k.casErr = true },
			write: func(s *placement.Store) error {
				_, err := s.CompareAndSwapPlacement(ctx, grown, cur)
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inner := newFenceGuardedKV()
			seedPlacement(t, inner, cur)
			kv := &faultyFenceKV{fenceGuardedKV: inner}
			tc.fault(kv)
			s := newFencedStore(t, kv)
			requireStoreErr(t, tc.write(s), errInjectedKV)
		})
	}
}

func TestStoreFence_PutOfCorruptStoredPlacementFails(t *testing.T) {
	kv := newFenceGuardedKV()
	cur := fencePlacement("n1")
	kv.set(placementKeyOf(cur), []byte("{not json"))
	s := newFencedStore(t, kv)
	err := s.Put(context.Background(), cur)
	var storeErr *placement.StoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("Put over a corrupt placement = %v, want *StoreError", err)
	}
}

func TestStoreFence_PutGivesUpAfterRepeatedConflicts(t *testing.T) {
	inner := newFenceGuardedKV()
	cur := fencePlacement("n1")
	seedPlacement(t, inner, cur)
	kv := &faultyFenceKV{fenceGuardedKV: inner, neverApply: true}
	s := newFencedStore(t, kv)
	err := s.Put(context.Background(), withReplica(cur, "n2", placement.ReplicaStateActive))
	var conflict *placement.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Put under constant conflict = %v, want *ConflictError", err)
	}
	if inner.plainPuts != 0 {
		t.Fatal("a conflicted fenced Put fell back to a plain write")
	}
}

func TestWithReplicaAddFence_NilArgumentsLeaveTheStoreUnfenced(t *testing.T) {
	refusal := func(string, []byte) error { return errors.New("refused") }
	for name, opt := range map[string]placement.StoreOption{
		"nil key":     placement.WithReplicaAddFence(nil, refusal),
		"nil refusal": placement.WithReplicaAddFence(fenceTestKey, nil),
	} {
		t.Run(name, func(t *testing.T) {
			kv := newFenceGuardedKV()
			kv.set(fenceTestKey("t1"), []byte("journal"))
			s, err := placement.NewStore(kv, opt)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			if err := s.Put(context.Background(), fencePlacement("n1")); err != nil {
				t.Fatalf("unfenced Put = %v, want nil", err)
			}
			if kv.batches != 0 || kv.plainPuts != 1 {
				t.Fatalf("batches=%d plainPuts=%d, want an unguarded plain write", kv.batches, kv.plainPuts)
			}
		})
	}
}

func TestNotAReplicaError_LocalBinding(t *testing.T) {
	if !(&placement.NotAReplicaError{}).LocalBinding() {
		t.Fatal("ShardID 0 is a local binding")
	}
	if (&placement.NotAReplicaError{ShardID: 7}).LocalBinding() {
		t.Fatal("a Raft group is not a local binding")
	}
}
