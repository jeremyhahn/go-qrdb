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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestServiceErrorCode_DataRootOverlapIsAFailedPrecondition: MCP answers a
// DataRoot collision, a retired claimant's included, with the same code as
// gRPC, because it classifies through the same classifier.
func TestServiceErrorCode_DataRootOverlapIsAFailedPrecondition(t *testing.T) {
	err := &ToolError{Tool: "qrdb_tenant_create", Err: &tenant.DataRootOverlapError{
		DataRoot: "/srv/d", Conflict: "/srv/d", Kind: "retired tenant vgone", Remedy: "run `qrdb tenant reconcile-deleted`",
	}}
	if got := ServiceErrorCode(err); got != wire.CodeFailedPrecondition {
		t.Fatalf("ServiceErrorCode = %v; want CodeFailedPrecondition", got)
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
	handler := handleTenantReconcileDeleted(failingMCPDeletedSvc{report: report, err: agg})
	result, err := handler(context.Background(), buildCallToolRequest(t, "qrdb_tenant_reconcile_deleted", nil))
	if err != nil || result.IsError {
		t.Fatalf("reconcile = %v, %v; want the report", result, err)
	}
	var got tenant.ReconcileReport
	if err := json.Unmarshal([]byte(resultText(t, result)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Reclaimed) != 1 || len(got.Failed) != 1 || got.Failed[0].TenantID != "va" {
		t.Fatalf("report = %+v; want vb reclaimed and va failed", got)
	}
}
