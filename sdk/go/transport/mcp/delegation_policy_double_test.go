// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The standing delegation policy operations (#1186), which this double
// does not exercise: each refuses as not configured.

func (m *mockCertEnrollmentServicer) PutDelegationPolicy(ctx context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
	if m.policyPutFn != nil {
		return m.policyPutFn(ctx, req)
	}
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (m *mockCertEnrollmentServicer) ListDelegationPolicies(ctx context.Context, tenantID string) ([]delegation.Policy, error) {
	if m.policyListFn != nil {
		return m.policyListFn(ctx, tenantID)
	}
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (m *mockCertEnrollmentServicer) DeleteDelegationPolicy(ctx context.Context, tenantID, principal string) error {
	if m.policyDeleteFn != nil {
		return m.policyDeleteFn(ctx, tenantID, principal)
	}
	return &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*mockCertEnrollmentServicer) MintDelegation(context.Context, *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}
