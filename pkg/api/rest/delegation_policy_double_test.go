// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package rest

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// The standing delegation policy operations (#1186), which this double
// does not exercise: each refuses as not configured.

func (*fakeCertEnroll) PutDelegationPolicy(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*fakeCertEnroll) ListDelegationPolicies(context.Context, string) ([]delegation.Policy, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*fakeCertEnroll) DeleteDelegationPolicy(context.Context, string, string) error {
	return &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}

func (*fakeCertEnroll) MintDelegation(context.Context, *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	return nil, &service.CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
}
