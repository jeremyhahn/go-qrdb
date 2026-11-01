// Copyright (c) 2026 Jeremy Hahn
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

package roleconv

import (
	"reflect"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestReconcileFromProto_CarriesEveryField(t *testing.T) {
	got := ReconcileFromProto(&pb.ReconcileBuiltInRolesResponse{
		TenantId: "acme",
		Roles: []*pb.RoleReconcileResult{{
			RoleId: "r1", Name: "admin", Created: true,
			Added: []string{"roles:read"}, Removed: []string{"certs:generate"},
		}},
	})
	want := &transport.ReconcileRolesResponse{
		TenantID: "acme",
		Roles: []transport.RoleReconcileResult{{
			RoleID: "r1", Name: "admin", Created: true,
			Added: []string{"roles:read"}, Removed: []string{"certs:generate"},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestReconcileFromProto_Nil(t *testing.T) {
	if got := ReconcileFromProto(nil); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

func TestAllTenantsFromProto_CarriesEveryField(t *testing.T) {
	got := AllTenantsFromProto(&pb.ReconcileBuiltInRolesAllTenantsResponse{
		Tenants: []*pb.ReconcileBuiltInRolesResponse{
			{TenantId: "system"},
			nil,
			{TenantId: "acme", Roles: []*pb.RoleReconcileResult{{RoleId: "r1", Name: "operator", Removed: []string{"certs:generate"}}}},
		},
		Skipped: []*pb.RoleReconcileSkippedTenant{{TenantId: "vault", Reason: "sealed", Detail: "barrier sealed"}},
		Failed:  []*pb.RoleReconcileFailedTenant{{TenantId: "bad", Error: "store unavailable"}},
	})
	want := &transport.ReconcileAllTenantsRolesResponse{
		Tenants: []transport.ReconcileRolesResponse{
			{TenantID: "system"},
			{TenantID: "acme", Roles: []transport.RoleReconcileResult{{RoleID: "r1", Name: "operator", Removed: []string{"certs:generate"}}}},
		},
		Skipped: []transport.RoleReconcileSkippedTenant{{TenantID: "vault", Reason: "sealed", Detail: "barrier sealed"}},
		Failed:  []transport.RoleReconcileFailedTenant{{TenantID: "bad", Error: "store unavailable"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAllTenantsFromProto_NilIsEmpty(t *testing.T) {
	got := AllTenantsFromProto(nil)
	if got == nil || len(got.Tenants)+len(got.Skipped)+len(got.Failed) != 0 {
		t.Fatalf("got %+v, want an empty report", got)
	}
}
