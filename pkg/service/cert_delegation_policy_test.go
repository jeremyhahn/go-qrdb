// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

var policyScope = []string{"certs:delegate", "kv:get", "kv:put"}

// Exported for the external delegated-caller tests, which need authtest,
// and authtest imports this package.
// newPolicyService is a delegation service whose CA also keeps the
// tenants' standing delegation policies, as the node wires it.
func newPolicyService(t *testing.T, opts ...CertEnrollmentOption) *CertEnrollmentService {
	t.Helper()
	p := newCertEnrollmentProvider(t)
	store, ok := any(p).(cert.DelegationPolicyStore)
	if !ok {
		t.Fatalf("%T keeps no delegation policies", p)
	}
	return NewCertEnrollmentService(p, nil, nil, append([]CertEnrollmentOption{WithDelegationPolicyStore(store)}, opts...)...)
}

func billingPolicy() *DelegationPolicyRequest {
	return &DelegationPolicyRequest{Principal: "billing", Scope: slices.Clone(policyScope), TTL: 30 * time.Minute}
}

// servicePrincipalCtx is the node's in-process service principal, which
// holds the system tenant (Node.ServiceContext).
func servicePrincipalCtx(name string) context.Context {
	id := auth.NewServicePrincipal(name, tenantid.System, nil)
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), tenantid.System)
}

func TestDelegationPolicy_CRUD(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	svc := newPolicyService(t, WithEnrollmentAuditLogger(ring))
	admin := anchorCaller("acme", "alice")

	got, err := svc.PutDelegationPolicy(admin, billingPolicy())
	if err != nil || got.Tenant != "acme" || got.Principal != "billing" || !slices.Equal(got.Scope, policyScope) {
		t.Fatalf("PutDelegationPolicy = %+v, %v", got, err)
	}
	if ev := findEvent(ring.Snapshot(), ActionCertDelegationPolicyPut); ev == nil || ev.TenantID != "acme" || ev.Outcome != audit.OutcomeSuccess {
		t.Fatalf("%s event = %+v", ActionCertDelegationPolicyPut, ev)
	}
	list, err := svc.ListDelegationPolicies(admin, "")
	if err != nil || len(list) != 1 || list[0].Principal != "billing" {
		t.Fatalf("ListDelegationPolicies = %+v, %v", list, err)
	}
	if err := svc.DeleteDelegationPolicy(admin, "", "billing"); err != nil {
		t.Fatalf("DeleteDelegationPolicy: %v", err)
	}
	if ev := findEvent(ring.Snapshot(), ActionCertDelegationPolicyDelete); ev == nil || ev.TenantID != "acme" {
		t.Fatalf("%s event = %+v", ActionCertDelegationPolicyDelete, ev)
	}
	var notFound *DelegationPolicyNotFoundError
	if err := svc.DeleteDelegationPolicy(admin, "", "billing"); !errors.As(err, &notFound) || notFound.Error() == "" {
		t.Fatalf("deleting it again: err = %v, want *DelegationPolicyNotFoundError", err)
	}
	var field *CertEnrollmentFieldError
	if err := svc.DeleteDelegationPolicy(admin, "", ""); !errors.As(err, &field) {
		t.Fatalf("delete with no principal: err = %v, want *CertEnrollmentFieldError", err)
	}
	if _, err := svc.PutDelegationPolicy(admin, nil); !errors.As(err, &field) {
		t.Fatalf("nil put: err = %v, want *CertEnrollmentFieldError", err)
	}
}

