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

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// fakeTenantStore is an in-memory, stateful tenant.TenantServicer used by the
// Phase-3 quota integration test. It embeds mockTenantServicer for the methods
// the test does not exercise and overrides the lifecycle/quota methods the
// resolver, accountant, and reconciler actually call so the persisted
// CurrentStorageBytes counter behaves like the real tenant store.
type fakeTenantStore struct {
	mockTenantServicer
	mu      sync.Mutex
	tenants map[string]*tenant.TenantInfo
}

func newFakeTenantStore() *fakeTenantStore {
	return &fakeTenantStore{tenants: map[string]*tenant.TenantInfo{}}
}

func (f *fakeTenantStore) add(info *tenant.TenantInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenants[info.ID] = info
}

func (f *fakeTenantStore) current(id string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tenants[id].Quotas.CurrentStorageBytes
}

func (f *fakeTenantStore) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ti, ok := f.tenants[id]
	if !ok {
		return nil, tenant.ErrTenantNotFound
	}
	cp := *ti
	if ti.Quotas != nil {
		q := *ti.Quotas
		cp.Quotas = &q
	}
	return &cp, nil
}

func (f *fakeTenantStore) ListTenants(_ context.Context) ([]tenant.TenantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tenant.TenantInfo, 0, len(f.tenants))
	for _, ti := range f.tenants {
		out = append(out, *ti)
	}
	return out, nil
}

func (f *fakeTenantStore) Quotas(_ context.Context, id string) (*quota.Quotas, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ti, ok := f.tenants[id]
	if !ok || ti.Quotas == nil {
		return &quota.Quotas{}, nil
	}
	q := *ti.Quotas
	return &q, nil
}

func (f *fakeTenantStore) IncrementQuotaUsage(_ context.Context, id string, r quota.Resource, d int64) (*tenant.TenantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ti := f.tenants[id]
	cur, _ := ti.Quotas.CurrentUsage(r)
	_ = ti.Quotas.SetCurrentUsage(r, cur+d)
	return ti, nil
}

func (f *fakeTenantStore) DecrementQuotaUsage(_ context.Context, id string, r quota.Resource, d int64) (*tenant.TenantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ti := f.tenants[id]
	cur, _ := ti.Quotas.CurrentUsage(r)
	_ = ti.Quotas.SetCurrentUsage(r, cur-d) // SetCurrentUsage clamps to zero
	return ti, nil
}

func (f *fakeTenantStore) SetQuotaUsage(_ context.Context, id string, r quota.Resource, v int64) (*tenant.TenantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ti := f.tenants[id]
	_ = ti.Quotas.SetCurrentUsage(r, v)
	return ti, nil
}

// buildQuotaStack composes the production resolver + accountant + reconciler the
// same way the daemon does, returning the shared registry, accountant, and a
// reconciler whose cycle the test drives directly.
func buildQuotaStack(t *testing.T, store *fakeTenantStore) (*restServiceRegistry, *quota.Accountant, *quota.Reconciler) {
	t.Helper()

	acct, err := quota.NewAccountant(quota.AccountantConfig{
		Quotas: store.Quotas,
		Flush: func(ctx context.Context, id string, delta int64) error {
			switch {
			case delta > 0:
				_, e := store.IncrementQuotaUsage(ctx, id, quota.ResourceStorageBytes, delta)
				return e
			case delta < 0:
				_, e := store.DecrementQuotaUsage(ctx, id, quota.ResourceStorageBytes, -delta)
				return e
			default:
				return nil
			}
		},
	})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}

	dbReg := NewDatabaseServiceRegistry("default")
	dbReg.Register("default", &DatabaseServices{KV: newMockKVService()})

	reg := NewRESTServiceRegistry(dbReg, nil, tenantid.System,
		WithTenantServicer(store),
		withQuotaAccountant(acct),
	).(*restServiceRegistry)

	rec, err := quota.NewReconciler(quota.ReconcilerConfig{
		Accountant: acct,
		Interval:   time.Hour,
		Tenants: func(ctx context.Context) ([]quota.StorageTenant, error) {
			infos, e := store.ListTenants(ctx)
			if e != nil {
				return nil, e
			}
			out := make([]quota.StorageTenant, 0, len(infos))
			for i := range infos {
				ti := &infos[i]
				out = append(out, quota.StorageTenant{
					ID:       ti.ID,
					DataRoot: ti.DataRoot,
					Volume:   ti.EffectiveIsolationType() == tenant.IsolationVolume,
				})
			}
			return out, nil
		},
		SetUsage: func(ctx context.Context, id string, b int64) error {
			_, e := store.SetQuotaUsage(ctx, id, quota.ResourceStorageBytes, b)
			return e
		},
	})
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	return reg, acct, rec
}

