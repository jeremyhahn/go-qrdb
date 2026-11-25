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

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestE2E_ListTenants_OlderServerIgnoresStatus is the rolling-upgrade case.
// The double is a base-version server: it predates the tenant.List status
// filter, drops it and answers every tenant. A filtered list must fail with
// *CapabilityUnsupportedError and no tenants; an unfiltered list still
// succeeds.
func TestE2E_ListTenants_OlderServerIgnoresStatus(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.List", func([]byte) (uint16, []byte) {
		b, _ := proto.Marshal(&pb.TenantListResponse{Tenants: []*pb.TenantResponse{
			{Id: "t-active", Name: "a", Status: "active"},
			{Id: "t-suspended", Name: "s", Status: "suspended"},
		}})
		return wire.StatusOK, b
	})

	resp, err := tr.ListTenants(context.Background(), &transport.ListTenantsRequest{Status: transport.TenantStatusSuspended})
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) || resp != nil {
		t.Fatalf("ListTenants(status=suspended) on an older server = %+v, %v; want nil, CapabilityUnsupportedError", resp, err)
	}

	resp, err = tr.ListTenants(context.Background(), nil)
	if err != nil || len(resp.Tenants) != 2 {
		t.Fatalf("unfiltered ListTenants on an older server = %+v, %v; want 2 tenants", resp, err)
	}
}
