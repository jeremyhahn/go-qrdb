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

package main

import (
	"context"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestBuildQuotaAccountant_FlushDriveIncrementAndDecrement exercises the Flush
// closure wired by buildQuotaAccountant. A positive accumulator delta is routed
// through IncrementQuotaUsage and a negative delta through DecrementQuotaUsage,
// advancing the durable per-tenant base in both directions.
func TestBuildQuotaAccountant_FlushDriveIncrementAndDecrement(t *testing.T) {
	svc, _ := newReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "qacct", "Quota Acct", tenant.IsolationVolume); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	acct := buildQuotaAccountant(svc)
	if acct == nil {
		t.Fatal("expected non-nil accountant")
	}

	// Positive delta: Reserve (no limit) then Flush -> Increment branch.
	if err := acct.Reserve(ctx, "qacct", 0, 100); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	acct.Flush(ctx)
	if got := acct.Usage("qacct"); got != 100 {
		t.Fatalf("usage after increment flush = %d, want 100", got)
	}

	// Negative delta: Sub then Flush -> Decrement branch.
	acct.Sub("qacct", 40)
	acct.Flush(ctx)
	if got := acct.Usage("qacct"); got != 60 {
		t.Fatalf("usage after decrement flush = %d, want 60", got)
	}

	// Confirm the durable tenant metadata reflects the flushed total.
	info, err := svc.GetTenant(ctx, "qacct")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if info.Quotas == nil {
		t.Fatal("expected quotas to be initialized after flush")
	}
	used, err := info.Quotas.CurrentUsage(quota.ResourceStorageBytes)
	if err != nil {
		t.Fatalf("CurrentUsage: %v", err)
	}
	if used != 60 {
		t.Fatalf("durable storage-bytes usage = %d, want 60", used)
	}
}

// TestBuildQuotaReconciler_RunOnceDrivesClosures exercises the Tenants and
// SetUsage closures wired by buildQuotaReconciler. RunOnce enumerates the live
// tenants, walks each volume tenant's data root, and writes the recomputed
// authoritative usage back through SetQuotaUsage.
func TestBuildQuotaReconciler_RunOnceDrivesClosures(t *testing.T) {
	svc, _ := newReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "qrec", "Quota Rec", tenant.IsolationVolume); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	acct := buildQuotaAccountant(svc)
	if acct == nil {
		t.Fatal("expected non-nil accountant")
	}
	rec := buildQuotaReconciler(acct, svc, qrdbconfig.TenantConfig{})
	if rec == nil {
		t.Fatal("expected non-nil reconciler")
	}

	// A single synchronous cycle flushes the accountant, enumerates tenants
	// (Tenants closure), walks the volume tenant's data root, and writes back
	// the recomputed usage (SetUsage closure). It must not panic and the
	// authoritative usage must be a defined (non-negative) value afterward.
	rec.RunOnce(ctx)

	info, err := svc.GetTenant(ctx, "qrec")
	if err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if info.Quotas != nil {
		if used, uerr := info.Quotas.CurrentUsage(quota.ResourceStorageBytes); uerr != nil {
			t.Fatalf("CurrentUsage: %v", uerr)
		} else if used < 0 {
			t.Fatalf("reconciled usage must be non-negative, got %d", used)
		}
	}
}
