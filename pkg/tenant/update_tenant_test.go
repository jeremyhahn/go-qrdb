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
)

// TestUpdateTenant_Success verifies that non-empty patch fields are applied and
// the returned TenantInfo reflects the changes.
func TestUpdateTenant_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-update", "Original Name", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	updated, err := svc.UpdateTenant(ctx, "t-update", TenantPatch{Name: "New Name"})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if updated.Name != "New Name" {
		t.Errorf("expected Name %q, got %q", "New Name", updated.Name)
	}
	if updated.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}

	// Verify the change persists in the store.
	got, err := svc.GetTenant(ctx, "t-update")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if got.Name != "New Name" {
		t.Errorf("persisted Name = %q, want %q", got.Name, "New Name")
	}
}

// TestUpdateTenant_EmptyID returns ErrEmptyTenantID without touching the store.
func TestUpdateTenant_EmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.UpdateTenant(ctx, "", TenantPatch{Name: "x"})
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

// TestUpdateTenant_TenantNotFound returns an error when the tenant does not exist.
func TestUpdateTenant_TenantNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.UpdateTenant(ctx, "nonexistent", TenantPatch{Name: "x"})
	if err == nil {
		t.Fatal("expected error for nonexistent tenant")
	}
}

// TestUpdateTenant_IsolationTypePatch verifies that IsolationType is updated.
func TestUpdateTenant_IsolationTypePatch(t *testing.T) {
	svc, _ := newTestService(t)
	svc.SetTenantDataProbe(&fakeDataProbe{})
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-iso", "ISO Tenant", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	updated, err := svc.UpdateTenant(ctx, "t-iso", TenantPatch{IsolationType: "row"})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if updated.IsolationType != "row" {
		t.Errorf("expected IsolationType %q, got %q", "row", updated.IsolationType)
	}
}

// TestUpdateTenant_EmptyPatch verifies that an empty patch is a no-op (no fields change).
func TestUpdateTenant_EmptyPatch(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	orig, err := svc.CreateTenant(ctx, "t-noop", "No-op Tenant", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	updated, err := svc.UpdateTenant(ctx, "t-noop", TenantPatch{})
	if err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	// Name must remain unchanged because patch.Name is empty.
	if updated.Name != orig.Name {
		t.Errorf("expected name to remain %q, got %q", orig.Name, updated.Name)
	}
}

// TestUpdateTenant_StorePutFails verifies that a store write failure is surfaced
// as a StoreError.
func TestUpdateTenant_StorePutFails(t *testing.T) {
	inner, reg := newTestService(t)
	ctx := context.Background()

	_, err := inner.CreateTenant(ctx, "t-fail", "Fail Tenant", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Wrap the store so Put always fails after the tenant exists.
	failing := &failingBackend{inner: inner.store, failPut: true}
	svc, err := NewTenantService(failing, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	// The store has the tenant (Get works), but Put will fail.
	_, err = svc.UpdateTenant(ctx, "t-fail", TenantPatch{Name: "New"})
	if err == nil {
		t.Fatal("expected error from store Put failure")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Quota usage: IncrementQuotaUsage
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Quota usage: DecrementQuotaUsage
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Quota usage: GetQuotaUsage
// ---------------------------------------------------------------------------
