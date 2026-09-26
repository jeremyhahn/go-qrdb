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

func TestToListDeletedTenantsResponse(t *testing.T) {
	if got := ToListDeletedTenantsResponse(nil); got == nil || len(got.Tenants) != 0 || got.NextPageToken != "" {
		t.Fatalf("nil response = %+v; want an empty page", got)
	}
	deleted := time.Date(2026, 9, 20, 14, 31, 7, 0, time.UTC)
	got := ToListDeletedTenantsResponse(&pb.TenantListDeletedResponse{
		NextPageToken: "tok",
		Tenants: []*pb.DeletedTenant{
			{Id: "acme", Name: "Acme", IsolationType: "volume", DataRoot: "/d/acme", DeletedAt: deleted.Format(time.RFC3339)},
			{Id: "undated", DeletedAt: "not a time"},
		},
	})
	if got.NextPageToken != "tok" || len(got.Tenants) != 2 {
		t.Fatalf("page = %+v", got)
	}
	a := got.Tenants[0]
	if a.ID != "acme" || a.Name != "Acme" || a.IsolationType != "volume" || a.DataRoot != "/d/acme" ||
		!a.DeletedAt.Equal(deleted) || !a.CreatedAt.IsZero() {
		t.Fatalf("record = %+v", a)
	}
	if !got.Tenants[1].DeletedAt.IsZero() {
		t.Fatal("a malformed timestamp must leave the field zero")
	}
}

func TestToReconcileDeletedTenantsResponse(t *testing.T) {
	if got := ToReconcileDeletedTenantsResponse(nil); got == nil || got.Inspected != 0 {
		t.Fatalf("nil response = %+v", got)
	}
	got := ToReconcileDeletedTenantsResponse(&pb.TenantReconcileDeletedResponse{
		Inspected: 2,
		Reclaimed: []*pb.TenantResiduePath{{TenantId: "a", Path: "/p/a"}},
		Skipped:   []*pb.TenantResiduePath{{TenantId: "b", Path: "/p/b", Reason: "overlaps-live-tenant"}},
	})
	if got.Inspected != 2 || len(got.Reclaimed) != 1 || got.Reclaimed[0].Path != "/p/a" ||
		len(got.Skipped) != 1 || got.Skipped[0].Reason != "overlaps-live-tenant" {
		t.Fatalf("report = %+v", got)
	}
}
