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

package quic

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestE2E_ReconcileDeletedTenants_PartialFailureCarriesBoth: the frame
// carries the report encoded by the server's own conversion, failures
// included, and the client returns the response AND the error.
func TestE2E_ReconcileDeletedTenants_PartialFailureCarriesBoth(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.ReconcileDeleted", func([]byte) (uint16, []byte) {
		b, _ := proto.Marshal(grpcapi.ReconcileReportToProto(&tenant.ReconcileReport{
			Inspected: 2,
			Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}},
			Failed:    []tenant.FailedReclaim{{TenantID: "va", Op: "remove", Path: "/srv/va", Error: "device busy"}},
		}))
		return wire.StatusOK, b
	})
	resp, err := tr.ReconcileDeletedTenants(context.Background())
	if resp == nil || len(resp.Reclaimed) != 1 || len(resp.Failed) != 1 || resp.Failed[0].Op != "remove" {
		t.Fatalf("resp = %+v; want vb reclaimed and va failed", resp)
	}
	var re *transport.ReconcileDeletedTenantsError
	if !errors.As(err, &re) || re.Failed[0].TenantID != "va" {
		t.Fatalf("err = %v; want *transport.ReconcileDeletedTenantsError naming va", err)
	}
}