// TestQuotaPhase3_EndToEnd drives the full Phase-3 storage-quota stack through
// the real transport-neutral resolver: writes are admitted/rejected against
// MaxStorageBytes, the in-memory accumulator is flushed to the persisted
// CurrentStorageBytes counter, deletes free space, and the reconciler corrects
// a deliberately-skewed counter for a dedicated-volume tenant.
func TestQuotaPhase3_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store := newFakeTenantStore()
	// Prefix tenant with a tight 100-byte storage limit for write enforcement.
	store.add(&tenant.TenantInfo{
		ID:            "acme",
		IsolationType: tenant.IsolationPrefix,
		Quotas:        &quota.Quotas{MaxStorageBytes: 100},
	})
	reg, acct, rec := buildQuotaStack(t, store)

	kv, err := reg.KVService("acme", "default")
	if err != nil {
		t.Fatalf("KVService: %v", err)
	}

	// (a) A write under the limit succeeds and, after a reconcile cycle (which
	// flushes the accumulator), the persisted counter rises.
	if err := kv.Put(ctx, "k1", make([]byte, 60)); err != nil {
		t.Fatalf("put 60: %v", err)
	}
	rec.RunOnce(ctx)
	if got := store.current("acme"); got != 60 {
		t.Fatalf("(a) CurrentStorageBytes = %d, want 60", got)
	}

	// (b) A write that would exceed the limit is rejected with ExceededError
	// (the error REST maps to HTTP 429) and is never committed.
	err = kv.Put(ctx, "k2", make([]byte, 50))
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("(b) expected *quota.ExceededError, got %v", err)
	}
	if _, gerr := kv.Get(ctx, "k2"); gerr == nil {
		t.Fatal("(b) rejected write must not be readable")
	}

	// (c) Deleting frees space; the counter drops and writes succeed again.
	if err := kv.Delete(ctx, "k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rec.RunOnce(ctx)
	if got := store.current("acme"); got != 0 {
		t.Fatalf("(c) CurrentStorageBytes after delete = %d, want 0", got)
	}
	if err := kv.Put(ctx, "k3", make([]byte, 90)); err != nil {
		t.Fatalf("(c) put 90 after freeing space: %v", err)
	}

	// (d) A dedicated-volume tenant whose persisted counter has drifted is
	// corrected by the reconciler walking its on-disk DataRoot.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.bin"), make([]byte, 300), 0o600); err != nil {
		t.Fatal(err)
	}
	store.add(&tenant.TenantInfo{
		ID:            "vol",
		IsolationType: tenant.IsolationVolume,
		DataRoot:      root,
		Quotas:        &quota.Quotas{MaxStorageBytes: 1 << 20, CurrentStorageBytes: 99999}, // skewed
	})
	rec.RunOnce(ctx)
	if got := store.current("vol"); got != 300 {
		t.Fatalf("(d) volume CurrentStorageBytes = %d, want 300 (reconciled to on-disk size)", got)
	}
	if got := acct.Usage("vol"); got != 300 {
		t.Fatalf("(d) accountant base = %d, want 300", got)
	}
}
