// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

func doPolicy(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

func TestHandlePutDelegationPolicy_Success(t *testing.T) {
	var got *service.DelegationPolicyRequest
	ce := &fakeCertEnroll{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			got = req
			return &delegation.Policy{Tenant: "acme", Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodPost, "/certs/delegate/policies",
		`{"principal":"billing","scope":["kv:get"],"ttl":"30m"}`)

	assertStatus(t, rec, http.StatusCreated)
	if got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) || got.TTL != 30*time.Minute {
		t.Fatalf("service request = %+v", got)
	}
	var resp delegationPolicyResponse
	decodeJSON(t, rec, &resp)
	if resp.TenantID != "acme" || resp.Principal != "billing" || resp.TTL != "30m0s" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandlePutDelegationPolicy_RefusesBadRequests(t *testing.T) {
	srv := newEnrollAuthServer(&fakeCertEnroll{})
	for name, body := range map[string]string{
		"invalid json": "{bad",
		"invalid ttl":  `{"principal":"billing","scope":["kv:get"],"ttl":"soon"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assertStatus(t, doPolicy(srv, http.MethodPost, "/certs/delegate/policies", body), http.StatusBadRequest)
		})
	}
}

func TestHandlePutDelegationPolicy_MapsServiceErrors(t *testing.T) {
	ce := &fakeCertEnroll{
		policyPutFn: func(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			return nil, &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodPost, "/certs/delegate/policies", `{"principal":"billing","scope":["kv:get"]}`)
	assertStatus(t, rec, http.StatusForbidden)
}

func TestHandlePutDelegationPolicy_NilServiceDirect(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthServer(nil, nil, nil).handlePutDelegationPolicy(rec,
		httptest.NewRequest(http.MethodPost, "/certs/delegate/policies", strings.NewReader("{}")))
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleListDelegationPolicies_Success(t *testing.T) {
	ce := &fakeCertEnroll{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return []delegation.Policy{{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get"}}}, nil
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodGet, "/certs/delegate/policies", "")

	assertStatus(t, rec, http.StatusOK)
	var resp struct {
		Policies []delegationPolicyResponse `json:"policies"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Policies) != 1 || resp.Policies[0].Principal != "billing" ||
		resp.Policies[0].TTL != delegation.DefaultLifetime.String() {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleListDelegationPolicies_MapsServiceErrors(t *testing.T) {
	rec := doPolicy(newEnrollAuthServer(&fakeCertEnroll{}), http.MethodGet, "/certs/delegate/policies", "")
	if rec.Code < 400 {
		t.Fatalf("status = %d, want an error", rec.Code)
	}
}

func TestHandleListDelegationPolicies_NilServiceDirect(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthServer(nil, nil, nil).handleListDelegationPolicies(rec,
		httptest.NewRequest(http.MethodGet, "/certs/delegate/policies", nil))
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleDeleteDelegationPolicy_Success(t *testing.T) {
	var gotPrincipal string
	ce := &fakeCertEnroll{
		policyDeleteFn: func(_ context.Context, _, principal string) error {
			gotPrincipal = principal
			return nil
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodDelete, "/certs/delegate/policies/billing", "")

	assertStatus(t, rec, http.StatusNoContent)
	if gotPrincipal != "billing" {
		t.Fatalf("principal = %q, want billing", gotPrincipal)
	}
}

func TestHandleDeleteDelegationPolicy_MapsServiceErrors(t *testing.T) {
	ce := &fakeCertEnroll{
		policyDeleteFn: func(context.Context, string, string) error {
			return &service.DelegationRequestError{Field: "principal", Reason: "no policy"}
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodDelete, "/certs/delegate/policies/billing", "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleDeleteDelegationPolicy_NilServiceDirect(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthServer(nil, nil, nil).handleDeleteDelegationPolicy(rec,
		httptest.NewRequest(http.MethodDelete, "/certs/delegate/policies/billing", nil))
	assertStatus(t, rec, http.StatusNotImplemented)
}
