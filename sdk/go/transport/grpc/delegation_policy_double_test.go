// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The standing delegation policy operations (#1186): Put validates and
// echoes the policy, List reports one, and MintDelegation, which no
// transport serves, refuses as not configured.

func (*fakeCertEnrollmentServicer) PutDelegationPolicy(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
	p := delegation.Policy{Tenant: req.TenantID, Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}
	if err := p.Validate(); err != nil {
		return nil, &service.DelegationRequestError{Field: "policy", Reason: err.Error()}
	}
	return &p, nil
}

func (*fakeCertEnrollmentServicer) ListDelegationPolicies(_ context.Context, tenantID string) ([]delegation.Policy, error) {
	return []delegation.Policy{{Tenant: tenantID, Principal: "billing", Scope: []string{"kv:get"}}}, nil
}

// DeleteDelegationPolicy holds the one policy ListDelegationPolicies
// reports, for "billing"; any other principal has none.
func (*fakeCertEnrollmentServicer) DeleteDelegationPolicy(_ context.Context, _, principal string) error {
	if principal != "billing" {
		return &service.DelegationRequestError{Field: "principal", Reason: "no delegation policy"}
	}
	return nil
}

func (*fakeCertEnrollmentServicer) MintDelegation(context.Context, *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}
