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

package tenantconv

import (
	"testing"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// TestToTenantInfo_Nil proves a nil response yields the zero value (no panic).
func TestToTenantInfo_Nil(t *testing.T) {
	got := ToTenantInfo(nil)
	if got.ID != "" || got.Quotas != nil || got.Entitlements != nil {
		t.Fatalf("nil response should yield the zero value, got %+v", got)
	}
}

// TestToTenantInfo_Full proves every field (scalars, quotas, entitlements,
// timestamps) is converted — the shared converter must not drop any field that
// the per-transport duplicates carried.
func TestToTenantInfo_Full(t *testing.T) {
	resp := &pb.TenantResponse{
		Id:            "acme",
		Name:          "Acme Corp",
		IsolationType: "raft",
		CreatedAt:     "2026-06-27T08:00:00Z",
		UpdatedAt:     "2026-06-27T09:00:00Z",
		Quotas: &pb.TenantQuotas{
			MaxDatabases:          5,
			MaxKeys:               100,
			SystemBarrierQuota:    2,
			MaxDedicatedWalGroups: 3,
		},
		Entitlements: &pb.TenantEntitlements{
			AllowDedicatedRaft:  true,
			AllowDedicatedWal:   true,
			AllowedBarrierModes: []string{"passphrase"},
		},
	}

	got := ToTenantInfo(resp)
	if got.ID != "acme" || got.Name != "Acme Corp" || string(got.IsolationType) != "raft" {
		t.Fatalf("scalar fields wrong: %+v", got)
	}
	if got.Quotas == nil || got.Quotas.MaxDatabases != 5 || got.Quotas.MaxKeys != 100 ||
		got.Quotas.SystemBarrierQuota != 2 || got.Quotas.MaxDedicatedWALGroups != 3 {
		t.Fatalf("quotas wrong: %+v", got.Quotas)
	}
	if got.Entitlements == nil || !got.Entitlements.AllowDedicatedRaft ||
		!got.Entitlements.AllowDedicatedWAL || len(got.Entitlements.AllowedBarrierModes) != 1 {
		t.Fatalf("entitlements wrong: %+v", got.Entitlements)
	}
	if !got.CreatedAt.Equal(time.Date(2026, 6, 27, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("createdAt wrong: %v", got.CreatedAt)
	}
	if !got.UpdatedAt.Equal(time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("updatedAt wrong: %v", got.UpdatedAt)
	}
}

// TestToTenantInfo_PartialAndBadTimestamp proves absent quotas/entitlements stay
// nil and a malformed timestamp leaves the field at its zero value (best-effort).
func TestToTenantInfo_PartialAndBadTimestamp(t *testing.T) {
	got := ToTenantInfo(&pb.TenantResponse{Id: "x", CreatedAt: "not-a-time"})
	if got.ID != "x" || got.Quotas != nil || got.Entitlements != nil {
		t.Fatalf("partial response wrong: %+v", got)
	}
	if !got.CreatedAt.IsZero() {
		t.Fatalf("malformed timestamp should leave CreatedAt zero, got %v", got.CreatedAt)
	}
}
