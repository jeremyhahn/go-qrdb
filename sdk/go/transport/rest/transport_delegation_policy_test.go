// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestRESTPutDelegationPolicy_RequestAndResponseFields(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var captured transport.DelegationPolicyRequest
	rs.handle(exactPath(http.MethodPost, "/certs/delegate/policies"), func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		writeJSON(w, &transport.DelegationPolicy{TenantID: "acme", Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m0s"})
	})
	got, err := rt.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m",
	})
	if err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	if captured.Principal != "billing" || !slices.Equal(captured.Scope, []string{"kv:get"}) || captured.TTL != "15m" {
		t.Fatalf("request captured = %+v", captured)
	}
	if got.TenantID != "acme" || got.Principal != "billing" || got.TTL != "15m0s" {
		t.Fatalf("response = %+v", got)
	}
}

func TestRESTPutDelegationPolicy_RefusalIsARequestError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/certs/delegate/policies"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "permission denied", http.StatusForbidden)
	})
	_, err := rt.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{Principal: "billing"})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying 403", err, err)
	}
}

func TestRESTListDelegationPolicies_Decodes(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/certs/delegate/policies"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"policies": []transport.DelegationPolicy{{TenantID: "acme", Principal: "billing", TTL: "1h0m0s"}}})
	})
	got, err := rt.ListDelegationPolicies(context.Background())
	if err != nil {
		t.Fatalf("ListDelegationPolicies: %v", err)
	}
	if len(got) != 1 || got[0].Principal != "billing" || got[0].TTL != "1h0m0s" {
		t.Fatalf("policies = %+v", got)
	}
}

func TestRESTListDelegationPolicies_RefusalIsARequestError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/certs/delegate/policies"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "permission denied", http.StatusForbidden)
	})
	_, err := rt.ListDelegationPolicies(context.Background())
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusForbidden {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying 403", err, err)
	}
}

func TestRESTDeleteDelegationPolicy_EscapesThePrincipal(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	called := false
	rs.handle(exactPath(http.MethodDelete, "/certs/delegate/policies/billing"), func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.DeleteDelegationPolicy(context.Background(), "billing"); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if !called {
		t.Fatal("DELETE /certs/delegate/policies/billing was not called")
	}
}

func TestRESTDeleteDelegationPolicy_RefusalIsARequestError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodDelete, "/certs/delegate/policies/billing"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no policy", http.StatusNotFound)
	})
	err := rt.DeleteDelegationPolicy(context.Background(), "billing")
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v (%T); want a *transport.RequestError carrying 404", err, err)
	}
}

func TestRESTDelegationPolicies_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	var ce *transport.ClosedError
	if _, err := rt.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{}); !errors.As(err, &ce) {
		t.Fatalf("put: expected ClosedError, got %T", err)
	}
	if _, err := rt.ListDelegationPolicies(context.Background()); !errors.As(err, &ce) {
		t.Fatalf("list: expected ClosedError, got %T", err)
	}
	if err := rt.DeleteDelegationPolicy(context.Background(), "billing"); !errors.As(err, &ce) {
		t.Fatalf("delete: expected ClosedError, got %T", err)
	}
}
