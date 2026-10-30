// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

const (
	// ActionCertDelegationPolicyPut records PutDelegationPolicy: a tenant
	// writing a standing delegation policy.
	ActionCertDelegationPolicyPut = "cert.delegation_policy.put"

	// ActionCertDelegationPolicyDelete records DeleteDelegationPolicy: a
	// tenant revoking a standing delegation policy.
	ActionCertDelegationPolicyDelete = "cert.delegation_policy.delete"

	// ActionCertDelegationMint records a delegation certificate minted under
	// a standing policy, on the principal's own request.
	ActionCertDelegationMint = "cert.delegation.mint"
)

// WithDelegationPolicyStore installs the store the tenants' standing
// delegation policies are kept in. Without one, every policy operation and
// every mint is refused as not configured.
func WithDelegationPolicyStore(store cert.DelegationPolicyStore) CertEnrollmentOption {
	return func(s *CertEnrollmentService) { s.policies = store }
}

// DelegationPolicyRequest writes TenantID's standing delegation policy for
// Principal.
type DelegationPolicyRequest struct {
	// TenantID is the delegating tenant; empty is the caller's own, and any
	// other is refused.
	TenantID string

	// Principal is the embedder's service principal the policy names.
	Principal string

	// Scope is the most a certificate minted under the policy may delegate.
	Scope []string

	// TTL is the longest a minted certificate may live; zero is
	// delegation.DefaultLifetime, and more than delegation.MaxLifetime is
	// refused.
	TTL time.Duration
}

// MintDelegationRequest is the policy's principal asking for a delegation
// certificate in TenantID.
type MintDelegationRequest struct {
	// TenantID is the delegating tenant. A delegated caller's is its own;
	// the node's service principal names it.
	TenantID string

	// Scope narrows the policy's scope; empty is the whole of it.
	Scope []string

	// CSRPEM is the principal's certificate signing request, proving
	// possession of the key the certificate is issued for.
	CSRPEM []byte

	// TTL narrows the policy's lifetime; zero is the whole of it.
	TTL time.Duration
}

// DelegationPolicyNotFoundError reports that no standing delegation policy
// names the principal in the tenant: nothing is minted, and nothing is
// deleted.
type DelegationPolicyNotFoundError struct {
	TenantID  string
	Principal string
}

// Error implements error.
func (e *DelegationPolicyNotFoundError) Error() string {
	return "service: no delegation policy stands for " + e.Principal + " in tenant " + e.TenantID
}

// policyAdmin confines a policy write, read or delete to the caller's own
// tenant, which is neither the system tenant nor reached through a
// delegation: a delegated caller can never write the policy it mints under.
func (s *CertEnrollmentService) policyAdmin(ctx context.Context, tenantID, verb string) (string, error) {
	if s.policies == nil {
		return "", &CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
	}
	own, err := tenant.Confine(ctx, tenantID, verb)
	if err != nil {
		return "", err
	}
	if tenantid.IsSystem(own) {
		return "", &DelegationDeniedError{Reason: "the system tenant cannot delegate"}
	}
	if _, delegated := auth.DelegationOf(auth.IdentityFromContext(ctx)); delegated {
		return "", &DelegationDeniedError{Reason: "a delegated caller cannot write a delegation policy"}
	}
	return own, nil
}

