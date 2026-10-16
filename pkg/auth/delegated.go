// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"slices"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Identity.Metadata keys a delegated identity carries, for the audit trail.
const (
	// DelegationSerialMetadataKey is the delegation certificate's serial, as
	// lowercase unpadded hex -- the form certificate revocation names it by.
	DelegationSerialMetadataKey = "delegation_serial"
)

// Delegation is the tenant-issued delegation a service principal
// authenticated with: a certificate the tenant's own login CA signed,
// naming Principal and the permissions (Scope) it may exercise inside the
// identity's tenant. See pkg/auth/delegation.
type Delegation struct {
	// Principal is the service principal the delegation names.
	Principal string

	// Scope is the delegation's permission scope. The permissions a request
	// holds are Scope intersected with the principal's own grants.
	Scope []string

	// Serial is the delegation certificate's serial, as lowercase unpadded
	// hex.
	Serial string

	// NotAfter is when the delegation certificate expires.
	NotAfter time.Time

	// Certificate is the delegation certificate's DER, so a node forwarding
	// the request carries the credential itself and the receiving node
	// verifies it again rather than trusting the forwarder's word for it.
	Certificate []byte
}

// DelegationOf returns the delegation id authenticated with, and whether it
// authenticated with one. Only MTLSAuthenticator, after verifying a
// delegation certificate in full, produces an identity for which it reports
// true. The returned value is a copy.
func DelegationOf(id *Identity) (Delegation, bool) {
	if id == nil || id.delegation == nil {
		return Delegation{}, false
	}
	d := *id.delegation
	d.Scope = slices.Clone(d.Scope)
	d.Certificate = slices.Clone(d.Certificate)
	return d, true
}

// TenantIssuerVerifier is the capability a delegation's issuer check needs
// beyond IssuerVerifier: that issuer is tenantID's OWN registered login CA
// (Key B) and nothing else -- not the cluster CA, not a static client CA,
// not a trust anchor. A tenant delegates through its own CA; no other CA
// can speak for it. *clienttrust.Pool implements it.
type TenantIssuerVerifier interface {
	VerifyTenantIssuer(ctx context.Context, tenantID string, issuer *x509.Certificate) error
}

// WithDelegatedPrincipal names the service principal this node accepts
// tenant-issued delegations for. A delegation naming any other principal is
// refused, and so is every delegation when no principal is named (the
// default). See docs/identity-rbac.
func WithDelegatedPrincipal(name string) MTLSOption {
	return func(a *MTLSAuthenticator) {
		a.delegatedPrincipal = name
	}
}

// DelegationRefusedError reports that a client certificate carrying a
// delegation was refused. It matches ErrInvalidCertificate under errors.Is,
// so every transport classifies it as an unusable credential (401), and
// carries the reason and cause for logs and tests.
type DelegationRefusedError struct {
	// Reason says which check refused the delegation.
	Reason string

	// Err is the underlying cause, when there is one.
	Err error
}

// Error returns the invalid-certificate message with the reason appended.
func (e *DelegationRefusedError) Error() string {
	msg := ErrInvalidCertificate.Error() + ": delegation refused: " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes both ErrInvalidCertificate and the cause, when one exists.
func (e *DelegationRefusedError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrInvalidCertificate}
	}
	return []error{ErrInvalidCertificate, e.Err}
}

// VerifyDelegation verifies a delegation certificate presented as DER --
// the form a forwarding node carries it in -- exactly as Authenticate
// verifies one presented on the connection, and returns the delegated
// identity. It needs an issuer verifier: without one there is no trust pool
// to build the chain against, and the certificate is refused.
func (a *MTLSAuthenticator) VerifyDelegation(ctx context.Context, der []byte) (*Identity, error) {
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, &DelegationRefusedError{Reason: "certificate does not parse", Err: err}
	}
	g, found, err := delegation.FromCertificate(leaf)
	if !found {
		return nil, &DelegationRefusedError{Reason: "certificate carries no delegation"}
	}
	if err != nil {
		return nil, &DelegationRefusedError{Reason: "malformed delegation", Err: err}
	}
	if a.issuerVerifier == nil {
		return nil, &DelegationRefusedError{Reason: "no trust pool to verify against"}
	}
	chain, err := a.issuerVerifier.VerifyPeerChain(ctx, leaf, nil)
	if err != nil {
		return nil, &DelegationRefusedError{Reason: "certificate does not verify", Err: err}
	}
	return a.authenticateDelegation(ctx, chain, g)
}

