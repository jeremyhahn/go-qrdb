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
	"context"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// descriptiveRESTServer returns a server over the REAL seal-disabled tenant
// service holding one tenant, "acme".
func descriptiveRESTServer(t *testing.T) *Server {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	return newTenantServer(svc)
}

// restTenantCall dispatches method on /v1/tenants/acme with body and returns
// the recorder.
func restTenantCall(srv *Server, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/v1/tenants/acme", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

// restTenantGet reads tenant "acme" back through GET.
func restTenantGet(t *testing.T, srv *Server) tenantResponse {
	t.Helper()
	rec := restTenantCall(srv, http.MethodGet, "")
	assertStatus(t, rec, http.StatusOK)
	var resp tenantResponse
	decodeJSON(t, rec, &resp)
	return resp
}

func TestHandleUpdateTenant_DescriptionAndMetadataRoundTrip(t *testing.T) {
	srv := descriptiveRESTServer(t)
	want := map[string]string{"env": "prod", "team": "storage"}

	rec := restTenantCall(srv, http.MethodPut, `{"description":"Acme production","metadata":{"env":"prod","team":"storage"}}`)
	assertStatus(t, rec, http.StatusOK)
	got := restTenantGet(t, srv)
	if got.Description != "Acme production" || !maps.Equal(got.Metadata, want) {
		t.Fatalf("get = description %q, metadata %v; want %q, %v", got.Description, got.Metadata, "Acme production", want)
	}

	// Absent fields, and a null metadata, leave the stored values unchanged.
	rec = restTenantCall(srv, http.MethodPut, `{"name":"Acme Inc","metadata":null}`)
	assertStatus(t, rec, http.StatusOK)
	got = restTenantGet(t, srv)
	if got.Description != "Acme production" || !maps.Equal(got.Metadata, want) {
		t.Fatalf("rename changed descriptive fields: %q, %v", got.Description, got.Metadata)
	}
}

func TestHandleUpdateTenant_EmptyMetadataObjectClears(t *testing.T) {
	srv := descriptiveRESTServer(t)
	assertStatus(t, restTenantCall(srv, http.MethodPut, `{"description":"to clear","metadata":{"env":"prod"}}`), http.StatusOK)

	rec := restTenantCall(srv, http.MethodPut, `{"description":"","metadata":{}}`)
	assertStatus(t, rec, http.StatusOK)
	got := restTenantGet(t, srv)
	if got.Description != "" || len(got.Metadata) != 0 {
		t.Fatalf("cleared tenant = description %q, metadata %v; want both empty", got.Description, got.Metadata)
	}
}

func TestHandleUpdateTenant_OverLimitIsBadRequest(t *testing.T) {
	srv := descriptiveRESTServer(t)
	cases := map[string]string{
		"description":    `{"description":"` + strings.Repeat("d", tenant.MaxDescriptionBytes+1) + `"}`,
		"metadata value": `{"metadata":{"k":"` + strings.Repeat("v", tenant.MaxMetadataValueBytes+1) + `"}}`,
	}
	for name, body := range cases {
		if rec := restTenantCall(srv, http.MethodPut, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s over limit: status = %d; want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}
	got := restTenantGet(t, srv)
	if got.Description != "" || len(got.Metadata) != 0 {
		t.Fatalf("refused update was stored: %q, %v", got.Description, got.Metadata)
	}
}
