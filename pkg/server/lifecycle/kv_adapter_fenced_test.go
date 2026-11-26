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

package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// fencedCreators returns the two production FencedCreator implementations
// over one evaluating transaction service, so each case runs against both.
func fencedCreators(tx *evaluatingTxService) map[string]kvstore.FencedCreator {
	return map[string]kvstore.FencedCreator{
		"kvSealBackend":        kvSealBackend{KVStore: newKVStoreAdapter(&mockKVServicer{}, nil), tx: tx},
		"fencedKVStoreAdapter": &fencedKVStoreAdapter{kvStoreAdapter: &kvStoreAdapter{svc: &mockKVServicer{}}, tx: tx},
	}
}

// TestPutIfAllAbsent_CreatesWhenEveryKeyIsAbsent: the transaction's compares
// hold, the put is applied, and created=true.
func TestPutIfAllAbsent_CreatesWhenEveryKeyIsAbsent(t *testing.T) {
	for name := range fencedCreators(newEvaluatingTxService()) {
		t.Run(name, func(t *testing.T) {
			tx := newEvaluatingTxService()
			fc := fencedCreators(tx)[name]
			created, err := fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
			if err != nil || !created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want true, nil", created, err)
			}
			if got := tx.data["meta/acme"]; got == nil || string(got.Value) != "v" {
				t.Fatalf("the put was not applied: %+v", got)
			}
		})
	}
}

// TestPutIfAllAbsent_RefusedWhenAFenceIsPresent is the property #484 depends
// on: a present fence refuses the create inside the same transaction, and the
// store is unchanged.
func TestPutIfAllAbsent_RefusedWhenAFenceIsPresent(t *testing.T) {
	for name := range fencedCreators(newEvaluatingTxService()) {
		t.Run(name, func(t *testing.T) {
			tx := newEvaluatingTxService()
			tx.put("tomb/acme", []byte("retired"))
			fc := fencedCreators(tx)[name]
			created, err := fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
			if err != nil || created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want false, nil", created, err)
			}
			if _, ok := tx.data["meta/acme"]; ok {
				t.Fatal("a refused fenced create wrote the key")
			}
		})
	}
}

// TestPutIfAllAbsent_RefusedWhenTheKeyIsPresent: the created key itself is a
// fence too, so an existing record is never overwritten.
func TestPutIfAllAbsent_RefusedWhenTheKeyIsPresent(t *testing.T) {
	for name := range fencedCreators(newEvaluatingTxService()) {
		t.Run(name, func(t *testing.T) {
			tx := newEvaluatingTxService()
			tx.put("meta/acme", []byte("winner"))
			fc := fencedCreators(tx)[name]
			created, err := fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("loser"), "tomb/acme")
			if err != nil || created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want false, nil", created, err)
			}
			if string(tx.data["meta/acme"].Value) != "winner" {
				t.Fatal("a refused fenced create overwrote the existing record")
			}
		})
	}
}

// TestPutIfAllAbsent_WithoutTransactionServiceRefuses: no transaction plane,
// no capability -- the typed refusal, never a check-then-write.
func TestPutIfAllAbsent_WithoutTransactionServiceRefuses(t *testing.T) {
	for name, fc := range map[string]kvstore.FencedCreator{
		"kvSealBackend":        kvSealBackend{KVStore: newKVStoreAdapter(&mockKVServicer{}, nil)},
		"fencedKVStoreAdapter": &fencedKVStoreAdapter{kvStoreAdapter: &kvStoreAdapter{svc: &mockKVServicer{}}},
	} {
		t.Run(name, func(t *testing.T) {
			created, err := fc.PutIfAllAbsent(context.Background(), "meta/acme", []byte("v"), "tomb/acme")
			if !errors.Is(err, ErrNoTransactionService) || created {
				t.Fatalf("PutIfAllAbsent = %v, %v; want false, ErrNoTransactionService", created, err)
			}
		})
	}
}

// TestKVStoreAdapter_WithoutTransactionPlaneIsNotFenced pins the reason the
// fenced adapter is a distinct type: the plain adapter must not claim the
// capability it cannot honour.
func TestKVStoreAdapter_WithoutTransactionPlaneIsNotFenced(t *testing.T) {
	if _, ok := newKVStoreAdapter(&mockKVServicer{}, nil).(kvstore.FencedCreator); ok {
		t.Fatal("kvStoreAdapter satisfies kvstore.FencedCreator without a transaction plane")
	}
}

