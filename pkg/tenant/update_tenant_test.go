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

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
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

func TestIncrementQuotaUsage_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-inc", "Increment Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Set an initial quota with a limit.
	info, err := svc.UpdateQuotas(ctx, "t-inc", &quota.Quotas{MaxKeys: 100})
	if err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil TenantInfo from UpdateQuotas")
	}

	updated, err := svc.IncrementQuotaUsage(ctx, "t-inc", quota.ResourceKeys, 5)
	if err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}
	if updated.Quotas.CurrentKeys != 5 {
		t.Errorf("CurrentKeys = %d, want 5", updated.Quotas.CurrentKeys)
	}

	// Increment again.
	updated, err = svc.IncrementQuotaUsage(ctx, "t-inc", quota.ResourceKeys, 3)
	if err != nil {
		t.Fatalf("IncrementQuotaUsage second call: %v", err)
	}
	if updated.Quotas.CurrentKeys != 8 {
		t.Errorf("CurrentKeys after second increment = %d, want 8", updated.Quotas.CurrentKeys)
	}
}

func TestIncrementQuotaUsage_ExceedsLimit(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-exceed", "Exceed Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "t-exceed", &quota.Quotas{MaxKeys: 10}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	// Increment by exactly the limit — should succeed.
	if _, err := svc.IncrementQuotaUsage(ctx, "t-exceed", quota.ResourceKeys, 10); err != nil {
		t.Fatalf("IncrementQuotaUsage at limit: %v", err)
	}

	// One more unit should fail with QuotaExceededError.
	_, err = svc.IncrementQuotaUsage(ctx, "t-exceed", quota.ResourceKeys, 1)
	if err == nil {
		t.Fatal("expected QuotaExceededError, got nil")
	}
	var qe *QuotaExceededError
	if !errors.As(err, &qe) {
		t.Errorf("error type = %T, want *QuotaExceededError", err)
	}
	if qe.TenantID != "t-exceed" {
		t.Errorf("QuotaExceededError.TenantID = %q, want %q", qe.TenantID, "t-exceed")
	}
}

func TestIncrementQuotaUsage_UnlimitedLimit(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-unlimited", "Unlimited Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// MaxKeys = -1 means unlimited.
	if _, err := svc.UpdateQuotas(ctx, "t-unlimited", &quota.Quotas{MaxKeys: -1}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	for i := range 1000 {
		if _, err := svc.IncrementQuotaUsage(ctx, "t-unlimited", quota.ResourceKeys, 1); err != nil {
			t.Fatalf("IncrementQuotaUsage iteration %d: %v", i, err)
		}
	}
}

func TestIncrementQuotaUsage_ZeroDelta(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-zerodelta", "Zero Delta Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	_, err = svc.IncrementQuotaUsage(ctx, "t-zerodelta", quota.ResourceKeys, 0)
	if err == nil {
		t.Fatal("expected error for zero delta")
	}
}

func TestIncrementQuotaUsage_EmptyTenantID(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.IncrementQuotaUsage(context.Background(), "", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Quota usage: DecrementQuotaUsage
// ---------------------------------------------------------------------------

func TestDecrementQuotaUsage_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-dec", "Decrement Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "t-dec", quota.ResourceUsers, 10); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}
	updated, err := svc.DecrementQuotaUsage(ctx, "t-dec", quota.ResourceUsers, 4)
	if err != nil {
		t.Fatalf("DecrementQuotaUsage: %v", err)
	}
	if updated.Quotas.CurrentUsers != 6 {
		t.Errorf("CurrentUsers = %d, want 6", updated.Quotas.CurrentUsers)
	}
}

func TestDecrementQuotaUsage_ClampToZero(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-clamp", "Clamp Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "t-clamp", quota.ResourceDatabases, 3); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}
	// Decrement by more than current — should clamp to 0.
	updated, err := svc.DecrementQuotaUsage(ctx, "t-clamp", quota.ResourceDatabases, 100)
	if err != nil {
		t.Fatalf("DecrementQuotaUsage: %v", err)
	}
	if updated.Quotas.CurrentDatabases != 0 {
		t.Errorf("CurrentDatabases = %d after clamp, want 0", updated.Quotas.CurrentDatabases)
	}
}

func TestDecrementQuotaUsage_ZeroDelta(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-zdec", "Zero Dec Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	_, err = svc.DecrementQuotaUsage(ctx, "t-zdec", quota.ResourceKeys, 0)
	if err == nil {
		t.Fatal("expected error for zero delta")
	}
}

func TestDecrementQuotaUsage_EmptyTenantID(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.DecrementQuotaUsage(context.Background(), "", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Quota usage: GetQuotaUsage
// ---------------------------------------------------------------------------

func TestGetQuotaUsage_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-gq", "GetQuota Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "t-gq", quota.ResourceRoles, 7); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}
	usage, err := svc.GetQuotaUsage(ctx, "t-gq", quota.ResourceRoles)
	if err != nil {
		t.Fatalf("GetQuotaUsage: %v", err)
	}
	if usage != 7 {
		t.Errorf("GetQuotaUsage = %d, want 7", usage)
	}
}

func TestGetQuotaUsage_NoQuotasSetReturnsZero(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.CreateTenant(ctx, "t-nq", "No Quota Test", "")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	usage, err := svc.GetQuotaUsage(ctx, "t-nq", quota.ResourceBackups)
	if err != nil {
		t.Fatalf("GetQuotaUsage with no quotas: %v", err)
	}
	if usage != 0 {
		t.Errorf("GetQuotaUsage with no quotas = %d, want 0", usage)
	}
}

func TestGetQuotaUsage_EmptyTenantID(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.GetQuotaUsage(context.Background(), "", quota.ResourceKeys)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestGetQuotaUsage_TenantNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.GetQuotaUsage(context.Background(), "nonexistent", quota.ResourceKeys)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}