func TestDelegationPolicy_RefusesBadWriters(t *testing.T) {
	svc := newPolicyService(t)
	var cross *tenant.CrossTenantDeniedError
	req := billingPolicy()
	req.TenantID = "globex"
	if _, err := svc.PutDelegationPolicy(anchorCaller("acme", "alice"), req); !errors.As(err, &cross) {
		t.Fatalf("another tenant's policy: err = %v, want *tenant.CrossTenantDeniedError", err)
	}
	var denied *DelegationDeniedError
	if _, err := svc.ListDelegationPolicies(servicePrincipalCtx("billing"), ""); !errors.As(err, &denied) {
		t.Fatalf("the system tenant listing: err = %v, want *DelegationDeniedError", err)
	}
	var reqErr *DelegationRequestError
	for name, mutate := range map[string]func(*DelegationPolicyRequest){
		"past the maximum lifetime": func(r *DelegationPolicyRequest) { r.TTL = delegation.MaxLifetime + time.Second },
		"a built-in role":           func(r *DelegationPolicyRequest) { r.Principal = "admin" },
		"an unknown permission":     func(r *DelegationPolicyRequest) { r.Scope = []string{"kv:levitate"} },
	} {
		r := billingPolicy()
		mutate(r)
		if _, err := svc.PutDelegationPolicy(anchorCaller("acme", "alice"), r); !errors.As(err, &reqErr) {
			t.Fatalf("%s: err = %v, want *DelegationRequestError", name, err)
		}
	}
	var notConfigured *CertEnrollmentNotConfiguredError
	bare := newDelegationService(t)
	if _, err := bare.PutDelegationPolicy(anchorCaller("acme", "alice"), billingPolicy()); !errors.As(err, &notConfigured) {
		t.Fatalf("no policy store: err = %v, want *CertEnrollmentNotConfiguredError", err)
	}
	if _, err := bare.MintDelegation(servicePrincipalCtx("billing"), &MintDelegationRequest{TenantID: "acme", CSRPEM: delegationCSR(t)}); !errors.As(err, &notConfigured) {
		t.Fatalf("mint with no policy store: err = %v, want *CertEnrollmentNotConfiguredError", err)
	}
}

func TestMintDelegation_OnlyThePrincipal(t *testing.T) {
	svc := newPolicyService(t)
	if _, err := svc.PutDelegationPolicy(anchorCaller("acme", "alice"), billingPolicy()); err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	var denied *DelegationDeniedError
	var notFound *DelegationPolicyNotFoundError
	var field *CertEnrollmentFieldError
	csr := delegationCSR(t)
	if _, err := svc.MintDelegation(anchorCaller("acme", "alice"), &MintDelegationRequest{CSRPEM: csr}); !errors.As(err, &denied) {
		t.Fatalf("a tenant user minting: err = %v, want *DelegationDeniedError", err)
	}
	if _, err := svc.MintDelegation(servicePrincipalCtx("billing"), &MintDelegationRequest{CSRPEM: csr}); !errors.As(err, &denied) {
		t.Fatalf("the service principal naming no tenant: err = %v, want *DelegationDeniedError", err)
	}
	if _, err := svc.MintDelegation(servicePrincipalCtx("reports"), &MintDelegationRequest{TenantID: "acme", CSRPEM: csr}); !errors.As(err, &notFound) {
		t.Fatalf("a principal the policy does not name: err = %v, want *DelegationPolicyNotFoundError", err)
	}
	if _, err := svc.MintDelegation(servicePrincipalCtx("billing"), &MintDelegationRequest{TenantID: "acme"}); !errors.As(err, &field) {
		t.Fatalf("no CSR: err = %v, want *CertEnrollmentFieldError", err)
	}
	if _, err := svc.MintDelegation(servicePrincipalCtx("billing"), &MintDelegationRequest{TenantID: "acme", CSRPEM: []byte("not a csr")}); err == nil {
		t.Fatal("a malformed CSR was minted")
	}
	if _, err := NewCertEnrollmentService(nil, nil, nil).MintDelegation(servicePrincipalCtx("billing"), &MintDelegationRequest{TenantID: "acme", CSRPEM: csr}); err == nil {
		t.Fatal("minted with no CA")
	}
}

func TestMintDelegation_FailClosedOnAuditRefusal(t *testing.T) {
	svc := newPolicyService(t)
	if _, err := svc.PutDelegationPolicy(anchorCaller("acme", "alice"), billingPolicy()); err != nil {
		t.Fatalf("PutDelegationPolicy: %v", err)
	}
	WithEnrollmentAuditLogger(&refusingLogger{})(svc)
	var gate *audit.GateError
	if got, err := svc.MintDelegation(servicePrincipalCtx("billing"), &MintDelegationRequest{TenantID: "acme", CSRPEM: delegationCSR(t)}); got != nil || !errors.As(err, &gate) {
		t.Fatalf("mint with a refusing trail = %+v, %v; want *audit.GateError", got, err)
	}
	if _, err := svc.PutDelegationPolicy(anchorCaller("acme", "alice"), billingPolicy()); !errors.As(err, &gate) {
		t.Fatalf("put with a refusing trail: err = %v, want *audit.GateError", err)
	}
	if err := svc.DeleteDelegationPolicy(anchorCaller("acme", "alice"), "", "billing"); !errors.As(err, &gate) {
		t.Fatalf("delete with a refusing trail: err = %v, want *audit.GateError", err)
	}
}
