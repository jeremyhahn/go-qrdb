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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// anchorRequest builds what the router would hand a trust-anchor handler for
// a caller of callerTenant naming tenant id -- WITHOUT the router, so no
// route-level target check runs. The handler and the service are all that
// stand between the caller and another tenant's anchors.
func anchorRequest(method, callerTenant, id, fingerprint, body string) *http.Request {
	req := httptest.NewRequest(method, "/v1/tenants/"+id+"/trust-anchors", strings.NewReader(body))
	req.SetPathValue("id", id)
	if fingerprint != "" {
		req.SetPathValue("fingerprint", fingerprint)
	}
	ctx := auth.WithIdentity(req.Context(), &auth.Identity{Subject: callerTenant + "-admin", TenantID: callerTenant, Roles: []string{"admin"}})
	return req.WithContext(tenant.WithTenantID(ctx, callerTenant))
}

// TestTrustAnchorHandlers_RefuseAnotherTenantWithoutTheRouteTarget: a tenant
// Admin acting on another tenant's anchors is refused by the handlers
// themselves -- with the auth middleware's handler gate, and, with no
// middleware at all, by the service -- so removing the route-level target
// cannot open another tenant's trust store.
func TestTrustAnchorHandlers_RefuseAnotherTenantWithoutTheRouteTarget(t *testing.T) {
	for _, withMW := range []bool{true, false} {
		f := newCertScopeFixture(t)
		if !withMW {
			f.srv.authMW = nil
		}
		seeded, err := f.anchors.Add(context.Background(), certScopeTenantB, newCertScopeCAPEM(t, "globex-root"))
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		body, _ := json.Marshal(map[string]string{"certs_pem": string(newCertScopeCAPEM(t, "rogue"))})

		for name, call := range map[string]func(*httptest.ResponseRecorder){
			"add": func(rec *httptest.ResponseRecorder) {
				f.srv.handleAddTrustAnchors(rec, anchorRequest(http.MethodPost, certScopeTenantA, certScopeTenantB, "", string(body)))
			},
			"list": func(rec *httptest.ResponseRecorder) {
				f.srv.handleListTrustAnchors(rec, anchorRequest(http.MethodGet, certScopeTenantA, certScopeTenantB, "", ""))
			},
			"remove": func(rec *httptest.ResponseRecorder) {
				f.srv.handleRemoveTrustAnchor(rec, anchorRequest(http.MethodDelete, certScopeTenantA, certScopeTenantB, seeded[0].Fingerprint, ""))
			},
		} {
			rec := httptest.NewRecorder()
			call(rec)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("middleware=%v %s: status = %d, want 403 (body %q)", withMW, name, rec.Code, rec.Body.String())
			}
		}
		left, err := f.anchors.List(context.Background(), certScopeTenantB)
		if err != nil || len(left) != 1 || left[0].Fingerprint != seeded[0].Fingerprint {
			t.Fatalf("middleware=%v: tenant B's anchors = %v, %v; want only the seeded one", withMW, left, err)
		}
	}
}

// TestTrustAnchorHandlers_OwnTenantWithoutTheRouteTarget is the control.
func TestTrustAnchorHandlers_OwnTenantWithoutTheRouteTarget(t *testing.T) {
	f := newCertScopeFixture(t)
	body, _ := json.Marshal(map[string]string{"certs_pem": string(newCertScopeCAPEM(t, "acme-root"))})
	rec := httptest.NewRecorder()
	f.srv.handleAddTrustAnchors(rec, anchorRequest(http.MethodPost, certScopeTenantA, certScopeTenantA, "", string(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("own-tenant add: status = %d (body %q)", rec.Code, rec.Body.String())
	}
}
