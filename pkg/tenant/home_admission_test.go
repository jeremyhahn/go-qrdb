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

package tenant

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// countingAdmission is a HomeAdmission that refuses with err when set and
// otherwise counts admissions, releases and the operations in flight. Its
// release is idempotent, as the contract allows a caller to rely on.
type countingAdmission struct {
	err error

	mu       sync.Mutex
	tenants  []string
	admitted int
	released int
	inflight int
}

var _ HomeAdmission = (*countingAdmission)(nil)

func (a *countingAdmission) AdmitHome(tenantID string) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.tenants = append(a.tenants, tenantID)
	if a.err != nil {
		return nil, a.err
	}
	a.admitted++
	a.inflight++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.released++
			a.inflight--
		})
	}, nil
}

func (a *countingAdmission) counts() (admitted, released, inflight int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.admitted, a.released, a.inflight
}

// admittedHomeFixture returns an isolated tenant's home store from a
// LocalHomes gated by admission, and the fixture holding its barrier.
func admittedHomeFixture(t *testing.T, admission HomeAdmission) (*homeFixture, kvstore.KVStore) {
	t.Helper()
	f := newHomeFixture(t)
	f.createIsolated(t, "vault", TenantIsolationPolicyInherit)
	homes, err := NewLocalHomes(f.reg, WithHomeAdmission(admission))
	if err != nil {
		t.Fatalf("NewLocalHomes: %v", err)
	}
	store, err := homes.HomeStore(context.Background(), "vault")
	if err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	return f, store
}

// homeStoreOps is every data operation of a home store, each named, run
// against store; the index operations run before any registration, so the
// namespaced store above delegates them rather than refusing them itself.
func homeStoreOps(t *testing.T, store kvstore.KVStore) map[string]func() error {
	t.Helper()
	ctx := context.Background()
	atomicStore, ok := store.(kvstore.AtomicKVStore)
	if !ok {
		t.Fatal("an admitted home store must keep the barrier's atomic capability")
	}
	guarded, ok := store.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		t.Fatal("an admitted home store must keep the barrier's guarded-batch capability")
	}
	return map[string]func() error{
		"Put":    func() error { return store.Put(ctx, "users/alice", []byte("v")) },
		"Get":    func() error { _, err := store.Get(ctx, "users/alice"); return err },
		"Delete": func() error { return store.Delete(ctx, "users/gone") },
		"Scan": func() error {
			return store.Scan(ctx, "users/", func(string, []byte) error { return nil })
		},
		"List":       func() error { _, err := store.List(ctx, "users/"); return err },
		"Exists":     func() error { _, err := store.Exists(ctx, "users/alice"); return err },
		"QueryIndex": func() error { _, err := store.QueryIndex(ctx, "by-name", []byte("a")); return err },
		"ScanIndex":  func() error { _, err := store.ScanIndex(ctx, "by-name", nil, nil); return err },
		"QueryCompoundIndex": func() error {
			_, err := store.QueryCompoundIndex(ctx, "by-pair", [][]byte{[]byte("a")})
			return err
		},
		"ScanCompoundIndex": func() error {
			_, err := store.ScanCompoundIndex(ctx, "by-pair", [][]byte{[]byte("a")}, nil, nil)
			return err
		},
		"PutIfAbsent": func() error { _, err := atomicStore.PutIfAbsent(ctx, "users/bob", []byte("b")); return err },
		"CompareAndDelete": func() error {
			_, err := atomicStore.CompareAndDelete(ctx, "users/carol", []byte("c"))
			return err
		},
		"CompareAndSwap": func() error {
			_, err := atomicStore.CompareAndSwap(ctx, "users/alice", []byte("v"), []byte("w"))
			return err
		},
		"PutAllIfUnchanged": func() error {
			_, err := guarded.PutAllIfUnchanged(ctx, "users/guard", nil, []kvstore.KV{{Key: "users/dave", Value: []byte("d")}})
			return err
		},
		"PutAllIfAllUnchanged": func() error {
			_, err := guarded.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: "users/guard"}}, []kvstore.KV{{Key: "users/erin", Value: []byte("e")}})
			return err
		},
	}
}

// indexOps are the homeStoreOps the barrier store refuses as unsupported
// once admitted, which is its answer and not the admission's.
var indexOps = map[string]bool{
	"QueryIndex": true, "ScanIndex": true, "QueryCompoundIndex": true, "ScanCompoundIndex": true,
}

// A refused admission refuses every data operation with its own error,
// unchanged, and nothing reaches the barrier.
func TestLocalHomes_WithHomeAdmission_RefusesEveryOperation(t *testing.T) {
	errFenced := errors.New("fenced")
	admission := &countingAdmission{err: errFenced}
	f, store := admittedHomeFixture(t, admission)

	ops := homeStoreOps(t, store)
	for name, op := range ops {
		if err := op(); !errors.Is(err, errFenced) {
			t.Errorf("%s under a refused admission = %v, want the admission's error", name, err)
		}
	}
	if got := len(admission.tenants); got != len(ops) {
		t.Fatalf("admission asked %d times, want once per operation (%d)", got, len(ops))
	}
	for _, id := range admission.tenants {
		if id != "vault" {
			t.Fatalf("admission asked for tenant %q, want vault", id)
		}
	}
	tb, err := f.reg.GetTenant("vault")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	keys, err := tb.List(context.Background(), HomeName+"/")
	if err != nil || len(keys) != 0 {
		t.Fatalf("barrier holds %v (%v) under a refused admission; want nothing", keys, err)
	}
}

