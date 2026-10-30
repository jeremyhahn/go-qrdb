// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mintingCertEnrollment answers MintDelegation with the tenant it was asked
// for, so the test sees the request reach the service unchanged.
type mintingCertEnrollment struct {
	mockCertEnrollmentServicer
}

func (mintingCertEnrollment) MintDelegation(_ context.Context, req *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	return &service.IssuedDelegation{TenantID: req.TenantID, Principal: "billing"}, nil
}

var _ service.CertEnrollmentServicer = mintingCertEnrollment{}

func TestMintDelegation_ServerHandsTheRequestToTheService(t *testing.T) {
	got, err := serverWith(WithCertEnrollment(mintingCertEnrollment{})).MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: "acme"})
	if err != nil || got.TenantID != "acme" || got.Principal != "billing" {
		t.Fatalf("MintDelegation = %+v, %v", got, err)
	}
}

func TestMintDelegation_ServerWithoutEnrollmentIsNotConfigured(t *testing.T) {
	var notConfigured *service.CertEnrollmentNotConfiguredError
	if got, err := serverWith().MintDelegation(context.Background(), &service.MintDelegationRequest{TenantID: "acme"}); got != nil || !errors.As(err, &notConfigured) {
		t.Fatalf("MintDelegation = %+v, %v; want *service.CertEnrollmentNotConfiguredError", got, err)
	}
}
