// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"context"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ActionCertDelegate records IssueDelegation: a delegation certificate issued
// by a tenant's own CA to a service principal.
const ActionCertDelegate = "cert.delegate"

// IssueDelegationRequest asks a tenant's own CA for a delegation certificate:
// Principal may act in TenantID with at most Scope, for TTL.
type IssueDelegationRequest struct {
	// TenantID is the delegating tenant; empty is the caller's own, and any
	// other is refused.
	TenantID string

	// Principal is the embedder's service principal the delegation names.
	Principal string

	// Scope is the set of permissions delegated. A request made under the
	// delegation holds Scope intersected with the principal's own grants.
	Scope []string

	// CSRPEM is the principal's certificate signing request, proving
	// possession of the key the certificate is issued for. Its subject is
	// ignored.
	CSRPEM []byte

	// TTL is the delegation's lifetime; zero is delegation.DefaultLifetime,
	// and more than delegation.MaxLifetime is refused.
	TTL time.Duration
}

// IssuedDelegation is a delegation certificate a tenant's CA issued.
type IssuedDelegation struct {
	TenantID  string
	Principal string
	Scope     []string
	CertPEM   []byte
	Serial    string
	NotAfter  time.Time
}

// IssueDelegation implements CertEnrollmentServicer.
//
// The delegation is the tenant's own act: it is confined to the caller's
// tenant (tenant.Confine), the system tenant can never issue one, and a
// caller that is itself delegated cannot issue another -- a delegation that
// could renew itself would have no lifetime. Everything is judged before the
// gating audit record is written and anything is signed.
func (s *CertEnrollmentService) IssueDelegation(ctx context.Context, req *IssueDelegationRequest) (*IssuedDelegation, error) {
	if s.certs == nil {
		return nil, &CertEnrollmentNotConfiguredError{Capability: "certificate issuance"}
	}
	if req == nil || len(req.CSRPEM) == 0 {
		return nil, &CertEnrollmentFieldError{Field: "csr_pem"}
	}
	own, err := tenant.Confine(ctx, req.TenantID, "issue a delegation")
	if err != nil {
		return nil, err
	}
	if tenantid.IsSystem(own) {
		return nil, &DelegationDeniedError{Reason: "the system tenant cannot delegate"}
	}
	if _, delegated := auth.DelegationOf(auth.IdentityFromContext(ctx)); delegated {
		return nil, &DelegationDeniedError{Reason: "a delegated caller cannot issue a delegation"}
	}
	g := delegation.Grant{Tenant: own, Principal: req.Principal, Scope: slices.Clone(req.Scope)}
	if err := validateDelegationRequest(g, req.TTL); err != nil {
		return nil, err
	}
	pub, err := cert.ParseCSR(req.CSRPEM)
	if err != nil {
		return nil, err
	}

	ev := certEnrollmentEvent(ctx, own, ActionCertDelegate, map[string]any{
		"principal": g.Principal,
		"scope":     slices.Clone(g.Scope),
	})
	if err := s.gateCertAudit(ctx, ev); err != nil {
		return nil, err
	}
	issued, err := s.certs.IssueClientCert(ctx, &cert.IssueRequest{
		Username:   auth.ServicePrincipalSubjectPrefix + g.Principal,
		TenantID:   own,
		PublicKey:  pub,
		TTL:        req.TTL,
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

// validateDelegationRequest refuses a grant a tenant may not issue: a
// malformed one, a principal named for a built-in role (it would hold that
// whole role), a scope naming a permission this node does not know, or a
// lifetime outside (0, delegation.MaxLifetime].
func validateDelegationRequest(g delegation.Grant, ttl time.Duration) error {
	if ttl < 0 || ttl > delegation.MaxLifetime {
		return &DelegationRequestError{Field: "ttl", Reason: "must be at most " + delegation.MaxLifetime.String()}
	}
	if permission.IsBuiltinRole(g.Principal) {
		return &DelegationRequestError{Field: "principal", Reason: g.Principal + " is a built-in role"}
	}
	if err := g.Validate(); err != nil {
		return &DelegationRequestError{Field: "grant", Reason: "invalid", Err: err}
	}
	for _, p := range g.Scope {
		if !permission.IsRegistered(permission.Permission(p)) {
			return &DelegationRequestError{Field: "scope", Reason: p + " is not a registered permission"}
		}
	}
	return nil
}
