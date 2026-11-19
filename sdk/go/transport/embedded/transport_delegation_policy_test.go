// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package embedded

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestPutDelegationPolicy_Success(t *testing.T) {
	var got *service.DelegationPolicyRequest
	mock := &mockCertEnrollmentServicer{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			got = req
			return &delegation.Policy{Tenant: req.TenantID, Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	p, err := tr.PutDelegationPolicy(tenant.WithTenantID(context.Background(), "tenant-a"), &transport.DelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The tenant is always the caller's own; the request cannot name one.
	if got.TenantID != "tenant-a" || got.Principal != "billing" || !slices.Equal(got.Scope, []string{"kv:get"}) || got.TTL != 15*time.Minute {
		t.Errorf("service received %+v", got)
	}
	if p.TenantID != "tenant-a" || p.Principal != "billing" || p.TTL != "15m0s" {
		t.Errorf("policy = %+v", p)
	}
}

// A nil request is refused before the service is reached, with or without
// a tenant on the context.
func TestPutDelegationPolicy_NilRequest(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"with a tenant":    tenant.WithTenantID(context.Background(), "tenant-a"),
		"without a tenant": context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			reached := make(chan struct{}, 1)
			mock := &mockCertEnrollmentServicer{
				policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
					reached <- struct{}{}
					return &delegation.Policy{Tenant: req.TenantID, Principal: req.Principal}, nil
				},
			}
			tr := newAuthTransport(t, WithCertEnrollment(mock))
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("PutDelegationPolicy(nil) panicked: %v", r)
				}
			}()
			p, err := tr.PutDelegationPolicy(ctx, nil)
			var nilReq *transport.NilRequestError
			var reqErr *transport.RequestError
			if p != nil || !errors.As(err, &nilReq) || !errors.As(err, &reqErr) || reqErr.Method != "PutDelegationPolicy" {
				t.Fatalf("PutDelegationPolicy(nil) = %+v, %v; want a PutDelegationPolicy *transport.RequestError wrapping *transport.NilRequestError", p, err)
			}
			if len(reached) != 0 {
				t.Fatal("the nil request reached the service")
			}
		})
	}
}

func TestPutDelegationPolicy_InvalidTTL(t *testing.T) {
	tr := newAuthTransport(t, WithCertEnrollment(&mockCertEnrollmentServicer{}))
	if _, err := tr.PutDelegationPolicy(tenant.WithTenantID(context.Background(), "tenant-a"), &transport.DelegationPolicyRequest{
		Principal: "billing", Scope: []string{"kv:get"}, TTL: "soon",
	}); err == nil {
		t.Fatal("an unparseable ttl was accepted")
	}
}

func TestPutDelegationPolicy_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	_, err := tr.PutDelegationPolicy(context.Background(), &transport.DelegationPolicyRequest{Principal: "billing"})
	assertNotImplementedError(t, err, "PutDelegationPolicy")
}

