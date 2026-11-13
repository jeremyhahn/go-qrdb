// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package quic

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// delegationPolicyWireRequest is the payload of cert.DelegationPolicyPut.
type delegationPolicyWireRequest struct {
	Principal string   `json:"principal"`
	Scope     []string `json:"scope"`
	TTL       string   `json:"ttl,omitempty"`
}

// delegationPolicyWire is a tenant's standing delegation policy on the wire.
type delegationPolicyWire struct {
	TenantID  string   `json:"tenantID"`
	Principal string   `json:"principal"`
	Scope     []string `json:"scope"`
	TTL       string   `json:"ttl"`
}

// deleteDelegationPolicyWireRequest is the payload of
// cert.DelegationPolicyDelete.
type deleteDelegationPolicyWireRequest struct {
	Principal string `json:"principal"`
}

func delegationPolicyToWire(p *delegation.Policy) delegationPolicyWire {
	return delegationPolicyWire{TenantID: p.Tenant, Principal: p.Principal, Scope: p.Scope, TTL: p.Lifetime().String()}
}

// delegationPolicyAdmin is the gate every delegation-policy handler passes:
// the enrollment service is installed and the caller holds certs:delegate in
// its own tenant, which it returns.
func (s *Server) delegationPolicyAdmin(ctx context.Context) (string, error) {
	if s.certEnroll == nil {
		return "", &ServiceNotConfiguredError{Service: "cert enrollment"}
	}
	tenantID := callerTenant(ctx)
	if err := s.authorizeCertTenant(ctx, tenantID, permission.CertsDelegate); err != nil {
		return "", err
	}
	return tenantID, nil
}

// handlePutDelegationPolicy writes the caller's own tenant's standing
// delegation policy (#1186).
func (s *Server) handlePutDelegationPolicy(ctx context.Context, payload []byte) ([]byte, error) {
	tenantID, err := s.delegationPolicyAdmin(ctx)
	if err != nil {
		return nil, err
	}
	var req delegationPolicyWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	var ttl time.Duration
	if req.TTL != "" {
		if ttl, err = time.ParseDuration(req.TTL); err != nil {
			return nil, &InvalidTTLError{Value: req.TTL, Err: err}
		}
	}
	p, err := s.certEnroll.PutDelegationPolicy(ctx, &service.DelegationPolicyRequest{
		TenantID:  tenantID,
		Principal: req.Principal,
		Scope:     req.Scope,
		TTL:       ttl,
	})
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(delegationPolicyToWire(p))
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleListDelegationPolicies lists the caller's own tenant's standing
// delegation policies.
func (s *Server) handleListDelegationPolicies(ctx context.Context, _ []byte) ([]byte, error) {
	tenantID, err := s.delegationPolicyAdmin(ctx)
	if err != nil {
		return nil, err
	}
	policies, err := s.certEnroll.ListDelegationPolicies(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	resp := struct {
		Policies []delegationPolicyWire `json:"policies"`
	}{Policies: make([]delegationPolicyWire, len(policies))}
	for i := range policies {
		resp.Policies[i] = delegationPolicyToWire(&policies[i])
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return out, nil
}

// handleDeleteDelegationPolicy revokes the caller's own tenant's standing
// delegation policy for a principal.
func (s *Server) handleDeleteDelegationPolicy(ctx context.Context, payload []byte) ([]byte, error) {
	tenantID, err := s.delegationPolicyAdmin(ctx)
	if err != nil {
		return nil, err
	}
	var req deleteDelegationPolicyWireRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}
	if err := s.certEnroll.DeleteDelegationPolicy(ctx, tenantID, req.Principal); err != nil {
		return nil, err
	}
	return []byte("{}"), nil
}