// An admitted operation runs against the barrier and releases its admission
// exactly once, whatever the operation returns.
func TestLocalHomes_WithHomeAdmission_AdmitsAndReleasesEachOperation(t *testing.T) {
	admission := &countingAdmission{}
	f, store := admittedHomeFixture(t, admission)

	ops := homeStoreOps(t, store)
	for _, name := range []string{"Put", "Get", "Exists", "List", "Scan", "PutIfAbsent", "CompareAndSwap",
		"CompareAndDelete", "Delete", "PutAllIfUnchanged", "PutAllIfAllUnchanged",
		"QueryIndex", "ScanIndex", "QueryCompoundIndex", "ScanCompoundIndex"} {
		err := ops[name]()
		if indexOps[name] {
			if !errors.Is(err, kvstore.ErrIndexUnsupported) {
				t.Errorf("%s = %v, want the barrier store's kvstore.ErrIndexUnsupported", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s admitted = %v, want success", name, err)
		}
	}
	admitted, released, inflight := admission.counts()
	if admitted != len(ops) || released != admitted || inflight != 0 {
		t.Fatalf("admitted %d, released %d, in flight %d; want %d, %d, 0", admitted, released, inflight, len(ops), len(ops))
	}
	tb, err := f.reg.GetTenant("vault")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	ctx := context.Background()
	for key, want := range map[string]string{"users/alice": "w", "users/bob": "b", "users/dave": "d", "users/erin": "e"} {
		got, err := tb.Get(ctx, HomeName+"/"+key)
		if err != nil || string(got) != want {
			t.Errorf("barrier Get(%s/%s) = (%q, %v), want %q", HomeName, key, got, err, want)
		}
	}
}

// A Scan holds its admission while its callback runs, so a drain waiting on
// the admission waits for the whole scan.
func TestLocalHomes_WithHomeAdmission_ScanHoldsAdmissionThroughCallbacks(t *testing.T) {
	admission := &countingAdmission{}
	_, store := admittedHomeFixture(t, admission)
	ctx := context.Background()
	if err := store.Put(ctx, "users/alice", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	called := false
	err := store.Scan(ctx, "users/", func(string, []byte) error {
		called = true
		if _, _, inflight := admission.counts(); inflight != 1 {
			t.Errorf("in flight during the Scan callback = %d, want 1", inflight)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("Scan = %v, callback called %v; want success with one record", err, called)
	}
	if _, _, inflight := admission.counts(); inflight != 0 {
		t.Fatalf("in flight after the Scan = %d, want 0", inflight)
	}
}

// RegisterEntityIndexes reads and writes no record and so asks no
// admission; it still answers with the barrier store's own capability.
func TestLocalHomes_WithHomeAdmission_RegisterEntityIndexesNeedsNoAdmission(t *testing.T) {
	admission := &countingAdmission{err: errors.New("fenced")}
	_, store := admittedHomeFixture(t, admission)
	err := store.RegisterEntityIndexes("user", []kvstore.EntityIndex{{FieldName: "Name", JSONFieldName: "name"}})
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v, want the barrier store's kvstore.ErrIndexUnsupported", err)
	}
	if n := len(admission.tenants); n != 0 {
		t.Fatalf("RegisterEntityIndexes asked admission %d times, want 0", n)
	}
}

// An admitted store re-resolves the tenant's barrier for each operation: one
// the registry no longer holds fails the operation instead of serving a
// barrier that was replaced.
func TestLocalHomes_WithHomeAdmission_ResolvesTheCurrentBarrier(t *testing.T) {
	admission := &countingAdmission{}
	f, store := admittedHomeFixture(t, admission)
	ctx := context.Background()
	if err := store.Put(ctx, "users/alice", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := f.reg.UnregisterTenant("vault"); err != nil {
		t.Fatalf("UnregisterTenant: %v", err)
	}
	if _, err := store.Get(ctx, "users/alice"); err == nil {
		t.Fatal("Get after the barrier left the registry must fail, not read the old barrier")
	}
	if err := store.RegisterEntityIndexes("user", nil); err == nil {
		t.Fatal("RegisterEntityIndexes after the barrier left the registry must fail")
	}
	if _, released, inflight := admission.counts(); inflight != 0 || released == 0 {
		t.Fatalf("released %d, in flight %d after a failed resolution; want every admission released", released, inflight)
	}
}

// Without WithHomeAdmission the home store is the namespaced barrier store
// itself, with no admitted layer between them.
func TestLocalHomes_WithoutAdmission_StoreIsUnadmitted(t *testing.T) {
	f := newHomeFixture(t)
	f.createIsolated(t, "vault", TenantIsolationPolicyInherit)
	homes, err := NewLocalHomes(f.reg)
	if err != nil {
		t.Fatalf("NewLocalHomes: %v", err)
	}
	if _, err := homes.HomeStore(context.Background(), "vault"); err != nil {
		t.Fatalf("HomeStore: %v", err)
	}
	cached, ok := homes.stores.Load("vault")
	if !ok {
		t.Fatal("HomeStore cached no entry")
	}
	if entry := cached.(*localHomeEntry); entry.guarded != nil {
		t.Fatal("an unadmitted LocalHomes must not build the admitted layer")
	}
}
