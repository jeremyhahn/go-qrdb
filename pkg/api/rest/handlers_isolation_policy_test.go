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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

func restFloorCall(srv *Server, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/v1/cluster/isolation-policy", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

// strictIsolationActiveRESTServer is descriptiveRESTServer with the
// strict-isolation gate reporting active, which this binary cannot reach
// in production: it exercises the verbs' success paths.
func strictIsolationActiveRESTServer(t *testing.T) *Server {
	t.Helper()
	srv := descriptiveRESTServer(t)
	srv.tenantSvc.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })
	return srv
}

// TestIsolationPolicyVerbs_RefusedBeforeStrictIsolation pins that before
// strict-isolation is active the floor's get and set and a tenant update
// carrying isolation_policy answer 503 and change nothing.
func TestIsolationPolicyVerbs_RefusedBeforeStrictIsolation(t *testing.T) {
	srv := descriptiveRESTServer(t)
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"get floor":     restFloorCall(srv, http.MethodGet, ""),
		"set floor":     restFloorCall(srv, http.MethodPut, `{"policy":"strict"}`),
		"tenant strict": restTenantCall(srv, http.MethodPut, `{"isolation_policy":"strict"}`),
	} {
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "strict-isolation") {
			t.Fatalf("%s before activation: status %d (%s); want 503 naming strict-isolation", name, rec.Code, rec.Body.String())
		}
	}
	if got := restTenantGet(t, srv); got.IsolationPolicy != tenant.TenantIsolationPolicyInherit {
		t.Fatalf("isolation_policy after a refused update = %q; want inherit", got.IsolationPolicy)
	}
	srv.tenantSvc.(tenant.StrictIsolationWiring).SetStrictIsolationGate(func() error { return nil })
	if got := restFloorGet(t, srv); got != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("floor after a refused set = %q; want operator-access", got)
	}
}

func restFloorGet(t *testing.T, srv *Server) tenant.ClusterIsolationPolicy {
	t.Helper()
	rec := restFloorCall(srv, http.MethodGet, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET floor: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body clusterIsolationPolicyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Policy
}

// TestClusterIsolationPolicy_TightenOnly pins the floor routes: an unset
// floor reads as operator-access, it tightens to strict, and a loosening is
// 409 with the floor unchanged.
func TestClusterIsolationPolicy_TightenOnly(t *testing.T) {
	srv := strictIsolationActiveRESTServer(t)
	if got := restFloorGet(t, srv); got != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("unset floor = %q; want operator-access", got)
	}
	if rec := restFloorCall(srv, http.MethodPut, `{"policy":"strict"}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT strict: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec := restFloorCall(srv, http.MethodPut, `{"policy":"operator-access"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT operator-access after strict: status %d; want 409 (%s)", rec.Code, rec.Body.String())
	}
	if got := restFloorGet(t, srv); got != tenant.ClusterIsolationPolicyStrict {
		t.Fatalf("floor after refused loosening = %q; want strict", got)
	}
}

func TestClusterIsolationPolicy_BadBodyAndNoService(t *testing.T) {
	srv := strictIsolationActiveRESTServer(t)
	if rec := restFloorCall(srv, http.MethodPut, `{`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status %d; want 400", rec.Code)
	}
	if rec := restFloorCall(srv, http.MethodPut, `{"policy":"loose"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown policy: status %d; want 400 (%s)", rec.Code, rec.Body.String())
	}
	if got := restFloorGet(t, srv); got != tenant.ClusterIsolationPolicyOperatorAccess {
		t.Fatalf("floor after an invalid policy = %q; want operator-access", got)
	}
	bare := &Server{}
	for _, h := range []http.HandlerFunc{bare.handleGetClusterIsolationPolicy, bare.handleSetClusterIsolationPolicy} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/v1/cluster/isolation-policy", strings.NewReader("{}")))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("no tenant service: status %d; want 501", rec.Code)
		}
	}
}

// TestHandleUpdateTenant_IsolationPolicy pins the tenant's own policy on
// PUT /v1/tenants/{id}: inherit to strict is allowed and shows on GET;
// strict back to inherit ("") is 409 and leaves the tenant strict.
func TestHandleUpdateTenant_IsolationPolicy(t *testing.T) {
	srv := strictIsolationActiveRESTServer(t)
	if rec := restTenantCall(srv, http.MethodPut, `{"isolation_policy":""}`); rec.Code != http.StatusOK {
		t.Fatalf("inherit on an inherit tenant: status %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := restTenantCall(srv, http.MethodPut, `{"isolation_policy":"loose"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown isolation_policy: status %d; want 400 (%s)", rec.Code, rec.Body.String())
	}
	if rec := restTenantCall(srv, http.MethodPut, `{"isolation_policy":"strict"}`); rec.Code != http.StatusOK {
		t.Fatalf("strict: status %d (%s)", rec.Code, rec.Body.String())
	}
	if got := restTenantGet(t, srv); got.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("isolation_policy = %q; want strict", got.IsolationPolicy)
	}
	rec := restTenantCall(srv, http.MethodPut, `{"isolation_policy":""}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("strict to inherit: status %d; want 409 (%s)", rec.Code, rec.Body.String())
	}
	if got := restTenantGet(t, srv); got.IsolationPolicy != tenant.TenantIsolationPolicyStrict {
		t.Fatalf("isolation_policy after refused loosening = %q; want strict", got.IsolationPolicy)
	}
	if rec := restTenantCall(srv, http.MethodPut, `{"description":"d"}`); rec.Code != http.StatusOK {
		t.Fatalf("update without isolation_policy on a strict tenant: status %d (%s)", rec.Code, rec.Body.String())
	}
}
