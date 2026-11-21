// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package cert

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
)

// prefixDelegationPolicy is the prefix a tenant's standing delegation
// policies are stored under, one record per principal:
// __sys:cert_delegation_policy/<tenant>/<principal>. The records sit beside
// the tenant's certificate records, in the tenant's home store when it has
// one, so deleting the tenant deletes them with its CA.
const prefixDelegationPolicy = "__sys:cert_delegation_policy/"

// DelegationPolicyStore holds tenants' standing delegation policies
// (delegation.Policy). The store decides nothing: the service layer
// authorizes every write and every mint, and the certificate a mint
// produces is verified by the one delegation verifier.
type DelegationPolicyStore interface {
	// PutDelegationPolicy writes p, replacing any policy the tenant holds
	// for the same principal.
	PutDelegationPolicy(ctx context.Context, p delegation.Policy) error

	// DelegationPolicy reads the tenant's policy for principal, and whether
	// one stands.
	DelegationPolicy(ctx context.Context, tenantID, principal string) (delegation.Policy, bool, error)

	// ListDelegationPolicies lists every policy the tenant holds.
	ListDelegationPolicies(ctx context.Context, tenantID string) ([]delegation.Policy, error)

	// DeleteDelegationPolicy deletes the tenant's policy for principal and
	// reports whether one stood.
	DeleteDelegationPolicy(ctx context.Context, tenantID, principal string) (bool, error)
}

// Compile-time interface check.
var _ DelegationPolicyStore = (*DefaultCertProvider)(nil)

func delegationPolicyKey(tenantID, principal string) string {
	return prefixDelegationPolicy + tenantID + "/" + principal
}

func checkPolicyKey(tenantID, principal string) error {
	if tenantID == "" {
		return &ValidationError{Field: "tenantID", Reason: "must not be empty"}
	}
	if err := delegation.ValidatePrincipal(principal); err != nil {
		return &ValidationError{Field: "principal", Reason: err.Error()}
	}
	return nil
}

// PutDelegationPolicy implements DelegationPolicyStore.
func (p *DefaultCertProvider) PutDelegationPolicy(ctx context.Context, pol delegation.Policy) error {
	if p.barrier.IsSealed() {
		return &BarrierSealedError{}
	}
	if err := pol.Validate(); err != nil {
		return &ValidationError{Field: "policy", Reason: err.Error()}
	}
	store, _, err := p.recordStore(ctx, pol.Tenant)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(pol)
	if err != nil {
		return &StoreError{Op: "marshal", Detail: "delegation policy", Err: err}
	}
	key := delegationPolicyKey(pol.Tenant, pol.Principal)
	if err := store.Put(ctx, key, payload); err != nil {
		return &StoreError{Op: "put", Detail: key, Err: err}
	}
	return nil
}

// DelegationPolicy implements DelegationPolicyStore.
func (p *DefaultCertProvider) DelegationPolicy(ctx context.Context, tenantID, principal string) (delegation.Policy, bool, error) {
	if p.barrier.IsSealed() {
		return delegation.Policy{}, false, &BarrierSealedError{}
	}
	if err := checkPolicyKey(tenantID, principal); err != nil {
		return delegation.Policy{}, false, err
	}
	store, _, err := p.recordStore(ctx, tenantID)
	if err != nil {
		return delegation.Policy{}, false, err
	}
	key := delegationPolicyKey(tenantID, principal)
	val, err := store.Get(ctx, key)
	if err != nil {
		return delegation.Policy{}, false, &StoreError{Op: "get", Detail: key, Err: err}
	}
	if val == nil {
		return delegation.Policy{}, false, nil
	}
	var pol delegation.Policy
	if err := json.Unmarshal(val, &pol); err != nil {
		return delegation.Policy{}, false, &StoreError{Op: "unmarshal", Detail: key, Err: err}
	}
	return pol, true, nil
}

// ListDelegationPolicies implements DelegationPolicyStore.
func (p *DefaultCertProvider) ListDelegationPolicies(ctx context.Context, tenantID string) ([]delegation.Policy, error) {
	if p.barrier.IsSealed() {
		return nil, &BarrierSealedError{}
	}
	if tenantID == "" {
		return nil, &ValidationError{Field: "tenantID", Reason: "must not be empty"}
	}
	store, _, err := p.recordStore(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	prefix := prefixDelegationPolicy + tenantID + "/"
	policies := []delegation.Policy{}
	if err := store.Scan(ctx, prefix, func(key string, value []byte) error {
		var pol delegation.Policy
		if err := json.Unmarshal(value, &pol); err != nil {
			return &StoreError{Op: "unmarshal", Detail: key, Err: err}
		}
		policies = append(policies, pol)
		return nil
	}); err != nil {
		return nil, &StoreError{Op: "scan", Detail: prefix, Err: err}
	}
	return policies, nil
}

// DeleteDelegationPolicy implements DelegationPolicyStore.
func (p *DefaultCertProvider) DeleteDelegationPolicy(ctx context.Context, tenantID, principal string) (bool, error) {
	if p.barrier.IsSealed() {
		return false, &BarrierSealedError{}
	}
	if err := checkPolicyKey(tenantID, principal); err != nil {
		return false, err
	}
	store, _, err := p.recordStore(ctx, tenantID)
	if err != nil {
		return false, err
	}
	// The record is found by its raw value and never decoded, so one that no
	// longer decodes as a policy can still be removed.
	key := delegationPolicyKey(tenantID, principal)
	val, err := store.Get(ctx, key)
	if err != nil {
		return false, &StoreError{Op: "get", Detail: key, Err: err}
	}
	if val == nil {
		return false, nil
	}
	if err := store.Delete(ctx, key); err != nil {
		return false, &StoreError{Op: "delete", Detail: key, Err: err}
	}
	return true, nil
}