func TestListDelegationPolicies_Success(t *testing.T) {
	var gotTenant string
	mock := &mockCertEnrollmentServicer{
		policyListFn: func(_ context.Context, tenantID string) ([]delegation.Policy, error) {
			gotTenant = tenantID
			return []delegation.Policy{{Tenant: tenantID, Principal: "billing", Scope: []string{"kv:get"}}}, nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	got, err := tr.ListDelegationPolicies(tenant.WithTenantID(context.Background(), "tenant-a"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "tenant-a" || len(got) != 1 || got[0].Principal != "billing" || got[0].TTL != delegation.DefaultLifetime.String() {
		t.Errorf("tenant = %q, policies = %+v", gotTenant, got)
	}
}

func TestListDelegationPolicies_ServiceError(t *testing.T) {
	tr := newAuthTransport(t, WithCertEnrollment(&mockCertEnrollmentServicer{}))
	_, err := tr.ListDelegationPolicies(tenant.WithTenantID(context.Background(), "tenant-a"))
	var nc *service.CertEnrollmentNotConfiguredError
	if !errors.As(err, &nc) {
		t.Fatalf("err = %v (%T); want the service's refusal", err, err)
	}
}

func TestDeleteDelegationPolicy_Success(t *testing.T) {
	var gotTenant, gotPrincipal string
	mock := &mockCertEnrollmentServicer{
		policyDeleteFn: func(_ context.Context, tenantID, principal string) error {
			gotTenant, gotPrincipal = tenantID, principal
			return nil
		},
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	if err := tr.DeleteDelegationPolicy(tenant.WithTenantID(context.Background(), "tenant-a"), "billing"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "tenant-a" || gotPrincipal != "billing" {
		t.Errorf("service received tenant=%q principal=%q", gotTenant, gotPrincipal)
	}
}

func TestDeleteDelegationPolicy_ServiceError(t *testing.T) {
	absent := &service.DelegationRequestError{Field: "principal", Reason: "no delegation policy"}
	mock := &mockCertEnrollmentServicer{
		policyDeleteFn: func(context.Context, string, string) error { return absent },
	}
	tr := newAuthTransport(t, WithCertEnrollment(mock))

	if err := tr.DeleteDelegationPolicy(tenant.WithTenantID(context.Background(), "tenant-a"), "billing"); !errors.Is(err, absent) {
		t.Fatalf("err = %v (%T); want the service's refusal", err, err)
	}
}

func TestDeleteDelegationPolicy_NilService(t *testing.T) {
	tr := newAuthTransport(t)
	err := tr.DeleteDelegationPolicy(context.Background(), "billing")
	assertNotImplementedError(t, err, "DeleteDelegationPolicy")
}

// countingPolicyService is a cert enrollment double that counts every
// delegation policy call that reaches it.
func countingPolicyService(calls *int) *mockCertEnrollmentServicer {
	return &mockCertEnrollmentServicer{
		policyPutFn: func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			*calls++
			return &delegation.Policy{Tenant: req.TenantID, Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
		},
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			*calls++
			return nil, nil
		},
		policyDeleteFn: func(context.Context, string, string) error {
			*calls++
			return nil
		},
	}
}

// delegationPolicyOps drives each delegation policy method through tr.
func delegationPolicyOps() map[string]func(context.Context, *Transport) error {
	return map[string]func(context.Context, *Transport) error{
		"Put": func(ctx context.Context, tr *Transport) error {
			_, err := tr.PutDelegationPolicy(ctx, &transport.DelegationPolicyRequest{Principal: "billing", Scope: []string{"kv:get"}, TTL: "15m"})
			return err
		},
		"List": func(ctx context.Context, tr *Transport) error {
			_, err := tr.ListDelegationPolicies(ctx)
			return err
		},
		"Delete": func(ctx context.Context, tr *Transport) error {
			return tr.DeleteDelegationPolicy(ctx, "billing")
		},
	}
}

func TestDelegationPolicy_CertsDelegateAdmitted(t *testing.T) {
	for name, op := range delegationPolicyOps() {
		t.Run(name, func(t *testing.T) {
			var calls int
			tr := newAuthTransport(t, WithCertEnrollment(countingPolicyService(&calls)),
				WithAuthMiddleware(newGrantingAuthMW(permission.CertsDelegate)))

			if err := op(identityCtx("tenant-a", "member"), tr); err != nil {
				t.Fatalf("a caller holding certs:delegate was refused: %v", err)
			}
			if calls != 1 {
				t.Errorf("service calls = %d, want 1", calls)
			}
		})
	}
}

func TestDelegationPolicy_WithoutCertsDelegateRefused(t *testing.T) {
	for name, op := range delegationPolicyOps() {
		t.Run(name, func(t *testing.T) {
			var calls int
			// Every other certificate grant, but not certs:delegate.
			tr := newAuthTransport(t, WithCertEnrollment(countingPolicyService(&calls)),
				WithAuthMiddleware(newGrantingAuthMW(permission.CertsIssue, permission.CertsEnroll, permission.CertsRevoke, permission.CertsList)))

			err := op(identityCtx("tenant-a", "member"), tr)
			if !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("err = %v, want %v", err, auth.ErrUnauthorized)
			}
			if calls != 0 {
				t.Errorf("a refused call reached the service %d time(s)", calls)
			}
		})
	}
}
