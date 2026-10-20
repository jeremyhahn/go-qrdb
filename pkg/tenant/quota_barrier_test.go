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
	"log/slog"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// Error type coverage
// ---------------------------------------------------------------------------

func TestQuotaExceededError_Error(t *testing.T) {
	err := &QuotaExceededError{
		TenantID: "acme",
		Resource: "keys",
		Current:  95,
		Delta:    10,
		Limit:    100,
	}
	got := err.Error()
	for _, want := range []string{"acme", "keys", "95", "10", "100"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}
}

func TestQuotaExceededError_ImplementsError(t *testing.T) {
	var _ error = &QuotaExceededError{}
}

func TestInvalidStrategyError_Error_NoAvailable(t *testing.T) {
	err := &InvalidStrategyError{Strategy: "tpm2"}
	got := err.Error()
	if !strings.Contains(got, "tpm2") {
		t.Errorf("Error() = %q, want it to contain %q", got, "tpm2")
	}
	if !strings.Contains(got, "invalid seal strategy") {
		t.Errorf("Error() = %q, want it to contain %q", got, "invalid seal strategy")
	}
}

func TestInvalidStrategyError_Error_WithAvailable(t *testing.T) {
	err := &InvalidStrategyError{
		Strategy:  "tpm2",
		Available: []string{"software", "none", "shamir"},
	}
	got := err.Error()
	if !strings.Contains(got, "tpm2") {
		t.Errorf("Error() = %q, want it to contain %q", got, "tpm2")
	}
	if !strings.Contains(got, "software") {
		t.Errorf("Error() = %q, want it to contain %q", got, "software")
	}
	if !strings.Contains(got, "available") {
		t.Errorf("Error() = %q, want it to contain %q", got, "available")
	}
}

func TestInvalidStrategyError_Error_SingleAvailable(t *testing.T) {
	err := &InvalidStrategyError{
		Strategy:  "pkcs11",
		Available: []string{"none"},
	}
	got := err.Error()
	if !strings.Contains(got, "pkcs11") {
		t.Errorf("Error() = %q, want it to contain %q", got, "pkcs11")
	}
	if !strings.Contains(got, "none") {
		t.Errorf("Error() = %q, want it to contain %q", got, "none")
	}
}

// ---------------------------------------------------------------------------
// TenantService — IncrementQuotaUsage
// ---------------------------------------------------------------------------

func TestTenantService_IncrementQuotaUsage_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "inc-ok", "Inc OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	q := &quota.Quotas{MaxKeys: 100}
	if _, err := svc.UpdateQuotas(ctx, "inc-ok", q); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	info, err := svc.IncrementQuotaUsage(ctx, "inc-ok", quota.ResourceKeys, 5)
	if err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}
	if info.Quotas.CurrentKeys != 5 {
		t.Errorf("CurrentKeys = %d, want 5", info.Quotas.CurrentKeys)
	}

	// Increment again and verify accumulation.
	info, err = svc.IncrementQuotaUsage(ctx, "inc-ok", quota.ResourceKeys, 10)
	if err != nil {
		t.Fatalf("second IncrementQuotaUsage: %v", err)
	}
	if info.Quotas.CurrentKeys != 15 {
		t.Errorf("CurrentKeys = %d, want 15", info.Quotas.CurrentKeys)
	}
}

func TestTenantService_IncrementQuotaUsage_NilQuotas(t *testing.T) {
	// When a tenant has no quotas set, IncrementQuotaUsage allocates a
	// zero Quotas and applies the delta without hitting a limit.
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "inc-nil-q", "Inc Nil Q", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	info, err := svc.IncrementQuotaUsage(ctx, "inc-nil-q", quota.ResourceKeys, 3)
	if err != nil {
		t.Fatalf("IncrementQuotaUsage with nil quotas: %v", err)
	}
	if info.Quotas == nil {
		t.Fatal("expected non-nil quotas after increment")
	}
	if info.Quotas.CurrentKeys != 3 {
		t.Errorf("CurrentKeys = %d, want 3", info.Quotas.CurrentKeys)
	}
}