// txBackedKVServicer is a KV service whose single-key verbs read and write the
// same map tx evaluates transactions against, so both planes see one store.
func txBackedKVServicer(tx *evaluatingTxService) *mockKVServicer {
	value := func(key string) ([]byte, bool) {
		kv, ok := tx.data[key]
		if !ok || kv == nil {
			return nil, false
		}
		return kv.Value, true
	}
	return &mockKVServicer{
		putFn: func(_ context.Context, key string, v []byte) error {
			tx.put(key, v)
			return nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			v, ok := value(key)
			if !ok {
				return nil, &service.KeyNotFoundError{Key: key}
			}
			return v, nil
		},
		deleteFn: func(_ context.Context, key string) error {
			delete(tx.data, key)
			return nil
		},
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			var keys []string
			for k := range tx.data {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			return keys, nil
		},
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			out := map[string][]byte{}
			for k, kv := range tx.data {
				if strings.HasPrefix(k, prefix) && kv != nil {
					out[k] = kv.Value
				}
			}
			return out, nil
		},
		putIfAbsentFn: func(_ context.Context, key string, v []byte) (bool, error) {
			if _, ok := value(key); ok {
				return false, nil
			}
			tx.put(key, v)
			return true, nil
		},
		compareAndSwapFn: func(_ context.Context, key string, expected, next []byte) (bool, error) {
			v, ok := value(key)
			if !ok || !bytes.Equal(v, expected) {
				return false, nil
			}
			tx.put(key, next)
			return true, nil
		},
		compareAndDeleteFn: func(_ context.Context, key string, expected []byte) (bool, error) {
			v, ok := value(key)
			if !ok || !bytes.Equal(v, expected) {
				return false, nil
			}
			delete(tx.data, key)
			return true, nil
		},
	}
}

func TestSealDisabledTenantService_CommitsStrictClusterFloor(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("fenced adapter commits strict", func(t *testing.T) {
		tx := newEvaluatingTxService()
		store := &fencedKVStoreAdapter{kvStoreAdapter: &kvStoreAdapter{svc: txBackedKVServicer(tx)}, tx: tx}
		svc, err := tenant.NewKVTenantService(store, logger)
		if err != nil {
			t.Fatalf("NewKVTenantService: %v", err)
		}
		if err := svc.SetClusterIsolationPolicy(ctx, tenant.ClusterIsolationPolicyOperatorAccess); err != nil {
			t.Fatalf("set operator-access: %v", err)
		}
		if err := svc.SetClusterIsolationPolicy(ctx, tenant.ClusterIsolationPolicyStrict); err != nil {
			t.Fatalf("set strict: %v", err)
		}
		got, err := svc.ClusterIsolationPolicy(ctx)
		if err != nil {
			t.Fatalf("ClusterIsolationPolicy: %v", err)
		}
		if got != tenant.ClusterIsolationPolicyStrict {
			t.Fatalf("floor = %q, want %q", got, tenant.ClusterIsolationPolicyStrict)
		}
	})

	t.Run("bare adapter refuses strict", func(t *testing.T) {
		tx := newEvaluatingTxService()
		svc, err := tenant.NewKVTenantService(&kvStoreAdapter{svc: txBackedKVServicer(tx)}, logger)
		if err != nil {
			t.Fatalf("NewKVTenantService: %v", err)
		}
		if err := svc.SetClusterIsolationPolicy(ctx, tenant.ClusterIsolationPolicyOperatorAccess); err != nil {
			t.Fatalf("set operator-access: %v", err)
		}
		err = svc.SetClusterIsolationPolicy(ctx, tenant.ClusterIsolationPolicyStrict)
		if !errors.Is(err, tenant.ErrTighteningNotAtomic) {
			t.Fatalf("set strict err = %v, want ErrTighteningNotAtomic", err)
		}
		got, err := svc.ClusterIsolationPolicy(ctx)
		if err != nil {
			t.Fatalf("ClusterIsolationPolicy: %v", err)
		}
		if got != tenant.ClusterIsolationPolicyOperatorAccess {
			t.Fatalf("floor = %q, want %q", got, tenant.ClusterIsolationPolicyOperatorAccess)
		}
	})
}

// recordingTxService is evaluatingTxService that also records the compares
// of every Execute, so a test can see which guards one proposal carried.
type recordingTxService struct {
	*evaluatingTxService
	compares [][]service.Compare
}

