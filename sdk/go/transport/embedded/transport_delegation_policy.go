// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package embedded

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func delegationPolicyToTransport(p *delegation.Policy) transport.DelegationPolicy {
	return transport.DelegationPolicy{TenantID: p.Tenant, Principal: p.Principal, Scope: p.Scope, TTL: p.Lifetime().String()}
}

// delegationPolicyAdmin admits method and authorizes certs:delegate in the
// caller's own tenant, which it returns with the admitted context.
func (t *Transport) delegationPolicyAdmin(ctx context.Context, method, verb string) (context.Context, string, error) {
	ctx, err := t.admit(ctx, method)
	if err != nil {
		return nil, "", err
	}
	if t.certEnroll == nil {
		return nil, "", &transport.NotImplementedError{Method: method}
	}
	tenantID, err := tenant.Confine(ctx, "", verb)
	if err != nil {
		return nil, "", err
	}
	if err := t.authorizeCertTenant(ctx, tenantID, permission.CertsDelegate); err != nil {
		return nil, "", err
	}
	return ctx, tenantID, nil
}

// PutDelegationPolicy implements transport.CertClient.
func (t *Transport) PutDelegationPolicy(ctx context.Context, req *transport.DelegationPolicyRequest) (*transport.DelegationPolicy, error) {
	ctx, tenantID, err := t.delegationPolicyAdmin(ctx, "PutDelegationPolicy", "write a delegation policy")
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "PutDelegationPolicy", Err: &transport.NilRequestError{}}
	}
	var ttl time.Duration
	if req.TTL != "" {
		if ttl, err = time.ParseDuration(req.TTL); err != nil {
			return nil, err
		}
	}
	p, err := t.certEnroll.PutDelegationPolicy(ctx, &service.DelegationPolicyRequest{
		TenantID:  tenantID,
		Principal: req.Principal,
		Scope:     req.Scope,
		TTL:       ttl,
	})
	if err != nil {
		return nil, err
	}
	out := delegationPolicyToTransport(p)
	return &out, nil
}

// ListDelegationPolicies implements transport.CertClient.
func (t *Transport) ListDelegationPolicies(ctx context.Context) ([]transport.DelegationPolicy, error) {
	ctx, tenantID, err := t.delegationPolicyAdmin(ctx, "ListDelegationPolicies", "list delegation policies")
	if err != nil {
		return nil, err
	}
	policies, err := t.certEnroll.ListDelegationPolicies(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]transport.DelegationPolicy, len(policies))
	for i := range policies {
		out[i] = delegationPolicyToTransport(&policies[i])
	}
	return out, nil
}

// DeleteDelegationPolicy implements transport.CertClient.
func (t *Transport) DeleteDelegationPolicy(ctx context.Context, principal string) error {
	ctx, tenantID, err := t.delegationPolicyAdmin(ctx, "DeleteDelegationPolicy", "delete a delegation policy")
	if err != nil {
		return err
	}
	return t.certEnroll.DeleteDelegationPolicy(ctx, tenantID, principal)
}