func TestTenantService_IncrementQuotaUsage_EmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.IncrementQuotaUsage(ctx, "", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_IncrementQuotaUsage_ZeroDelta(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "inc-zero", "Inc Zero", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.IncrementQuotaUsage(ctx, "inc-zero", quota.ResourceKeys, 0)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for zero delta, got %v", err)
	}
}

func TestTenantService_IncrementQuotaUsage_NegativeDelta(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "inc-neg", "Inc Neg", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.IncrementQuotaUsage(ctx, "inc-neg", quota.ResourceKeys, -1)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for negative delta, got %v", err)
	}
}

func TestTenantService_IncrementQuotaUsage_QuotaExceeded(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "inc-exceed", "Inc Exceed", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "inc-exceed", &quota.Quotas{MaxKeys: 10}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	_, err := svc.IncrementQuotaUsage(ctx, "inc-exceed", quota.ResourceKeys, 11)
	var qe *QuotaExceededError
	if !errors.As(err, &qe) {
		t.Fatalf("expected QuotaExceededError, got %v", err)
	}
	if qe.TenantID != "inc-exceed" {
		t.Errorf("TenantID = %q, want %q", qe.TenantID, "inc-exceed")
	}
	if qe.Delta != 11 {
		t.Errorf("Delta = %d, want 11", qe.Delta)
	}
	if qe.Limit != 10 {
		t.Errorf("Limit = %d, want 10", qe.Limit)
	}
}

func TestTenantService_IncrementQuotaUsage_AtLimit(t *testing.T) {
	// Incrementing exactly to the limit succeeds; exceeding by 1 fails.
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "inc-at-limit", "Inc At Limit", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "inc-at-limit", &quota.Quotas{MaxKeys: 5}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	// Exactly at limit — should succeed.
	if _, err := svc.IncrementQuotaUsage(ctx, "inc-at-limit", quota.ResourceKeys, 5); err != nil {
		t.Fatalf("IncrementQuotaUsage to limit: %v", err)
	}

	// One more — must exceed.
	_, err := svc.IncrementQuotaUsage(ctx, "inc-at-limit", quota.ResourceKeys, 1)
	var qe *QuotaExceededError
	if !errors.As(err, &qe) {
		t.Fatalf("expected QuotaExceededError when over limit, got %v", err)
	}
}

func TestTenantService_IncrementQuotaUsage_UnlimitedLimit(t *testing.T) {
	// MaxKeys == -1 means unlimited: no QuotaExceededError regardless of delta.
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "inc-unlimit", "Inc Unlimited", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "inc-unlimit", &quota.Quotas{MaxKeys: -1}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	if _, err := svc.IncrementQuotaUsage(ctx, "inc-unlimit", quota.ResourceKeys, 1_000_000); err != nil {
		t.Fatalf("IncrementQuotaUsage with unlimited quota: %v", err)
	}
}

