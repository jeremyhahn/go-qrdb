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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newTestAccountant builds a quota.Accountant backed by an in-memory persisted
// counter keyed by tenant. The returned map is the durable CurrentStorageBytes.
func newTestAccountant(t *testing.T, base map[string]int64) (*quota.Accountant, map[string]int64) {
	t.Helper()
	persisted := map[string]int64{}
	for k, v := range base {
		persisted[k] = v
	}
	acct, err := quota.NewAccountant(quota.AccountantConfig{
		Quotas: func(_ context.Context, tenantID string) (*quota.Quotas, error) {
			return &quota.Quotas{CurrentStorageBytes: persisted[tenantID]}, nil
		},
		Flush: func(_ context.Context, tenantID string, delta int64) error {
			persisted[tenantID] += delta
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAccountant: %v", err)
	}
	return acct, persisted
}

func TestQuotaKVService_PutAccountsAndEnforces(t *testing.T) {
	inner := newMockKVService()
	acct, persisted := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 100)
	ctx := context.Background()

	if err := kv.Put(ctx, "a", make([]byte, 60)); err != nil {
		t.Fatalf("put 60: %v", err)
	}
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage = %d, want 60", got)
	}

	// A write that would exceed the 100-byte limit is rejected with ExceededError
	// and never reaches the inner store.
	err := kv.Put(ctx, "b", make([]byte, 50))
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *quota.ExceededError, got %v", err)
	}
	if _, gerr := inner.Get(ctx, "b"); gerr == nil {
		t.Fatal("rejected write must not be committed to the inner store")
	}
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage after rejection = %d, want 60", got)
	}

	// A write that fits succeeds.
	if err := kv.Put(ctx, "c", make([]byte, 40)); err != nil {
		t.Fatalf("put 40 (to limit): %v", err)
	}
	acct.Flush(ctx)
	if got := persisted["t1"]; got != 100 {
		t.Fatalf("persisted = %d, want 100", got)
	}
}

func TestQuotaKVService_DeleteSubtracts(t *testing.T) {
	inner := newMockKVService()
	acct, persisted := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := kv.Put(ctx, "a", make([]byte, 200)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := acct.Usage("t1"); got != 200 {
		t.Fatalf("usage = %d, want 200", got)
	}
	if err := kv.Delete(ctx, "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after delete = %d, want 0", got)
	}
	acct.Flush(ctx)
	if got := persisted["t1"]; got != 0 {
		t.Fatalf("persisted = %d, want 0", got)
	}
}

// staleLaggingKVService models a store whose local (stale) read lags the
// linearizable read: the just-committed value is visible via Get but not yet via
// GetStale. It is the failure mode behind finding M-2 case 2 — a delete that
// sizes the freed bytes from a stale read frees zero and inflates usage forever.
type staleLaggingKVService struct {
	*mockKVService
}

func (s *staleLaggingKVService) GetStale(_ context.Context, key string) ([]byte, error) {
	return nil, &service.KeyNotFoundError{Key: key}
}

// TestQuotaKVService_DeleteUsesLinearizableRead asserts the delete path sizes the
// freed bytes via the linearizable Get, so deleting a just-written key whose
// value the stale read cannot yet see still frees the correct amount and leaves
// usage at zero (not inflated).
func TestQuotaKVService_DeleteUsesLinearizableRead(t *testing.T) {
	inner := &staleLaggingKVService{mockKVService: newMockKVService()}
	acct, persisted := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := kv.Put(ctx, "a", make([]byte, 200)); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := acct.Usage("t1"); got != 200 {
		t.Fatalf("usage = %d, want 200", got)
	}
	// With a stale read this delete would free 0 and usage would stay at 200.
	if err := kv.Delete(ctx, "a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage after delete = %d, want 0 (linearizable read frees full value)", got)
	}
	acct.Flush(ctx)
	if got := persisted["t1"]; got != 0 {
		t.Fatalf("persisted = %d, want 0", got)
	}
}

// TestQuotaKVService_OverDeleteNeverNegative pins the effective-usage floor on
// the server path: even when more bytes are subtracted than the base reflects
// (e.g. a small write followed by deletes of larger out-of-band values), usage
// never goes negative.
func TestQuotaKVService_OverDeleteNeverNegative(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()

	if err := kv.Put(ctx, "small", make([]byte, 10)); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Seed a larger value out-of-band (no Reserve accounted it), then delete it:
	// the freed bytes exceed the accounted usage.
	if err := inner.Put(ctx, "big", make([]byte, 500)); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	if err := kv.Delete(ctx, "big"); err != nil {
		t.Fatalf("delete big: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0 (floored, never negative)", got)
	}
}

func TestQuotaKVService_DeleteMissingKeyNoUnderflow(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	kv := newQuotaKVService(inner, acct, "t1", 1000)
	ctx := context.Background()
	if err := kv.Delete(ctx, "absent"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage = %d, want 0", got)
	}
}

func TestApplyQuota_InstallsForLimitedTenant(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	r := &restServiceRegistry{accountant: acct, systemTenant: tenant.SystemTenantID}

	svcs := &DatabaseServices{KV: inner}
	info := &tenant.TenantInfo{ID: "t1", Quotas: &quota.Quotas{MaxStorageBytes: 100}}
	out := r.applyQuota(svcs, "t1", info)

	if _, ok := out.KV.(*quotaKVService); !ok {
		t.Fatalf("expected KV wrapped with *quotaKVService, got %T", out.KV)
	}
}

func TestApplyQuota_SkipsSystemTenant(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	r := &restServiceRegistry{accountant: acct, systemTenant: tenant.SystemTenantID}

	svcs := &DatabaseServices{KV: inner}
	info := &tenant.TenantInfo{ID: tenant.SystemTenantID, Quotas: &quota.Quotas{MaxStorageBytes: 100}}
	out := r.applyQuota(svcs, tenant.SystemTenantID, info)

	if out != svcs {
		t.Fatal("system tenant must not be wrapped (passthrough)")
	}
}

func TestApplyQuota_SkipsUnlimitedTenant(t *testing.T) {
	inner := newMockKVService()
	acct, _ := newTestAccountant(t, nil)
	r := &restServiceRegistry{accountant: acct, systemTenant: tenant.SystemTenantID}

	svcs := &DatabaseServices{KV: inner}
	// MaxStorageBytes <= 0 means unlimited -> no wrapper, hot path untouched.
	for _, limit := range []int64{0, -1} {
		info := &tenant.TenantInfo{ID: "t1", Quotas: &quota.Quotas{MaxStorageBytes: limit}}
		if out := r.applyQuota(svcs, "t1", info); out != svcs {
			t.Fatalf("limit %d: expected passthrough, got wrapped", limit)
		}
	}
}

func TestApplyQuota_SkipsWhenNoAccountant(t *testing.T) {
	inner := newMockKVService()
	r := &restServiceRegistry{systemTenant: tenant.SystemTenantID}
	svcs := &DatabaseServices{KV: inner}
	info := &tenant.TenantInfo{ID: "t1", Quotas: &quota.Quotas{MaxStorageBytes: 100}}
	if out := r.applyQuota(svcs, "t1", info); out != svcs {
		t.Fatal("nil accountant must be passthrough")
	}
}
