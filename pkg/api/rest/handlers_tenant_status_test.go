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
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The update request carries status, the read returns it, and an absent
// status leaves it unchanged.
func TestHandleUpdateTenant_StatusRoundTrip(t *testing.T) {
	srv := descriptiveRESTServer(t)
	if got := restTenantGet(t, srv).Status; got != tenant.StatusActive {
		t.Fatalf("new tenant status = %q; want active", got)
	}
	for _, want := range []tenant.Status{tenant.StatusSuspended, tenant.StatusDisabled, tenant.StatusActive} {
		rec := restTenantCall(srv, http.MethodPut, `{"status":"`+string(want)+`"}`)
		assertStatus(t, rec, http.StatusOK)
		var resp tenantResponse
		decodeJSON(t, rec, &resp)
		if resp.Status != want {
			t.Fatalf("update to %s answered %q", want, resp.Status)
		}
		if got := restTenantGet(t, srv).Status; got != want {
			t.Fatalf("read after %s = %q", want, got)
		}
	}
	assertStatus(t, restTenantCall(srv, http.MethodPut, `{"status":"suspended"}`), http.StatusOK)
	assertStatus(t, restTenantCall(srv, http.MethodPut, `{"description":"d"}`), http.StatusOK)
	if got := restTenantGet(t, srv).Status; got != tenant.StatusSuspended {
		t.Fatalf("absent status changed it: %q", got)
	}
}

// An unknown status is 400; a transition the table does not allow is 409.
// Neither is stored.
func TestHandleUpdateTenant_StatusRefusals(t *testing.T) {
	srv := descriptiveRESTServer(t)
	if rec := restTenantCall(srv, http.MethodPut, `{"status":"paused"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status: %d; want 400 (%s)", rec.Code, rec.Body.String())
	}
	assertStatus(t, restTenantCall(srv, http.MethodPut, `{"status":"disabled"}`), http.StatusOK)
	if rec := restTenantCall(srv, http.MethodPut, `{"status":"suspended"}`); rec.Code != http.StatusConflict {
		t.Fatalf("disabled -> suspended: %d; want 409 (%s)", rec.Code, rec.Body.String())
	}
	if got := restTenantGet(t, srv).Status; got != tenant.StatusDisabled {
		t.Fatalf("refused transition was stored: %q", got)
	}
}
