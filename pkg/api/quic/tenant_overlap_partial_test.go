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

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestMapServiceError_DataRootOverlapIsAFailedPrecondition mirrors gRPC's
// FailedPrecondition and REST's 409 for a DataRoot collision, a retired
// claimant's included, carrying the remedy in the message.
func TestMapServiceError_DataRootOverlapIsAFailedPrecondition(t *testing.T) {
	err := &tenant.DataRootOverlapError{DataRoot: "/srv/d", Conflict: "/srv/d", Kind: "retired tenant vgone", Remedy: "run `qrdb tenant reconcile-deleted`"}
	code, msg := mapServiceError(err)
	if code != wire.StatusFailedPrecondition || string(msg) != err.Error() {
		t.Fatalf("mapServiceError = %d %q; want StatusFailedPrecondition carrying the message", code, msg)
	}
}

// TestHandleTenantReconcileDeleted_PartialFailureCarriesBoth: a pass that
// failed for one id answers with the report, whose failed list names it.
func TestHandleTenantReconcileDeleted_PartialFailureCarriesBoth(t *testing.T) {
	boom := errors.New("device busy")
	report := &tenant.ReconcileReport{
		Inspected: 2,
		Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}},
		Failed:    []tenant.FailedReclaim{{TenantID: "va", Op: "remove", Path: "/srv/va", Error: boom.Error()}},
	}
	agg := &tenant.ResidueReconcileFailuresError{Errors: []*tenant.ResidueReconcileError{{TenantID: "va", Op: "remove", Path: "/srv/va", Err: boom}}}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(failingQUICDeletedSvc{report: report, err: agg}))
	raw, err := srv.handleTenantReconcileDeleted(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleTenantReconcileDeleted = %v; want the report", err)
	}
	var resp pb.TenantReconcileDeletedResponse
	if err := proto.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.GetReclaimed()) != 1 || len(resp.GetFailed()) != 1 || resp.GetFailed()[0].GetTenantId() != "va" {
		t.Fatalf("resp = %v; want vb reclaimed and va failed", &resp)
	}
}
