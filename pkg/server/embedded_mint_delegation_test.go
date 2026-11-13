// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
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

// recordingCertEnrollment records whether MintDelegation was reached.
type recordingCertEnrollment struct {
	mockCertEnrollmentServicer
	called *bool
}

func (r recordingCertEnrollment) MintDelegation(_ context.Context, req *service.MintDelegationRequest) (*service.IssuedDelegation, error) {
	*r.called = true
	return &service.IssuedDelegation{TenantID: req.TenantID, Principal: "billing"}, nil
}

var _ service.CertEnrollmentServicer = recordingCertEnrollment{}

// TestMintDelegation_ServerRefusesADelegatedCaller: the in-process first mint
// runs no transport authorization, so a context from AuthenticateDelegation
// is refused before any service call. That one refusal covers a saved
// context whose certificate has since expired or been revoked and a context
// lacking certs:delegate alike: neither reaches the service, and renewal
// stays on IssueDelegation.
func TestMintDelegation_ServerRefusesADelegatedCaller(t *testing.T) {
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: embeddedDelegationPrincipal, Scope: []string{string(permission.KVGet)}})
	mw := authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t, "acme")), authmw.WithDelegationVerifier(d.Authenticator))
	ctx, err := serverWith(WithAuthMiddleware(mw)).AuthenticateDelegation(context.Background(), []*x509.Certificate{d.Certificate})
	if err != nil {
		t.Fatalf("AuthenticateDelegation: %v", err)
	}

	called := false
	got, err := serverWith(WithAuthMiddleware(mw), WithCertEnrollment(recordingCertEnrollment{called: &called})).MintDelegation(ctx, &service.MintDelegationRequest{TenantID: "acme"})
	var denied *service.DelegationDeniedError
	if got != nil || !errors.As(err, &denied) {
		t.Fatalf("MintDelegation = %+v, %v; want *service.DelegationDeniedError", got, err)
	}
	if called {
		t.Fatal("a delegated caller reached the service's MintDelegation")
	}
}
