// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package rest

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The standing delegation policy operations (#1186): each runs its hook
// when a test sets one, and refuses as not configured otherwise.

func (f *fakeCertEnroll) PutDelegationPolicy(ctx context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
	if f.policyPutFn != nil {
		return f.policyPutFn(ctx, req)
	}
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (f *fakeCertEnroll) ListDelegationPolicies(ctx context.Context, tenantID string) ([]delegation.Policy, error) {
	if f.policyListFn != nil {
		return f.policyListFn(ctx, tenantID)
	}
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (f *fakeCertEnroll) DeleteDelegationPolicy(ctx context.Context, tenantID, principal string) error {
	if f.policyDeleteFn != nil {
		return f.policyDeleteFn(ctx, tenantID, principal)
	}
	return &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*fakeCertEnroll) MintDelegation(context.Context, *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}
