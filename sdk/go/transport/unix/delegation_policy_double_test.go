// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package unix

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The standing delegation policy operations (#1186): each runs its hook
// when a test sets one, and refuses as not configured otherwise.

func (m *mockCertEnrollmentSvc) PutDelegationPolicy(ctx context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
	if m.policyPutFn != nil {
		return m.policyPutFn(ctx, req)
	}
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (m *mockCertEnrollmentSvc) ListDelegationPolicies(ctx context.Context, tenantID string) ([]delegation.Policy, error) {
	if m.policyListFn != nil {
		return m.policyListFn(ctx, tenantID)
	}
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (m *mockCertEnrollmentSvc) DeleteDelegationPolicy(ctx context.Context, tenantID, principal string) error {
	if m.policyDeleteFn != nil {
		return m.policyDeleteFn(ctx, tenantID, principal)
	}
	return &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*mockCertEnrollmentSvc) MintDelegation(context.Context, *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}