func (r *recordingTxService) Execute(ctx context.Context, compares []service.Compare, successOps, failureOps []service.Op) (*service.TransactionResponse, error) {
	r.compares = append(r.compares, compares)
	return r.evaluatingTxService.Execute(ctx, compares, successOps, failureOps)
}

// TestPlacementStore_ReplicaAddFencedOnMoveJournal proves the wiring
// lifecycle builds (placement.NewStore over guardedKVStoreAdapter with
// WithReplicaAddFence(volume.MoveKey, moveJournalReplicaRefusal)): a replica
// add for a tenant whose move journal is stored is refused with the
// journal's *volume.TenantMovingReplicaError, the move key is compared at
// CompareVersion 0 in the same Execute as the placement compare, and once
// the journal is deleted (the move ended) the same write commits.
func TestPlacementStore_ReplicaAddFencedOnMoveJournal(t *testing.T) {
	ctx := context.Background()
	tx := &recordingTxService{evaluatingTxService: newEvaluatingTxService()}
	kv := &mockKVServicer{getFn: func(_ context.Context, key string) ([]byte, error) {
		if v, ok := tx.data[key]; ok {
			return v.Value, nil
		}
		return nil, nil
	}}
	adapter, err := NewGuardedKVStoreAdapter(kv, nil, tx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := placement.NewStore(adapter, placement.WithReplicaAddFence(volume.MoveKey, moveJournalReplicaRefusal))
	if err != nil {
		t.Fatal(err)
	}
	cur := &placement.Placement{TenantID: "acme", Database: "db", ShardID: 9, RF: 1, LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "n1", RaftAddr: "n1:1"}}}
	curBytes, err := json.Marshal(cur)
	if err != nil {
		t.Fatal(err)
	}
	placementKey := placement.PlacementPrefix + "acme/db"
	tx.put(placementKey, curBytes)
	journal, err := json.Marshal(volume.Move{TenantID: "acme", FromPool: "fast", ToPool: "bulk", State: volume.MoveStateAdmitted})
	if err != nil {
		t.Fatal(err)
	}
	tx.put(volume.MoveKey("acme"), journal)
	next := *cur
	next.Replicas = append([]placement.Replica{}, cur.Replicas...)
	next.Replicas = append(next.Replicas, placement.Replica{ReplicaID: 2, NodeID: "n2", RaftAddr: "n2:1", State: placement.ReplicaStateObserver})

	swapped, err := store.CompareAndSwapPlacement(ctx, cur, &next)
	var refusal *volume.TenantMovingReplicaError
	if !errors.As(err, &refusal) || swapped {
		t.Fatalf("CAS = %v, %v; want *volume.TenantMovingReplicaError", swapped, err)
	}
	if refusal.TenantID != "acme" || refusal.FromPool != "fast" || refusal.ToPool != "bulk" || refusal.State != volume.MoveStateAdmitted {
		t.Fatalf("refusal = %+v, want the stored journal's move", refusal)
	}
	if string(tx.data[placementKey].Value) != string(curBytes) {
		t.Fatal("a refused replica add changed the placement")
	}
	if len(tx.compares) != 1 {
		t.Fatalf("Execute calls = %d, want one guarded proposal", len(tx.compares))
	}
	fenced, guarded := false, false
	for _, c := range tx.compares[0] {
		if string(c.Key) == volume.MoveKey("acme") && c.Target == service.CompareVersion && c.Result == service.CompareEqual && c.Version == 0 {
			fenced = true
		}
		if string(c.Key) == placementKey && c.Target == service.CompareValue && string(c.Value) == string(curBytes) {
			guarded = true
		}
	}
	if !fenced || !guarded {
		t.Fatalf("compares = %+v, want the move key absent and the placement unchanged in the same Execute", tx.compares[0])
	}

	delete(tx.data, volume.MoveKey("acme"))
	swapped, err = store.CompareAndSwapPlacement(ctx, cur, &next)
	if err != nil || !swapped {
		t.Fatalf("after the move ended: CAS = %v, %v; want committed", swapped, err)
	}
}

func TestMoveJournalReplicaRefusal_UndecodableJournalStillRefuses(t *testing.T) {
	err := moveJournalReplicaRefusal("acme", []byte("not json"))
	var refusal *volume.TenantMovingReplicaError
	if !errors.As(err, &refusal) || refusal.TenantID != "acme" {
		t.Fatalf("refusal = %v, want *volume.TenantMovingReplicaError for acme", err)
	}
}
