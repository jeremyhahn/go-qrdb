// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The standing delegation policy operations (#1186): fakeCertEnrollTool
// runs its hook when a test sets one; otherwise each refuses as not
// configured.

func (f *fakeCertEnrollTool) PutDelegationPolicy(ctx context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
	if f.policyPutFn != nil {
		return f.policyPutFn(ctx, req)
	}
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (f *fakeCertEnrollTool) ListDelegationPolicies(ctx context.Context, tenantID string) ([]delegation.Policy, error) {
	if f.policyListFn != nil {
		return f.policyListFn(ctx, tenantID)
	}
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (f *fakeCertEnrollTool) DeleteDelegationPolicy(ctx context.Context, tenantID, principal string) error {
	if f.policyDeleteFn != nil {
		return f.policyDeleteFn(ctx, tenantID, principal)
	}
	return &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*fakeCertEnrollTool) MintDelegation(context.Context, *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*refusingMCPCertEnroll) PutDelegationPolicy(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*refusingMCPCertEnroll) ListDelegationPolicies(context.Context, string) ([]delegation.Policy, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*refusingMCPCertEnroll) DeleteDelegationPolicy(context.Context, string, string) error {
	return &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*refusingMCPCertEnroll) MintDelegation(context.Context, *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}
