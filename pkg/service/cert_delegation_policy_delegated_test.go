// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// delegatedCtx is a caller presenting a delegation of g.Tenant.
func delegatedCtx(t *testing.T, g delegation.Grant) context.Context {
	t.Helper()
	id := authtest.NewDelegation(t, g).Identity(t)
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), g.Tenant)
}

func mintedGrant(t *testing.T, certPEM []byte) (delegation.Grant, *x509.Certificate) {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("minted certificate is not PEM")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	g, found, err := delegation.FromCertificate(leaf)
	if !found || err != nil {
		t.Fatalf("minted certificate carries no delegation: found %v, err %v", found, err)
	}
	return g, leaf
}

func TestDelegationPolicy_ADelegatedCallerNeverWritesIt(t *testing.T) {
	svc := service.NewPolicyServiceForTest(t)
	delegated := delegatedCtx(t, delegation.Grant{Tenant: "acme", Principal: "billing", Scope: []string{"certs:delegate"}})
	var denied *service.DelegationDeniedError
	if _, err := svc.PutDelegationPolicy(delegated, service.BillingPolicyForTest()); !errors.As(err, &denied) {
		t.Fatalf("a delegated writer: err = %v, want *service.DelegationDeniedError", err)
	}
	if err := svc.DeleteDelegationPolicy(delegated, "", "billing"); !errors.As(err, &denied) {
		t.Fatalf("a delegated deleter: err = %v, want *service.DelegationDeniedError", err)
	}
	var cross *tenant.CrossTenantDeniedError
	if _, err := svc.MintDelegation(delegated, &service.MintDelegationRequest{TenantID: "globex", CSRPEM: service.DelegationCSRForTest(t)}); !errors.As(err, &cross) {
		t.Fatalf("a delegation minting in another tenant: err = %v, want *tenant.CrossTenantDeniedError", err)
	}
}

func TestMintDelegation_WhileThePolicyStands(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	svc := service.NewPolicyServiceForTest(t, service.WithEnrollmentAuditLogger(ring))
	admin := service.AnchorCallerForTest("acme", "alice")
	sp := service.ServicePrincipalCtxForTest("billing")
	csr := service.DelegationCSRForTest
	mint := &service.MintDelegationRequest{TenantID: "acme", CSRPEM: csr(t)}

	var notFound *service.DelegationPolicyNotFoundError
	if _, err := svc.MintDelegation(sp, mint); !errors.As(err, &notFound) {
		t.Fatalf("mint before the policy: err = %v, want *service.DelegationPolicyNotFoundError", err)
	}
	policy := service.BillingPolicyForTest()
	if _, err := svc.PutDelegationPolicy(admin, policy); err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}

	// The first mint, in process: the whole policy.
	issued, err := svc.MintDelegation(sp, mint)
	if err != nil {
		t.Fatalf("MintDelegation: %v", err)
	}
	g, leaf := mintedGrant(t, issued.CertPEM)
	if g.Tenant != "acme" || g.Principal != "billing" || !slices.Equal(g.Scope, policy.Scope) || leaf.NotAfter.Sub(leaf.NotBefore) != policy.TTL {
		t.Fatalf("minted %+v for %s", g, leaf.NotAfter.Sub(leaf.NotBefore))
	}
	if ev := service.FindEventForTest(ring.Snapshot(), service.ActionCertDelegationMint); ev == nil || ev.TenantID != "acme" || ev.Attributes["principal"] != "billing" {
		t.Fatalf("%s event = %+v", service.ActionCertDelegationMint, ev)
	}

	// The renewal, by the delegation itself through IssueDelegation: within
	// the policy it narrows, beyond it it is refused.
	renewing := delegatedCtx(t, g)
	renewed, err := svc.IssueDelegation(renewing, &service.IssueDelegationRequest{Scope: []string{"kv:get"}, TTL: 10 * time.Minute, CSRPEM: csr(t)})
	if err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if rg, rleaf := mintedGrant(t, renewed.CertPEM); !slices.Equal(rg.Scope, []string{"kv:get"}) || rleaf.NotAfter.Sub(rleaf.NotBefore) != 10*time.Minute {
		t.Fatalf("renewed %+v", rg)
	}
	var reqErr *service.DelegationRequestError
	for name, req := range map[string]*service.IssueDelegationRequest{
		"a wider scope": {Scope: []string{"kv:delete"}, CSRPEM: csr(t)},
		"a longer life": {TTL: policy.TTL + time.Minute, CSRPEM: csr(t)},
	} {
		if _, err := svc.IssueDelegation(renewing, req); !errors.As(err, &reqErr) {
			t.Fatalf("renewal with %s: err = %v, want *service.DelegationRequestError", name, err)
		}
	}

	// Revoked by deleting the policy: the next mint, either way, is refused.
	if err := svc.DeleteDelegationPolicy(admin, "", "billing"); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if _, err := svc.MintDelegation(sp, mint); !errors.As(err, &notFound) {
		t.Fatalf("mint after the delete: err = %v, want *service.DelegationPolicyNotFoundError", err)
	}
	if _, err := svc.IssueDelegation(renewing, &service.IssueDelegationRequest{CSRPEM: csr(t)}); !errors.As(err, &notFound) {
		t.Fatalf("renewal after the delete: err = %v, want *service.DelegationPolicyNotFoundError", err)
	}
}
