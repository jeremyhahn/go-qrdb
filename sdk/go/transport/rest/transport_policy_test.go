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
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/abac"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTPolicy_EveryVerbHitsItsRoute holds each method to its route, verb,
// tenant query and body.
func TestRESTPolicy_EveryVerbHitsItsRoute(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	off := false
	doc := &abac.Policy{Kind: abac.PolicyKind, Metadata: abac.PolicyMetadata{Name: "a b"}, Spec: abac.PolicySpec{Enabled: &off}}
	echo := func(w http.ResponseWriter, r *http.Request) {
		var p abac.Policy
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, &p)
	}
	rs.handle(exactPath(http.MethodPost, "/v1/abac/policies"), echo)
	rs.handle(exactPath(http.MethodPut, "/v1/abac/policies/a b"), echo)
	rs.handle(exactPath(http.MethodGet, "/v1/abac/policies/a b"), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tenant_id") != "acme" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, doc)
	})
	rs.handle(exactPath(http.MethodGet, "/v1/abac/policies"), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("tenant_id") == "empty" {
			writeJSON(w, map[string]any{"policies": nil})
			return
		}
		writeJSON(w, &transport.ABACPolicyListResponse{Policies: []*abac.Policy{doc}})
	})
	rs.handle(exactPath(http.MethodDelete, "/v1/abac/policies/a b"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	ctx := context.Background()
	if p, err := rt.CreateABACPolicy(ctx, doc); err != nil || p.Spec.Enabled == nil || *p.Spec.Enabled {
		t.Errorf("create = %+v, %v", p, err)
	}
	if p, err := rt.GetABACPolicy(ctx, &transport.ABACPolicyRequest{TenantID: "acme", Name: "a b"}); err != nil || p.Metadata.Name != "a b" {
		t.Errorf("get = %+v, %v", p, err)
	}
	if l, err := rt.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{}); err != nil || len(l.Policies) != 1 {
		t.Errorf("list = %+v, %v", l, err)
	}
	if l, err := rt.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{TenantID: "empty"}); err != nil || l.Policies == nil {
		t.Errorf("empty list = %#v, %v, want an empty array", l, err)
	}
	if p, err := rt.UpdateABACPolicy(ctx, doc); err != nil || p.Metadata.Name != "a b" {
		t.Errorf("update = %+v, %v", p, err)
	}
	if err := rt.DeleteABACPolicy(ctx, &transport.ABACPolicyRequest{Name: "a b"}); err != nil {
		t.Errorf("delete: %v", err)
	}
}

func TestRESTPolicy_ServerRefusalIsReturned(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/v1/abac/policies/missing"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "abac policy not found"})
	})
	if err := rt.DeleteABACPolicy(context.Background(), &transport.ABACPolicyRequest{Name: "missing"}); err == nil {
		t.Fatal("a 404 must surface as an error")
	}
}

func TestRESTPolicy_NilRequestAndClosed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	ctx := context.Background()
	checks := map[string]error{}
	_, checks["create"] = rt.CreateABACPolicy(ctx, nil)
	_, checks["get"] = rt.GetABACPolicy(ctx, nil)
	_, checks["list"] = rt.ListABACPolicies(ctx, nil)
	_, checks["update"] = rt.UpdateABACPolicy(ctx, nil)
	checks["delete"] = rt.DeleteABACPolicy(ctx, nil)
	for op, err := range checks {
		var nre *transport.NilRequestError
		if !errors.As(err, &nre) {
			t.Errorf("%s(nil): %v", op, err)
		}
	}
	_ = rt.Close()
	var ce *transport.ClosedError
	if _, err := rt.ListABACPolicies(ctx, &transport.ABACPolicyListRequest{}); !errors.As(err, &ce) {
		t.Errorf("list after close: %v", err)
	}
}