// PutDelegationPolicy writes the caller's tenant's standing delegation
// policy for req.Principal, replacing any it holds. Its permission is
// certs:delegate, the one issuing a delegation takes, and it is audited to
// the tenant's trail before it is written.
func (s *CertEnrollmentService) PutDelegationPolicy(ctx context.Context, req *DelegationPolicyRequest) (*delegation.Policy, error) {
	if req == nil {
		return nil, &CertEnrollmentFieldError{Field: "principal"}
	}
	own, err := s.policyAdmin(ctx, req.TenantID, "write a delegation policy")
	if err != nil {
		return nil, err
	}
	p := delegation.Policy{Tenant: own, Principal: req.Principal, Scope: slices.Clone(req.Scope), TTL: req.TTL}
	if err := validateDelegationRequest(p.Grant(), p.TTL); err != nil {
		return nil, err
	}
	ev := certEnrollmentEvent(ctx, own, ActionCertDelegationPolicyPut, map[string]any{
		"principal": p.Principal,
		"scope":     slices.Clone(p.Scope),
		"ttl":       p.Lifetime().String(),
	})
	if err := s.gateCertAudit(ctx, ev); err != nil {
		return nil, err
	}
	err = s.policies.PutDelegationPolicy(ctx, p)
	s.settleCertAudit(ctx, ev, err)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListDelegationPolicies lists the caller's tenant's standing delegation
// policies.
func (s *CertEnrollmentService) ListDelegationPolicies(ctx context.Context, tenantID string) ([]delegation.Policy, error) {
	own, err := s.policyAdmin(ctx, tenantID, "list delegation policies")
	if err != nil {
		return nil, err
	}
	return s.policies.ListDelegationPolicies(ctx, own)
}

// DeleteDelegationPolicy revokes the caller's tenant's standing delegation
// policy for principal: no certificate is minted under it from then on.
// Certificates already minted stay valid until they expire, at most the
// policy's lifetime, unless the tenant revokes them by serial.
func (s *CertEnrollmentService) DeleteDelegationPolicy(ctx context.Context, tenantID, principal string) error {
	own, err := s.policyAdmin(ctx, tenantID, "delete a delegation policy")
	if err != nil {
		return err
	}
	if principal == "" {
		return &CertEnrollmentFieldError{Field: "principal"}
	}
	ev := certEnrollmentEvent(ctx, own, ActionCertDelegationPolicyDelete, map[string]any{"principal": principal})
	if err := s.gateCertAudit(ctx, ev); err != nil {
		return err
	}
	deleted, err := s.policies.DeleteDelegationPolicy(ctx, own, principal)
	if err == nil && !deleted {
		err = &DelegationPolicyNotFoundError{TenantID: own, Principal: principal}
	}
	s.settleCertAudit(ctx, ev, err)
	return err
}

// MintDelegation issues a delegation certificate under the standing policy
// that names the caller. The caller is the policy's principal: in process,
// the node's service principal (Node.ServiceContext), naming the tenant;
// over any transport, a current delegation of that tenant renewing itself,
// the one way a delegation may, and only within the policy. The policy
// bounds the scope and the lifetime, the tenant's own login CA signs
// through the one delegation issuer, and the result is verified, wherever
// it is presented, by the one delegation verifier.
func (s *CertEnrollmentService) MintDelegation(ctx context.Context, req *MintDelegationRequest) (*IssuedDelegation, error) {
	if s.certs == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "certificate issuance"}
	}
	if s.policies == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "delegation policies"}
	}
	if req == nil || len(req.CSRPEM) == 0 {
		return nil, &CertEnrollmentFieldError{Field: "csr_pem"}
	}
	own, principal, err := mintCaller(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	policy, found, err := s.policies.DelegationPolicy(ctx, own, principal)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &DelegationPolicyNotFoundError{TenantID: own, Principal: principal}
	}
	g, ttl, err := policy.Bound(req.Scope, req.TTL)
	if err != nil {
		return nil, &DelegationRequestError{Field: "policy", Reason: "the request exceeds the tenant's delegation policy", Err: err}
	}
	if err := validateDelegationRequest(g, ttl); err != nil {
		return nil, err
	}
	pub, err := cert.ParseCSR(req.CSRPEM)
	if err != nil {
		return nil, err
	}

	ev := certEnrollmentEvent(ctx, own, ActionCertDelegationMint, map[string]any{
		"principal": g.Principal,
		"scope":     slices.Clone(g.Scope),
		"ttl":       ttl.String(),
	})
	if err := s.gateCertAudit(ctx, ev); err != nil {
		return nil, err
	}
	issued, err := s.certs.IssueClientCert(ctx, &cert.IssueRequest{
		Username:   auth.ServicePrincipalSubjectPrefix + g.Principal,
		TenantID:   own,
		PublicKey:  pub,
		TTL:        ttl,
		Delegation: &g,
	})
	s.settleCertAudit(ctx, ev, err)
	if err != nil {
		return nil, err
	}
	return &IssuedDelegation{
		TenantID:  own,
		Principal: g.Principal,
		Scope:     g.Scope,
		CertPEM:   issued.CertPEM,
		Serial:    issued.Serial,
		NotAfter:  issued.NotAfter,
	}, nil
}

// mintCaller resolves the tenant and principal of a mint request from the
// caller alone. A delegated caller mints only in its own delegating tenant
// and only for its own principal; the node's in-process service principal,
// which holds the system tenant, names the tenant it mints in. Anyone else
// is not the policy's principal and is refused.
func mintCaller(ctx context.Context, tenantID string) (string, string, error) {
	id := auth.IdentityFromContext(ctx)
	if d, delegated := auth.DelegationOf(id); delegated {
		own, err := tenant.Confine(ctx, tenantID, "mint a delegation")
		if err != nil {
			return "", "", err
		}
		return own, d.Principal, nil
	}
	name, isService := "", false
	if id != nil {
		name, isService = id.Metadata[auth.ServicePrincipalMetadataKey]
	}
	if !isService || name == "" || !tenantid.IsSystem(id.TenantID) {
		return "", "", &DelegationDeniedError{Reason: "only the policy's principal mints a delegation under it"}
	}
	if tenantID == "" || tenantid.IsSystem(tenantID) {
		return "", "", &DelegationDeniedError{Reason: "the system tenant cannot delegate"}
	}
	return tenantID, name, nil
}