func TestTenantService_IncrementQuotaUsage_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.IncrementQuotaUsage(ctx, "ghost", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_IncrementQuotaUsage_StorePutFault(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "inc-putfault", "Inc PutFault", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	fb := &failingBackend{inner: store, failPut: true}
	svc.store = fb

	_, err = svc.IncrementQuotaUsage(ctx, "inc-putfault", quota.ResourceKeys, 1)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError from put fault, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantService — DecrementQuotaUsage
// ---------------------------------------------------------------------------

func TestTenantService_SetQuotaUsage_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "set-ok", "Set OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "set-ok", &quota.Quotas{MaxStorageBytes: 100}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	info, err := svc.SetQuotaUsage(ctx, "set-ok", quota.ResourceStorageBytes, 73)
	if err != nil {
		t.Fatalf("SetQuotaUsage: %v", err)
	}
	if info.Quotas.CurrentStorageBytes != 73 {
		t.Errorf("CurrentStorageBytes = %d, want 73", info.Quotas.CurrentStorageBytes)
	}
}

func TestTenantService_SetQuotaUsage_BypassesLimitAndClamps(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateTenant(ctx, "set-over", "Set Over", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "set-over", &quota.Quotas{MaxStorageBytes: 10}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	// SetQuotaUsage records true usage even above the limit (no enforcement).
	info, err := svc.SetQuotaUsage(ctx, "set-over", quota.ResourceStorageBytes, 999)
	if err != nil {
		t.Fatalf("SetQuotaUsage over limit: %v", err)
	}
	if info.Quotas.CurrentStorageBytes != 999 {
		t.Errorf("CurrentStorageBytes = %d, want 999", info.Quotas.CurrentStorageBytes)
	}
	// Negative values clamp to zero.
	info, err = svc.SetQuotaUsage(ctx, "set-over", quota.ResourceStorageBytes, -1)
	if err != nil {
		t.Fatalf("SetQuotaUsage negative: %v", err)
	}
	if info.Quotas.CurrentStorageBytes != 0 {
		t.Errorf("CurrentStorageBytes = %d, want 0 (clamped)", info.Quotas.CurrentStorageBytes)
	}
}

func TestTenantService_SetQuotaUsage_EmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.SetQuotaUsage(context.Background(), "", quota.ResourceStorageBytes, 1); !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestKVSetQuotaUsage_Success(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t).(*kvTenantService)
	if _, err := svc.CreateTenant(ctx, "kv-set-ok", "KV Set OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	info, err := svc.SetQuotaUsage(ctx, "kv-set-ok", quota.ResourceStorageBytes, 256)
	if err != nil {
		t.Fatalf("SetQuotaUsage: %v", err)
	}
	if info.Quotas.CurrentStorageBytes != 256 {
		t.Errorf("CurrentStorageBytes = %d, want 256", info.Quotas.CurrentStorageBytes)
	}
}

func TestKVSetQuotaUsage_EmptyID(t *testing.T) {
	svc := newKVService(t).(*kvTenantService)
	if _, err := svc.SetQuotaUsage(context.Background(), "", quota.ResourceStorageBytes, 1); !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_DecrementQuotaUsage_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "dec-ok", "Dec OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "dec-ok", &quota.Quotas{MaxKeys: 100}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "dec-ok", quota.ResourceKeys, 20); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}

	info, err := svc.DecrementQuotaUsage(ctx, "dec-ok", quota.ResourceKeys, 5)
	if err != nil {
		t.Fatalf("DecrementQuotaUsage: %v", err)
	}
	if info.Quotas.CurrentKeys != 15 {
		t.Errorf("CurrentKeys = %d, want 15", info.Quotas.CurrentKeys)
	}
}

func TestTenantService_DecrementQuotaUsage_ClampToZero(t *testing.T) {
	// Decrementing more than the current usage clamps to zero.
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "dec-clamp", "Dec Clamp", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "dec-clamp", &quota.Quotas{MaxKeys: 100}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "dec-clamp", quota.ResourceKeys, 3); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}

	info, err := svc.DecrementQuotaUsage(ctx, "dec-clamp", quota.ResourceKeys, 100)
	if err != nil {
		t.Fatalf("DecrementQuotaUsage with clamp: %v", err)
	}
	if info.Quotas.CurrentKeys != 0 {
		t.Errorf("CurrentKeys = %d, want 0 (clamped)", info.Quotas.CurrentKeys)
	}
}

func TestTenantService_DecrementQuotaUsage_NilQuotas(t *testing.T) {
	// When a tenant has no quotas, decrement allocates a zero Quotas and
	// clamps to zero — no error.
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "dec-nil-q", "Dec Nil Q", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	info, err := svc.DecrementQuotaUsage(ctx, "dec-nil-q", quota.ResourceKeys, 5)
	if err != nil {
		t.Fatalf("DecrementQuotaUsage with nil quotas: %v", err)
	}
	if info.Quotas.CurrentKeys != 0 {
		t.Errorf("CurrentKeys = %d, want 0", info.Quotas.CurrentKeys)
	}
}

func TestTenantService_DecrementQuotaUsage_EmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.DecrementQuotaUsage(ctx, "", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_DecrementQuotaUsage_ZeroDelta(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "dec-zero", "Dec Zero", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.DecrementQuotaUsage(ctx, "dec-zero", quota.ResourceKeys, 0)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for zero delta, got %v", err)
	}
}

func TestTenantService_DecrementQuotaUsage_NegativeDelta(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "dec-neg", "Dec Neg", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.DecrementQuotaUsage(ctx, "dec-neg", quota.ResourceKeys, -3)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for negative delta, got %v", err)
	}
}

