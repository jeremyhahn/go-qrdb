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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestTenantService_CreateTenant_RegistryDuplicateError exercises the branch
// where the metadata store has no entry yet but the barrier registry already
// knows this tenant ID (e.g. from a prior partially-completed create).
func TestTenantService_CreateTenant_RegistryDuplicateError(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	// Create a barrier tenant once successfully so the registry has it.
	// (Barrier isolation registers a per-tenant barrier; prefix isolation
	// would not, so the duplicate-registry branch is only reachable for
	// barrier tenants.)
	_, err := svc.CreateTenant(ctx, "reg-dup", "Reg Dup", IsolationBarrier)
	if err != nil {
		t.Fatalf("first CreateTenant: %v", err)
	}

	// Manually remove the metadata entry so the store thinks the tenant is new
	// but the registry already has it registered.
	if err := svc.store.Delete(ctx, metadataPrefix+"reg-dup"); err != nil {
		t.Fatalf("store.Delete: %v", err)
	}

	// RegisterTenantPooled should return ErrTenantAlreadyExists, which the
	// service maps to our typed error.
	_, err = svc.CreateTenant(ctx, "reg-dup", "Reg Dup Again", IsolationBarrier)
	if !errors.Is(err, ErrTenantAlreadyExists) {
		t.Errorf("expected ErrTenantAlreadyExists from registry duplicate, got %v", err)
	}
}

// TestTenantService_ListTenants_GetErrorAfterList exercises the branch inside
// ListTenants where List succeeds but Get returns a non-NotFound error.
func TestTenantService_ListTenants_GetErrorAfterList(t *testing.T) {
	inner := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, inner)
	ctx := context.Background()

	// Create a real tenant so the inner store has metadata.
	_, err := svc.CreateTenant(ctx, "list-get-err", "List Get Err", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Swap the store: List still works (delegates to inner), Get always fails.
	svc.store = &listOkGetFailBackend{inner: inner}

	_, err = svc.ListTenants(ctx)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError on Get-after-List failure, got %v", err)
	}
}

// TestTenantService_DeleteTenant_UnregisterNotFound exercises the branch where
// UnregisterTenant returns ErrTenantNotFound, which should be silently ignored.
func TestTenantService_DeleteTenant_UnregisterNotFound(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	// Write metadata directly without going through CreateTenant, so the
	// registry has no record of this tenant.
	rawMeta := `{"id":"no-registry","name":"No Registry","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	if err := store.Put(ctx, metadataPrefix+"no-registry", []byte(rawMeta)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	// DeleteTenant should succeed even though the registry has no entry for it
	// (ErrTenantNotFound from UnregisterTenant is silently ignored).
	if err := svc.DeleteTenant(ctx, "no-registry"); err != nil {
		t.Errorf("expected DeleteTenant to succeed when registry lacks tenant, got %v", err)
	}
}

// TestTenantService_ListTenants_SkipsDisappearedKey covers the branch where
// a key that was present during List is not found during the subsequent Get
// (i.e. deleted concurrently). The service should skip it silently.
func TestTenantService_ListTenants_SkipsDisappearedKey(t *testing.T) {
	inner := seal.NewMemoryBackend()
	svc, _ := newTestServiceWithStore(t, inner)
	ctx := context.Background()

	// Create a valid tenant so the inner store has real metadata.
	_, err := svc.CreateTenant(ctx, "visible", "Visible", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Put a phantom key so List returns it, then use a disappearing backend
	// that returns ErrSecretNotFound for that phantom key on Get.
	if err := inner.Put(ctx, metadataPrefix+"phantom", []byte(`{"id":"phantom"}`)); err != nil {
		t.Fatalf("phantom Put: %v", err)
	}

	svc.store = &disappearingBackend{inner: inner, disappearKey: metadataPrefix + "phantom"}

	tenants, err := svc.ListTenants(ctx)
	if err != nil {
		t.Fatalf("ListTenants: unexpected error: %v", err)
	}
	// Only the valid tenant should appear; the phantom is silently skipped.
	if len(tenants) != 1 {
		t.Errorf("expected 1 tenant (phantom skipped), got %d", len(tenants))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// listOkGetFailBackend delegates List to the inner backend but always
// returns an injected error from Get.
type listOkGetFailBackend struct {
	inner seal.StorageBackend
}

func (b *listOkGetFailBackend) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, errStoreFault
}
func (b *listOkGetFailBackend) Put(ctx context.Context, key string, data []byte) error {
	return b.inner.Put(ctx, key, data)
}
func (b *listOkGetFailBackend) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	return b.inner.PutIfAbsent(ctx, key, data)
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (b *listOkGetFailBackend) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}
func (b *listOkGetFailBackend) Delete(ctx context.Context, key string) error {
	return b.inner.Delete(ctx, key)
}
func (b *listOkGetFailBackend) List(ctx context.Context, prefix string) ([]string, error) {
	return b.inner.List(ctx, prefix)
}
func (b *listOkGetFailBackend) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return b.inner.Scan(ctx, prefix, fn)
}
func (b *listOkGetFailBackend) Exists(ctx context.Context, key string) (bool, error) {
	return b.inner.Exists(ctx, key)
}
func (b *listOkGetFailBackend) Close() error { return nil }

// disappearingBackend returns seal.ErrSecretNotFound for the configured key
// on Get, simulating a key that disappeared between List and Get. All other
// operations delegate to the inner backend.
type disappearingBackend struct {
	inner        seal.StorageBackend
	disappearKey string
}

func (b *disappearingBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if key == b.disappearKey {
		return nil, seal.ErrSecretNotFound
	}
	return b.inner.Get(ctx, key)
}
func (b *disappearingBackend) Put(ctx context.Context, key string, data []byte) error {
	return b.inner.Put(ctx, key, data)
}
func (b *disappearingBackend) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	return b.inner.PutIfAbsent(ctx, key, data)
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (b *disappearingBackend) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}
func (b *disappearingBackend) Delete(ctx context.Context, key string) error {
	return b.inner.Delete(ctx, key)
}
func (b *disappearingBackend) List(ctx context.Context, prefix string) ([]string, error) {
	return b.inner.List(ctx, prefix)
}
func (b *disappearingBackend) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return b.inner.Scan(ctx, prefix, fn)
}
func (b *disappearingBackend) Exists(ctx context.Context, key string) (bool, error) {
	return b.inner.Exists(ctx, key)
}
func (b *disappearingBackend) Close() error { return nil }
