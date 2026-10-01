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

package rest

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestMapHTTPError_DataRootOverlapIs409: a DataRoot that collides with a
// reserved path or another tenant's -- a retired one's too, whose message
// carries the remedy -- is a conflict the caller resolves. It was 500 before.
func TestMapHTTPError_DataRootOverlapIs409(t *testing.T) {
	for _, err := range []error{
		&tenant.DataRootOverlapError{DataRoot: "/srv/a", Conflict: "/srv", Kind: "volume pool"},
		&tenant.DataRootOverlapError{DataRoot: "/srv/d", Conflict: "/srv/d", Kind: "retired tenant vgone", Remedy: "run `qrdb tenant reconcile-deleted`"},
	} {
		if got := mapHTTPError(err); got != http.StatusConflict {
			t.Fatalf("mapHTTPError(%v) = %d; want 409", err, got)
		}
	}
}

// TestHandleReconcileDeletedTenants_PartialFailureCarriesBoth: a pass that
// failed for one id answers 200 with the report, whose failed list names it.
func TestHandleReconcileDeletedTenants_PartialFailureCarriesBoth(t *testing.T) {
	boom := errors.New("device busy")
	report := &tenant.ReconcileReport{
		Inspected: 2,
		Reclaimed: []tenant.ReclaimedPath{{TenantID: "vb", Path: "/srv/vb"}},
		Failed:    []tenant.FailedReclaim{{TenantID: "va", Op: "remove", Path: "/srv/va", Error: boom.Error()}},
	}
	agg := &tenant.ResidueReconcileErrors{Errors: []*tenant.ResidueReconcileError{{TenantID: "va", Op: "remove", Path: "/srv/va", Err: boom}}}
	srv := newTenantServer(failingRESTDeletedSvc{report: report, err: agg})

	rec := httptest.NewRecorder()
	srv.dispatch(rec, httptest.NewRequest(http.MethodPost, "/v1/tenants/deleted/reconcile", nil))
	assertStatus(t, rec, http.StatusOK)
	var got tenant.ReconcileReport
	decodeJSON(t, rec, &got)
	if len(got.Reclaimed) != 1 || len(got.Failed) != 1 || got.Failed[0].TenantID != "va" || got.Failed[0].Error != "device busy" {
		t.Fatalf("report = %+v; want vb reclaimed and va failed", got)
	}
}