func TestTenantService_DecrementQuotaUsage_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.DecrementQuotaUsage(ctx, "ghost", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_DecrementQuotaUsage_StorePutFault(t *testing.T) {
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "dec-putfault", "Dec PutFault", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	fb := &failingBackend{inner: store, failPut: true}
	svc.store = fb

	_, err = svc.DecrementQuotaUsage(ctx, "dec-putfault", quota.ResourceKeys, 1)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError from put fault, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantService — GetQuotaUsage
// ---------------------------------------------------------------------------

func TestTenantService_GetQuotaUsage_Success(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "getq-ok", "GetQ OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "getq-ok", &quota.Quotas{MaxKeys: 100}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "getq-ok", quota.ResourceKeys, 7); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}

	usage, err := svc.GetQuotaUsage(ctx, "getq-ok", quota.ResourceKeys)
	if err != nil {
		t.Fatalf("GetQuotaUsage: %v", err)
	}
	if usage != 7 {
		t.Errorf("usage = %d, want 7", usage)
	}
}

func TestTenantService_GetQuotaUsage_NilQuotas(t *testing.T) {
	// A tenant with no quotas configured returns 0 for any resource.
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "getq-nil", "GetQ Nil", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	usage, err := svc.GetQuotaUsage(ctx, "getq-nil", quota.ResourceKeys)
	if err != nil {
		t.Fatalf("GetQuotaUsage with nil quotas: %v", err)
	}
	if usage != 0 {
		t.Errorf("usage = %d, want 0", usage)
	}
}

func TestTenantService_GetQuotaUsage_EmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetQuotaUsage(ctx, "", quota.ResourceKeys)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_GetQuotaUsage_NotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.GetQuotaUsage(ctx, "ghost", quota.ResourceKeys)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantService — TenantBarrierSeal
// ---------------------------------------------------------------------------

func TestTenantService_BarrierSeal_Success(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "seal-ok", "Seal OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInit(ctx, "seal-ok", []byte("seal-ok-pw"), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}

	// Verify unsealed after init.
	st, err := reg.TenantStatus("seal-ok")
	if err != nil {
		t.Fatalf("TenantStatus: %v", err)
	}
	if st.Sealed {
		t.Fatal("expected unsealed after init")
	}

	// Seal via service.
	if err := svc.TenantBarrierSeal(WithTenantID(ctx, "seal-ok"), "seal-ok"); err != nil {
		t.Fatalf("TenantBarrierSeal: %v", err)
	}

	st, err = reg.TenantStatus("seal-ok")
	if err != nil {
		t.Fatalf("TenantStatus after seal: %v", err)
	}
	if !st.Sealed {
		t.Fatal("expected sealed after TenantBarrierSeal")
	}
}

func TestTenantService_BarrierSeal_EmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.TenantBarrierSeal(WithTenantID(ctx, ""), "")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_BarrierSeal_TenantNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	err := svc.TenantBarrierSeal(WithTenantID(ctx, "ghost"), "ghost")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestTenantService_BarrierSeal_BarrierNotRegistered(t *testing.T) {
	// The tenant exists in metadata but its barrier is not registered in the
	// registry (e.g. barrier init was never called). SealTenant returns
	// ErrTenantNotFound from the registry, which surfaces as a BarrierError.
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	// Write metadata directly without registering a barrier.
	rawMeta := `{"id":"no-barrier","name":"No Barrier","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	if err := store.Put(ctx, metadataPrefix+"no-barrier", []byte(rawMeta)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	err = svc.TenantBarrierSeal(WithTenantID(ctx, "no-barrier"), "no-barrier")
	var be *BarrierError
	if !errors.As(err, &be) {
		t.Errorf("expected BarrierError when barrier not registered, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TenantService — TenantBarrierStatus
// ---------------------------------------------------------------------------

func TestTenantService_BarrierStatus_Initialized(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "status-init", "Status Init", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInit(ctx, "status-init", []byte("status-init-pw"), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}

	st, err := svc.TenantBarrierStatus(ctx, "status-init")
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if st.TenantID != "status-init" {
		t.Errorf("TenantID = %q, want %q", st.TenantID, "status-init")
	}
	if !st.Initialized {
		t.Error("expected Initialized = true")
	}
	if st.Sealed {
		t.Error("expected Sealed = false after init")
	}
	if st.Strategy == "" {
		t.Error("expected non-empty Strategy")
	}
	// An unsealed keyed barrier names its key by a 16-byte check value.
	if len(st.KeyFingerprint) != 32 {
		t.Errorf("KeyFingerprint = %q, want 32 hex characters", st.KeyFingerprint)
	}
}

func TestTenantService_BarrierStatus_NotRegistered(t *testing.T) {
	// Tenant exists in metadata but barrier was never registered.
	// Status should report Initialized=false, Sealed=true.
	store := seal.NewMemoryBackend()
	reg := newTestBarrierRegistry(t)
	svc, err := NewTenantService(store, reg, testLogger())
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	ctx := context.Background()

	rawMeta := `{"id":"status-unreg","name":"Status Unreg","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`
	if err := store.Put(ctx, metadataPrefix+"status-unreg", []byte(rawMeta)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	st, err := svc.TenantBarrierStatus(ctx, "status-unreg")
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if st.TenantID != "status-unreg" {
		t.Errorf("TenantID = %q, want %q", st.TenantID, "status-unreg")
	}
	if st.Initialized {
		t.Error("expected Initialized = false for never-registered barrier")
	}
	if !st.Sealed {
		t.Error("expected Sealed = true for never-registered barrier")
	}
}

func TestTenantService_BarrierStatus_Sealed(t *testing.T) {
	svc, reg := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "status-sealed", "Status Sealed", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.TenantBarrierInit(ctx, "status-sealed", []byte("status-sealed-pw"), 0, 0); err != nil {
		t.Fatalf("TenantBarrierInit: %v", err)
	}
	if err := reg.SealTenant("status-sealed"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}

	st, err := svc.TenantBarrierStatus(ctx, "status-sealed")
	if err != nil {
		t.Fatalf("TenantBarrierStatus: %v", err)
	}
	if !st.Sealed {
		t.Error("expected Sealed = true after seal")
	}
	if !st.Initialized {
		t.Error("expected Initialized = true")
	}
	if st.KeyFingerprint != "" {
		t.Errorf("KeyFingerprint = %q for a sealed barrier, want none", st.KeyFingerprint)
	}
}

func TestTenantService_BarrierStatus_EmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.TenantBarrierStatus(ctx, "")
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestTenantService_BarrierStatus_TenantNotFound(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.TenantBarrierStatus(ctx, "ghost")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// kvTenantService — IncrementQuotaUsage
// ---------------------------------------------------------------------------

func TestKVIncrementQuotaUsage_Success(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-inc-ok", "KV Inc OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "kv-inc-ok", &quota.Quotas{MaxKeys: 100}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	info, err := svc.IncrementQuotaUsage(ctx, "kv-inc-ok", quota.ResourceKeys, 5)
	if err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}
	if info.Quotas.CurrentKeys != 5 {
		t.Errorf("CurrentKeys = %d, want 5", info.Quotas.CurrentKeys)
	}
}

func TestKVIncrementQuotaUsage_EmptyID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.IncrementQuotaUsage(ctx, "", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestKVIncrementQuotaUsage_ZeroDelta(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-inc-zero", "KV Inc Zero", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.IncrementQuotaUsage(ctx, "kv-inc-zero", quota.ResourceKeys, 0)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for zero delta, got %v", err)
	}
}

func TestKVIncrementQuotaUsage_NegativeDelta(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-inc-neg", "KV Inc Neg", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.IncrementQuotaUsage(ctx, "kv-inc-neg", quota.ResourceKeys, -1)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for negative delta, got %v", err)
	}
}

func TestKVIncrementQuotaUsage_NotFound(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.IncrementQuotaUsage(ctx, "ghost", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestKVIncrementQuotaUsage_QuotaExceeded(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-inc-exc", "KV Inc Exc", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "kv-inc-exc", &quota.Quotas{MaxKeys: 10}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}

	_, err := svc.IncrementQuotaUsage(ctx, "kv-inc-exc", quota.ResourceKeys, 11)
	var qe *QuotaExceededError
	if !errors.As(err, &qe) {
		t.Fatalf("expected QuotaExceededError, got %v", err)
	}
}

func TestKVIncrementQuotaUsage_NilQuotas(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-inc-nilq", "KV Inc NilQ", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	info, err := svc.IncrementQuotaUsage(ctx, "kv-inc-nilq", quota.ResourceKeys, 3)
	if err != nil {
		t.Fatalf("IncrementQuotaUsage with nil quotas: %v", err)
	}
	if info.Quotas.CurrentKeys != 3 {
		t.Errorf("CurrentKeys = %d, want 3", info.Quotas.CurrentKeys)
	}
}

func TestKVIncrementQuotaUsage_StorePutFault(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "kv-inc-putfault", "KV Inc PutFault", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Inject fault after create so Get succeeds but Put fails.
	store.err = errKVFault

	_, err = svc.IncrementQuotaUsage(ctx, "kv-inc-putfault", quota.ResourceKeys, 1)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// kvTenantService — DecrementQuotaUsage
// ---------------------------------------------------------------------------

func TestKVDecrementQuotaUsage_Success(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-dec-ok", "KV Dec OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "kv-dec-ok", &quota.Quotas{MaxKeys: 100}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "kv-dec-ok", quota.ResourceKeys, 20); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}

	info, err := svc.DecrementQuotaUsage(ctx, "kv-dec-ok", quota.ResourceKeys, 5)
	if err != nil {
		t.Fatalf("DecrementQuotaUsage: %v", err)
	}
	if info.Quotas.CurrentKeys != 15 {
		t.Errorf("CurrentKeys = %d, want 15", info.Quotas.CurrentKeys)
	}
}

func TestKVDecrementQuotaUsage_ClampToZero(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-dec-clamp", "KV Dec Clamp", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "kv-dec-clamp", &quota.Quotas{MaxKeys: 100}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "kv-dec-clamp", quota.ResourceKeys, 3); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}

	info, err := svc.DecrementQuotaUsage(ctx, "kv-dec-clamp", quota.ResourceKeys, 999)
	if err != nil {
		t.Fatalf("DecrementQuotaUsage clamped: %v", err)
	}
	if info.Quotas.CurrentKeys != 0 {
		t.Errorf("CurrentKeys = %d, want 0 (clamped)", info.Quotas.CurrentKeys)
	}
}

func TestKVDecrementQuotaUsage_EmptyID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.DecrementQuotaUsage(ctx, "", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestKVDecrementQuotaUsage_ZeroDelta(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-dec-zero", "KV Dec Zero", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.DecrementQuotaUsage(ctx, "kv-dec-zero", quota.ResourceKeys, 0)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for zero delta, got %v", err)
	}
}

func TestKVDecrementQuotaUsage_NegativeDelta(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-dec-neg", "KV Dec Neg", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	_, err := svc.DecrementQuotaUsage(ctx, "kv-dec-neg", quota.ResourceKeys, -2)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for negative delta, got %v", err)
	}
}