// authenticateDelegation admits a verified chain whose leaf carries the
// delegation g, or refuses it. It never falls back to reading the leaf as a
// user login: a certificate issued as a delegation is judged as one or not
// at all. Every check fails closed:
//
//   - the delegation names the principal this node accepts delegations for;
//   - the certificate's lifetime is within delegation.MaxLifetime, whatever
//     the issuing CA put in it;
//   - revocation is consulted and must answer. Unlike a user login, a
//     delegation is not admitted when revocation state is unreadable: a
//     sealed node has no tenant for it to act in anyway;
//   - the certificate's tenant -- its service SPIFFE SAN, or its Subject OU
//     when it has none -- is the grant's tenant, and the SAN names the
//     principal as a service;
//   - the tenant is not the system tenant: the platform cannot be delegated
//     to, and no platform CA can mint a delegation;
//   - the leaf's direct issuer is that tenant's own registered login CA.
func (a *MTLSAuthenticator) authenticateDelegation(ctx context.Context, chain []*x509.Certificate, g delegation.Grant) (*Identity, error) {
	leaf := chain[0]
	if a.delegatedPrincipal == "" || g.Principal != a.delegatedPrincipal {
		return nil, &DelegationRefusedError{Reason: "names principal " + g.Principal + ", which this node does not accept delegations for"}
	}
	if err := delegation.CheckLifetime(leaf); err != nil {
		return nil, &DelegationRefusedError{Reason: "lifetime", Err: err}
	}
	if a.revocation == nil {
		return nil, &DelegationRefusedError{Reason: "no revocation checker"}
	}
	revoked, err := a.revocation.IsCertRevoked(ctx, leaf)
	if err != nil {
		return nil, &RevocationCheckError{Err: err}
	}
	if revoked {
		return nil, ErrCertificateRevoked
	}
	source, err := delegationTenantSource(leaf, a.pinnedTrustDomain, g)
	if err != nil {
		return nil, err
	}
	if tenantid.IsSystem(g.Tenant) {
		return nil, &DelegationRefusedError{Reason: "the system tenant cannot delegate"}
	}
	tv, ok := a.issuerVerifier.(TenantIssuerVerifier)
	if !ok || len(chain) < 2 {
		return nil, &DelegationRefusedError{Reason: "issuer cannot be bound to the tenant"}
	}
	if err := tv.VerifyTenantIssuer(ctx, g.Tenant, chain[1]); err != nil {
		return nil, &DelegationRefusedError{Reason: "issuer is not tenant " + quoteTenant(g.Tenant) + "'s login CA", Err: err}
	}

	serial := ""
	if leaf.SerialNumber != nil {
		// pkg/cert.FormatSerial's form; pkg/cert imports this package, so
		// the one-line rule is restated rather than imported.
		serial = leaf.SerialNumber.Text(16)
	}
	return &Identity{
		Subject:  ServicePrincipalSubjectPrefix + g.Principal,
		TenantID: g.Tenant,
		Metadata: map[string]string{
			ServicePrincipalMetadataKey: g.Principal,
			DelegationSerialMetadataKey: serial,
			"tenant_source":             source,
		},
		delegation: &Delegation{
			Principal:   g.Principal,
			Scope:       slices.Clone(g.Scope),
			Serial:      serial,
			NotAfter:    leaf.NotAfter,
			Certificate: slices.Clone(leaf.Raw),
		},
	}, nil
}

// delegationTenantSource checks that leaf names g's tenant, and returns
// where it names it: "spiffe" for a tenant SPIFFE SAN, which must also name
// g's principal as a service, or "ou" for the Subject OU of a certificate
// with no SPIFFE SAN.
func delegationTenantSource(leaf *x509.Certificate, pinnedTrustDomain string, g delegation.Grant) (string, error) {
	id, err := spiffe.TenantFromCert(leaf, pinnedTrustDomain)
	switch {
	case err == nil:
		if id.TenantID != g.Tenant || id.Kind != spiffe.KindService || id.Name != g.Principal {
			return "", &DelegationRefusedError{Reason: "SPIFFE SAN does not name the delegated service in the delegated tenant"}
		}
		return "spiffe", nil
	case errors.Is(err, spiffe.ErrNoTenantSPIFFEID):
		if len(leaf.Subject.OrganizationalUnit) == 0 || leaf.Subject.OrganizationalUnit[0] != g.Tenant {
			return "", &DelegationRefusedError{Reason: "certificate does not name the delegated tenant"}
		}
		return "ou", nil
	default:
		return "", &CertificateTenantError{Err: err}
	}
}
