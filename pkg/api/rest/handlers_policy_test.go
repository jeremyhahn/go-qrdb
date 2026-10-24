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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// stubPolicyService is a policy service that is present but never called;
// it lets a server register the /v1/abac/policies routes.
type stubPolicyService struct{ service.PolicyServicer }

var _ service.PolicyServicer = stubPolicyService{}

// restPolicyStores resolves each tenant to its own memory store; an unknown
// tenant is sealed.
type restPolicyStores map[string]kvstore.KVStore

func (p restPolicyStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	if s, ok := p[tenantID]; ok {
		return s, nil
	}
	return nil, tenant.ErrTenantSealed
}

func newPolicyServer(t *testing.T) *Server {
	t.Helper()
	svc, err := service.NewPolicyService(restPolicyStores{"acme": kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0", WithPolicyService(svc))
}

// policyDo sends a raw JSON body as tenant acme through the router.
func policyDo(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(tenant.WithTenantID(req.Context(), "acme"))
	rec := httptest.NewRecorder()
	s.dispatch(rec, req)
	return rec
}

func restPolicyJSON(name, enabled string) string {
	spec := `"subjects":{"roles":["auditor"]},"rules":[{"name":"deny-secrets","effect":"deny","actions":["read"],"resources":["/secrets/**"]}]`
	if enabled != "" {
		spec = `"enabled":` + enabled + `,` + spec
	}
	return `{"apiVersion":"abac.qrdb.io/v1","kind":"Policy","metadata":{"name":"` + name + `"},"spec":{` + spec + `}}`
}

// specHasEnabled reports whether a response body's spec carries the
// enabled key at all, and its value when it does.
func specHasEnabled(t *testing.T, body []byte) (present bool, value bool) {
	t.Helper()
	var doc struct {
		Spec map[string]json.RawMessage `json:"spec"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	raw, ok := doc.Spec["enabled"]
	if !ok {
		return false, false
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("enabled %s: %v", raw, err)
	}
	return true, value
}

// TestHandlePolicy_LifecycleThroughTheRouter drives every route and pins
// that the JSON spec carries enabled exactly when the stored document set
// it: omitted stays omitted, false stays false.
func TestHandlePolicy_LifecycleThroughTheRouter(t *testing.T) {
	s := newPolicyServer(t)

	rec := policyDo(t, s, http.MethodPost, "/v1/abac/policies", restPolicyJSON("unset", ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if present, _ := specHasEnabled(t, rec.Body.Bytes()); present {
		t.Fatalf("create returned enabled for a policy written without it: %s", rec.Body)
	}
	if rec := policyDo(t, s, http.MethodPost, "/v1/abac/policies", restPolicyJSON("off", "false")); rec.Code != http.StatusCreated {
		t.Fatalf("create off: %d %s", rec.Code, rec.Body)
	}

	rec = policyDo(t, s, http.MethodGet, "/v1/abac/policies/unset", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	if present, _ := specHasEnabled(t, rec.Body.Bytes()); present {
		t.Fatalf("get returned enabled for a policy written without it: %s", rec.Body)
	}
	rec = policyDo(t, s, http.MethodGet, "/v1/abac/policies/off?tenant_id=acme", "")
	if present, value := specHasEnabled(t, rec.Body.Bytes()); rec.Code != http.StatusOK || !present || value {
		t.Fatalf("get off: %d %s, want enabled=false present", rec.Code, rec.Body)
	}

	rec = policyDo(t, s, http.MethodGet, "/v1/abac/policies", "")
	var list struct {
		Policies []json.RawMessage `json:"policies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || len(list.Policies) != 2 {
		t.Fatalf("list: %d %s (%v)", rec.Code, rec.Body, err)
	}
	if present, value := specHasEnabled(t, list.Policies[0]); !present || value {
		t.Fatalf("list[0] (off) = %s, want enabled=false present", list.Policies[0])
	}
	if present, _ := specHasEnabled(t, list.Policies[1]); present {
		t.Fatalf("list[1] (unset) = %s, want enabled omitted", list.Policies[1])
	}

	// A body without metadata.name takes the path's name.
	body := strings.Replace(restPolicyJSON("", "true"), `"name":""`, `"description":"renamed by path"`, 1)
	rec = policyDo(t, s, http.MethodPut, "/v1/abac/policies/unset", body)
	if present, value := specHasEnabled(t, rec.Body.Bytes()); rec.Code != http.StatusOK || !present || !value {
		t.Fatalf("update: %d %s, want enabled=true", rec.Code, rec.Body)
	}

	if rec := policyDo(t, s, http.MethodDelete, "/v1/abac/policies/unset", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := policyDo(t, s, http.MethodGet, "/v1/abac/policies/unset", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d, want 404", rec.Code)
	}
}

// TestHandlePolicy_RefusalsCarryTheirStatus pins the HTTP status of every
// refusal the routes render.
func TestHandlePolicy_RefusalsCarryTheirStatus(t *testing.T) {
	s := newPolicyServer(t)
	if rec := policyDo(t, s, http.MethodPost, "/v1/abac/policies", restPolicyJSON("p", "")); rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"create exists", http.MethodPost, "/v1/abac/policies", restPolicyJSON("p", ""), http.StatusConflict},
		{"create bad body", http.MethodPost, "/v1/abac/policies", "{", http.StatusBadRequest},
		{"create bad effect", http.MethodPost, "/v1/abac/policies", strings.Replace(restPolicyJSON("q", ""), `"deny"`, `"maybe"`, 1), http.StatusBadRequest},
		{"create cross tenant", http.MethodPost, "/v1/abac/policies", strings.Replace(restPolicyJSON("q", ""), `"name":"q"`, `"name":"q","tenant":"globex"`, 1), http.StatusForbidden},
		{"get missing", http.MethodGet, "/v1/abac/policies/missing", "", http.StatusNotFound},
		{"list cross tenant", http.MethodGet, "/v1/abac/policies?tenant_id=globex", "", http.StatusForbidden},
		{"update name mismatch", http.MethodPut, "/v1/abac/policies/p", restPolicyJSON("other", ""), http.StatusBadRequest},
		{"update bad body", http.MethodPut, "/v1/abac/policies/p", "{", http.StatusBadRequest},
		{"update missing", http.MethodPut, "/v1/abac/policies/missing", restPolicyJSON("missing", ""), http.StatusNotFound},
		{"delete missing", http.MethodDelete, "/v1/abac/policies/missing", "", http.StatusNotFound},
	}
	for _, tc := range cases {
		if rec := policyDo(t, s, tc.method, tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
	}
}

// TestHandlePolicy_RoutesAbsentWithoutTheService pins that a server built
// without a policy service registers none of the policy routes.
func TestHandlePolicy_RoutesAbsentWithoutTheService(t *testing.T) {
	s := NewServer(&mockKVService{}, newDefaultAdminSvc(), ":0")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := policyDo(t, s, method, "/v1/abac/policies", restPolicyJSON("p", ""))
		if rec.Code == http.StatusOK || rec.Code == http.StatusCreated {
			t.Fatalf("%s: status %d, want the route to be absent", method, rec.Code)
		}
	}
}

// TestPolicyListResponse_EmptyIsArray pins that an empty list encodes as
// [] rather than null.
func TestPolicyListResponse_EmptyIsArray(t *testing.T) {
	s := newPolicyServer(t)
	rec := policyDo(t, s, http.MethodGet, "/v1/abac/policies", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"policies":[]`) {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body)
	}
}