func TestKVDecrementQuotaUsage_NotFound(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.DecrementQuotaUsage(ctx, "ghost", quota.ResourceKeys, 1)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestKVDecrementQuotaUsage_NilQuotas(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-dec-nilq", "KV Dec NilQ", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	info, err := svc.DecrementQuotaUsage(ctx, "kv-dec-nilq", quota.ResourceKeys, 1)
	if err != nil {
		t.Fatalf("DecrementQuotaUsage with nil quotas: %v", err)
	}
	if info.Quotas.CurrentKeys != 0 {
		t.Errorf("CurrentKeys = %d, want 0", info.Quotas.CurrentKeys)
	}
}

func TestKVDecrementQuotaUsage_StorePutFault(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "kv-dec-putfault", "KV Dec PutFault", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	store.err = errKVFault

	_, err = svc.DecrementQuotaUsage(ctx, "kv-dec-putfault", quota.ResourceKeys, 1)
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// kvTenantService — GetQuotaUsage
// ---------------------------------------------------------------------------

func TestKVGetQuotaUsage_Success(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-getq-ok", "KV GetQ OK", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := svc.UpdateQuotas(ctx, "kv-getq-ok", &quota.Quotas{MaxUsers: 50}); err != nil {
		t.Fatalf("UpdateQuotas: %v", err)
	}
	if _, err := svc.IncrementQuotaUsage(ctx, "kv-getq-ok", quota.ResourceUsers, 4); err != nil {
		t.Fatalf("IncrementQuotaUsage: %v", err)
	}

	usage, err := svc.GetQuotaUsage(ctx, "kv-getq-ok", quota.ResourceUsers)
	if err != nil {
		t.Fatalf("GetQuotaUsage: %v", err)
	}
	if usage != 4 {
		t.Errorf("usage = %d, want 4", usage)
	}
}

func TestKVGetQuotaUsage_NilQuotas(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	if _, err := svc.CreateTenant(ctx, "kv-getq-nil", "KV GetQ Nil", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	usage, err := svc.GetQuotaUsage(ctx, "kv-getq-nil", quota.ResourceUsers)
	if err != nil {
		t.Fatalf("GetQuotaUsage with nil quotas: %v", err)
	}
	if usage != 0 {
		t.Errorf("usage = %d, want 0", usage)
	}
}

func TestKVGetQuotaUsage_EmptyID(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.GetQuotaUsage(ctx, "", quota.ResourceUsers)
	if !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestKVGetQuotaUsage_NotFound(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.GetQuotaUsage(ctx, "ghost", quota.ResourceUsers)
	if !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("expected ErrTenantNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// kvTenantService — barrier no-op stubs
// ---------------------------------------------------------------------------

func TestKVTenantBarrierInitWithStrategy_NotEnabled(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.TenantBarrierInitWithStrategy(ctx, "acme", []byte("pw"), 0, 0, seal.StrategyPassphrase, nil)
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Fatalf("want ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierSeal_NotEnabled(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	err := svc.TenantBarrierSeal(WithTenantID(ctx, "acme"), "acme")
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Fatalf("want ErrBarrierNotEnabled, got %v", err)
	}
}

func TestKVTenantBarrierStatus_NotEnabled(t *testing.T) {
	ctx := context.Background()
	svc := newKVService(t)

	_, err := svc.TenantBarrierStatus(ctx, "acme")
	if !errors.Is(err, ErrBarrierNotEnabled) {
		t.Fatalf("want ErrBarrierNotEnabled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// kvTenantService — partial coverage gaps in UpdateTenant, GetTenant,
// ListTenants, DeleteTenant, UpdateQuotas
// ---------------------------------------------------------------------------

func TestKVGetTenant_StoreError(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	store.err = errKVFault

	_, err = svc.GetTenant(ctx, "any")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestKVGetTenant_CorruptData(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	if err := store.Put(ctx, metadataPrefix+"corrupt", []byte("not-json")); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	_, err = svc.GetTenant(ctx, "corrupt")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError for corrupt data, got %v", err)
	}
}

func TestKVUpdateTenant_StorePutFault(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "upd-fault", "Upd Fault", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	store.err = errKVFault

	_, err = svc.UpdateTenant(ctx, "upd-fault", TenantPatch{Name: "New Name"})
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestKVListTenants_GetErrorAfterList(t *testing.T) {
	// List returns a key but the subsequent Get returns an error, exercising
	// the "return nil, &StoreError{Op: get}" branch inside ListTenants.
	ctx := context.Background()
	store := newMemKVStore()

	// Populate the inner store with a tenant directly so List returns a key.
	if err := store.Put(ctx, metadataPrefix+"list-getfail", []byte(`{"id":"list-getfail"}`)); err != nil {
		t.Fatalf("store.Put: %v", err)
	}

	// Wrap: List delegates to inner (returns the key), but Get always fails.
	svc, err := NewKVTenantService(&kvListOkGetFail{memKVStore: store}, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	_, listErr := svc.ListTenants(ctx)
	var se *StoreError
	if !errors.As(listErr, &se) {
		t.Errorf("expected StoreError for get-after-list failure, got %v", listErr)
	}
}

func TestKVDeleteTenant_StoreGetError(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	store.err = errKVFault

	err = svc.DeleteTenant(ctx, "any")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestKVDeleteTenant_StoreDeleteFault(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "del-fault", "Del Fault", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	store.err = errKVFault

	err = svc.DeleteTenant(ctx, "del-fault")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

func TestKVUpdateQuotas_StorePutFault(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "upd-quota-fault", "Upd Quota Fault", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	store.err = errKVFault

	_, err = svc.UpdateQuotas(ctx, "upd-quota-fault", &quota.Quotas{MaxKeys: 10})
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Additional ListTenants coverage: disappeared key and corrupt JSON
// ---------------------------------------------------------------------------

func TestKVListTenants_DisappearedKey(t *testing.T) {
	// List returns a key but the subsequent Get returns nil (key disappeared).
	// The service should silently skip the entry.
	ctx := context.Background()
	store := newMemKVStore()

	// Put a valid tenant so List returns it, then a phantom key.
	if err := store.Put(ctx, metadataPrefix+"visible2", []byte(`{"id":"visible2","name":"V"}`)); err != nil {
		t.Fatalf("store.Put visible: %v", err)
	}

	svc, err := NewKVTenantService(&kvGetNilForKey{memKVStore: store, nilKey: metadataPrefix + "visible2"}, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	tenants, listErr := svc.ListTenants(ctx)
	if listErr != nil {
		t.Fatalf("ListTenants: %v", listErr)
	}
	// The key that returned nil is skipped — list should be empty.
	if len(tenants) != 0 {
		t.Errorf("expected 0 tenants (disappeared key skipped), got %d", len(tenants))
	}
}

func TestKVListTenants_CorruptJSON(t *testing.T) {
	// List returns a key whose value is invalid JSON. The service should log
	// a warning and continue rather than returning an error.
	ctx := context.Background()
	store := newMemKVStore()

	if err := store.Put(ctx, metadataPrefix+"good", []byte(`{"id":"good","name":"Good"}`)); err != nil {
		t.Fatalf("store.Put good: %v", err)
	}
	if err := store.Put(ctx, metadataPrefix+"corrupt2", []byte("not-json-at-all")); err != nil {
		t.Fatalf("store.Put corrupt: %v", err)
	}

	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	tenants, listErr := svc.ListTenants(ctx)
	if listErr != nil {
		t.Fatalf("ListTenants returned error: %v", listErr)
	}
	// Corrupt entry is skipped; only the valid one is returned.
	if len(tenants) != 1 {
		t.Errorf("expected 1 tenant (corrupt skipped), got %d", len(tenants))
	}
	if tenants[0].ID != "good" {
		t.Errorf("tenant ID = %q, want %q", tenants[0].ID, "good")
	}
}

// TestKVDeleteTenant_DeleteOpFault exercises the branch where Get succeeds but
// the subsequent Delete call returns an error.
func TestKVDeleteTenant_DeleteOpFault(t *testing.T) {
	ctx := context.Background()
	store := newMemKVStore()
	svc, err := NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	if _, err := svc.CreateTenant(ctx, "del-op-fault", "Del Op Fault", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// Wrap: Get delegates to inner (succeeds), Delete always fails.
	svc2, err2 := NewKVTenantService(&kvGetOkDeleteFail{memKVStore: store}, slog.Default())
	if err2 != nil {
		t.Fatalf("NewKVTenantService wrapped: %v", err2)
	}

	err = svc2.DeleteTenant(ctx, "del-op-fault")
	var se *StoreError
	if !errors.As(err, &se) {
		t.Errorf("expected StoreError from delete fault, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// KV store wrapper helpers
// ---------------------------------------------------------------------------

// kvListOkGetFail wraps a memKVStore and overrides Get to always return an
// error, while delegating all other operations to the inner store.
type kvListOkGetFail struct {
	*memKVStore
}

func (k *kvListOkGetFail) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, errKVFault
}

// kvGetNilForKey wraps a memKVStore and returns nil, nil for a specific key on
// Get, simulating a key that disappeared between List and Get.
type kvGetNilForKey struct {
	*memKVStore
	nilKey string
}

func (k *kvGetNilForKey) Get(ctx context.Context, key string) ([]byte, error) {
	if key == k.nilKey {
		return nil, nil
	}
	return k.memKVStore.Get(ctx, key)
}

// kvGetOkDeleteFail wraps a memKVStore and overrides Delete to always return an
// error, while Get and other operations delegate normally.
type kvGetOkDeleteFail struct {
	*memKVStore
}

func (k *kvGetOkDeleteFail) Delete(_ context.Context, _ string) error {
	return errKVFault
}
