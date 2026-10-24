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

package tenant

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// quotaUsageSetter is SetQuotaUsage, which both services implement outside
// TenantServicer.
type quotaUsageSetter interface {
	SetQuotaUsage(ctx context.Context, tenantID string, resource quota.Resource, value int64) (*TenantInfo, error)
}

var (
	_ quotaUsageSetter = (*TenantService)(nil)
	_ quotaUsageSetter = (*kvTenantService)(nil)
)

// recordWrites are the tenant record writes other than UpdateTenant, each of
// which rewrites the whole record and so must commit against the record it
// read.
var recordWrites = map[string]func(ctx context.Context, svc TenantServicer) (*TenantInfo, error){
	"UpdateQuotas": func(ctx context.Context, svc TenantServicer) (*TenantInfo, error) {
		return svc.UpdateQuotas(ctx, "acme", &quota.Quotas{MaxKeys: 100})
	},
	"UpdateEntitlements": func(ctx context.Context, svc TenantServicer) (*TenantInfo, error) {
		return svc.UpdateEntitlements(ctx, "acme", &quota.Entitlements{})
	},
	"IncrementQuotaUsage": func(ctx context.Context, svc TenantServicer) (*TenantInfo, error) {
		return svc.IncrementQuotaUsage(ctx, "acme", quota.ResourceKeys, 2)
	},
	"DecrementQuotaUsage": func(ctx context.Context, svc TenantServicer) (*TenantInfo, error) {
		return svc.DecrementQuotaUsage(ctx, "acme", quota.ResourceKeys, 1)
	},
	"SetQuotaUsage": func(ctx context.Context, svc TenantServicer) (*TenantInfo, error) {
		return svc.(quotaUsageSetter).SetQuotaUsage(ctx, "acme", quota.ResourceKeys, 7)
	},
}

func TestRecordWrites_KeepAConcurrentSuspension(t *testing.T) {
	for write, do := range recordWrites {
		for name, c := range racedServices(t) {
			t.Run(write+"/"+name, func(t *testing.T) {
				ctx := context.Background()
				c.commitStatus(t, StatusSuspended)

				got, err := do(ctx, c.svc)
				if err != nil {
					t.Fatalf("%s: %v", write, err)
				}
				if got.Status != StatusSuspended {
					t.Fatalf("%s returned status %s, want suspended", write, got.Status)
				}
				stored, err := c.svc.GetTenant(ctx, "acme")
				if err != nil {
					t.Fatalf("GetTenant: %v", err)
				}
				if stored.Status != StatusSuspended {
					t.Fatalf("stored status %s after %s, want suspended", stored.Status, write)
				}
				want := statusChange{tenantID: "acme", from: StatusActive, to: StatusSuspended, settled: 1}
				if len(c.auditor.changes) != 1 || *c.auditor.changes[0] != want {
					t.Fatalf("audited %v, want only the suspension", c.auditor.changes)
				}
			})
		}
	}
}

func TestIncrementQuotaUsage_LimitCheckedAgainstCommittedUsage(t *testing.T) {
	for name, c := range racedServices(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := c.svc.UpdateQuotas(ctx, "acme", &quota.Quotas{MaxKeys: 1}); err != nil {
				t.Fatalf("UpdateQuotas: %v", err)
			}
			c.race.write = func() {
				if _, err := c.svc.IncrementQuotaUsage(ctx, "acme", quota.ResourceKeys, 1); err != nil {
					t.Errorf("racing IncrementQuotaUsage: %v", err)
				}
			}

			_, err := c.svc.IncrementQuotaUsage(ctx, "acme", quota.ResourceKeys, 1)
			var exceeded *QuotaExceededError
			if !errors.As(err, &exceeded) {
				t.Fatalf("IncrementQuotaUsage err = %v, want *QuotaExceededError", err)
			}
			if exceeded.Current != 1 || exceeded.Limit != 1 {
				t.Fatalf("exceeded = %+v, want current 1 of limit 1", exceeded)
			}
			usage, err := c.svc.GetQuotaUsage(ctx, "acme", quota.ResourceKeys)
			if err != nil {
				t.Fatalf("GetQuotaUsage: %v", err)
			}
			if usage != 1 {
				t.Fatalf("usage = %d, want 1: only the racing increment fits", usage)
			}
		})
	}
}

func TestRecordWrites_StoreWithoutSwapRefused(t *testing.T) {
	ctx := context.Background()
	unguarded := unguardedSealStore{seal.NewMemoryBackend()}
	unguardedSvc, _ := newTestServiceWithStore(t, unguarded)
	if _, err := unguardedSvc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	kvInner := newMemKVStore()
	kvSvc, err := NewKVTenantService(kvInner, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	if _, err := kvSvc.CreateTenant(ctx, "acme", "Acme", IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	services := map[string]TenantServicer{
		"barrier-unguarded": unguardedSvc,
		"barrier-plain":     &TenantService{store: plainSealView{unguarded}, logger: slog.Default()},
		"kv-plain":          &kvTenantService{store: plainKVStoreView{kvInner}, logger: slog.Default()},
	}
	for write, do := range recordWrites {
		for name, svc := range services {
			t.Run(write+"/"+name, func(t *testing.T) {
				if _, err := do(ctx, svc); !errors.Is(err, ErrTenantUpdateNotAtomic) {
					t.Fatalf("%s = %v, want ErrTenantUpdateNotAtomic", write, err)
				}
			})
		}
	}
}
