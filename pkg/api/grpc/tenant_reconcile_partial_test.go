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

package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// partialReconcile is what ReconcileDeletedTenants returns when one retired id
// failed and another was reclaimed: the report, whose Failed list copies the
// aggregate error it is returned with (pinned in pkg/tenant).
func partialReconcile() (*tenant.ReconcileReport, error) {
	boom := errors.New("device busy")
	report := &tenant.ReconcileReport{
		Inspected: 2,
		Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}},
		Failed:    []tenant.FailedReclaim{{TenantID: "va", Op: "remove", Path: "/srv/va", Error: boom.Error()}},
	}
	return report, &tenant.ResidueReconcileFailuresError{Errors: []*tenant.ResidueReconcileError{{TenantID: "va", Op: "remove", Path: "/srv/va", Err: boom}}}
}

// TestGRPC_TenantReconcileDeleted_PartialFailureCarriesBoth: a pass that
// failed for one id answers OK with the report, whose failed list names the
// id, instead of an error status that would drop what was reclaimed.
func TestGRPC_TenantReconcileDeleted_PartialFailureCarriesBoth(t *testing.T) {
	report, err := partialReconcile()
	s := &qrdbServiceServer{tenantSvc: failingDeletedSvc{report: report, err: err}}
	resp, rerr := s.TenantReconcileDeleted(context.Background(), &emptypb.Empty{})
	if rerr != nil {
		t.Fatalf("TenantReconcileDeleted = %v; want the report", rerr)
	}
	if len(resp.GetReclaimed()) != 1 || len(resp.GetFailed()) != 1 {
		t.Fatalf("resp = %v; want vb reclaimed and va failed", resp)
	}
	f := resp.GetFailed()[0]
	if f.GetTenantId() != "va" || f.GetOp() != "remove" || f.GetPath() != "/srv/va" || f.GetError() != "device busy" {
		t.Fatalf("failed = %v", f)
	}

	// A guard failure is still an error status.
	s = &qrdbServiceServer{tenantSvc: failingDeletedSvc{err: &tenant.ResidueReconcileError{Op: "list-tenants", Err: errors.New("corrupt")}}}
	if _, rerr := s.TenantReconcileDeleted(context.Background(), &emptypb.Empty{}); status.Code(rerr) != codes.Internal {
		t.Fatalf("guard failure = %v; want Internal", rerr)
	}
}
