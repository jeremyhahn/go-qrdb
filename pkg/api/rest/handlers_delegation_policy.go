// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// delegationPolicyRequest is the body of POST /certs/delegate/policies.
type delegationPolicyRequest struct {
	Principal string   `json:"principal"`
	Scope     []string `json:"scope"`
	TTL       string   `json:"ttl,omitempty"`
}

// delegationPolicyResponse is a tenant's standing delegation policy on the
// wire; TTL is its lifetime as a Go duration.
type delegationPolicyResponse struct {
	TenantID  string   `json:"tenantID"`
	Principal string   `json:"principal"`
	Scope     []string `json:"scope"`
	TTL       string   `json:"ttl"`
}

func delegationPolicyJSON(p *delegation.Policy) delegationPolicyResponse {
	return delegationPolicyResponse{TenantID: p.Tenant, Principal: p.Principal, Scope: p.Scope, TTL: p.Lifetime().String()}
}

// delegationPolicyAdmin is the gate every delegation-policy handler passes:
// the enrollment service is installed and the caller holds certs:delegate in
// its own tenant. It writes the refusal itself and reports whether to go on.
func (s *Server) delegationPolicyAdmin(ctx context.Context, w http.ResponseWriter) (string, bool) {
	if s.certEnrollment == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "cert enrollment"})
		return "", false
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsDelegate); err != nil {
		writeAuthzErrorJSON(w, err)
		return "", false
	}
	return tenantID, true
}

// handlePutDelegationPolicy handles POST /certs/delegate/policies -- the
// caller's own tenant writing its standing delegation policy for a service
// principal (#1186).
func (s *Server) handlePutDelegationPolicy(w http.ResponseWriter, r *http.Request) {
	var req delegationPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}
	var ttl time.Duration
	if req.TTL != "" {
		d, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeErrorJSON(w, http.StatusBadRequest, &InvalidParameterError{Param: "ttl", Err: err})
			return
		}
		ttl = d
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	tenantID, ok := s.delegationPolicyAdmin(ctx, w)
	if !ok {
		return
	}
	p, err := s.certEnrollment.PutDelegationPolicy(ctx, &service.DelegationPolicyRequest{
		TenantID:  tenantID,
		Principal: req.Principal,
		Scope:     req.Scope,
		TTL:       ttl,
	})
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(delegationPolicyJSON(p))
}

// handleListDelegationPolicies handles GET /certs/delegate/policies.
func (s *Server) handleListDelegationPolicies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	tenantID, ok := s.delegationPolicyAdmin(ctx, w)
	if !ok {
		return
	}
	policies, err := s.certEnrollment.ListDelegationPolicies(ctx, tenantID)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	out := struct {
		Policies []delegationPolicyResponse `json:"policies"`
	}{Policies: make([]delegationPolicyResponse, len(policies))}
	for i := range policies {
		out.Policies[i] = delegationPolicyJSON(&policies[i])
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleDeleteDelegationPolicy handles DELETE
// /certs/delegate/policies/{principal} -- the tenant revoking a standing
// delegation policy.
func (s *Server) handleDeleteDelegationPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()
	tenantID, ok := s.delegationPolicyAdmin(ctx, w)
	if !ok {
		return
	}
	if err := s.certEnrollment.DeleteDelegationPolicy(ctx, tenantID, r.PathValue("principal")); err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
