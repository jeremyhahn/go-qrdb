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

import "strconv"

// AuthError is a typed error for authentication/authorization failures.
type AuthError struct {
	Code    string
	Message string
}

// Error implements the error interface.
func (e *AuthError) Error() string {
	return "qrdb/auth: " + e.Code + ": " + e.Message
}

var (
	// ErrUnauthenticated is returned when no valid credentials are found.
	ErrUnauthenticated = &AuthError{Code: "unauthenticated", Message: "no valid credentials provided"}

	// ErrUnauthorized is returned when the identity lacks permission.
	ErrUnauthorized = &AuthError{Code: "unauthorized", Message: "insufficient permissions"}

	// ErrNoPeerCertificate is returned when mTLS is expected but no client certificate is present.
	ErrNoPeerCertificate = &AuthError{Code: "no_peer_cert", Message: "no client certificate in TLS connection"}

	// ErrInvalidCertificate is returned when the client certificate is malformed or unverified.
	ErrInvalidCertificate = &AuthError{Code: "invalid_cert", Message: "client certificate is invalid or unverified"}

	// ErrCertificateRevoked is returned when the client certificate verified
	// against the CA but its serial has been revoked. The chain is sound and
	// the certificate is inside its validity window; the issuer has withdrawn
	// it.
	//
	// This is the error that makes revocation an access-control decision
	// rather than a display flag. It is produced only by
	// MTLSAuthenticator.Authenticate, on every request, and never from a
	// handshake callback — see RevocationChecker for why.
	ErrCertificateRevoked = &AuthError{Code: "cert_revoked", Message: "client certificate has been revoked"}
)

// CertificateTenantError reports that a client certificate names no usable
// tenant and must be refused: no Subject OU (ErrNoTenantOU), no tenant SPIFFE
// ID SAN, an OU and SPIFFE ID that disagree (*TenantNameMismatchError), a
// SPIFFE SAN outside the pinned trust domain, a malformed SPIFFE SAN, or SANs
// naming more than one tenant. It matches ErrInvalidCertificate under
// errors.Is and errors.As (so every transport classifies it exactly as before)
// and carries the cause for callers that need to tell them apart.
type CertificateTenantError struct {
	// Err is the cause.
	Err error
}

// Error returns the invalid-certificate message with the cause appended.
func (e *CertificateTenantError) Error() string {
	return ErrInvalidCertificate.Error() + ": " + e.Err.Error()
}

// Unwrap exposes both ErrInvalidCertificate and the cause.
func (e *CertificateTenantError) Unwrap() []error {
	return []error{ErrInvalidCertificate, e.Err}
}

// ErrNoTenantOU is the cause carried by a *CertificateTenantError when a
// client certificate has no Subject OU: the OU is one of the two fields that
// must both name the certificate's tenant (TenantFromClientCert).
var ErrNoTenantOU = &AuthError{Code: "no_tenant_ou", Message: "client certificate has no subject OU naming its tenant"}

// ErrTenantIsClusterTrustDomain is the refusal when a tenant would be the
// cluster's own trust domain: the cluster trust domain names no tenant, so a
// certificate whose OU names it is refused (TenantFromClientCert) and none is
// issued for it (TenantPrincipalURIs).
var ErrTenantIsClusterTrustDomain = &AuthError{Code: "tenant_is_cluster_trust_domain", Message: "tenant names the cluster trust domain"}

// TenantNameMismatchError is the cause carried by a *CertificateTenantError
// when a client certificate's Subject OU and its SPIFFE ID SAN name different
// tenants. Neither field is preferred: the certificate names no tenant.
type TenantNameMismatchError struct {
	// OU is the tenant the first Subject OU names.
	OU string
	// SPIFFETenant is the tenant the SPIFFE ID SAN names.
	SPIFFETenant string
}

// Error names both values.
func (e *TenantNameMismatchError) Error() string {
	return "subject OU " + strconv.Quote(e.OU) + " and SPIFFE ID tenant " + strconv.Quote(e.SPIFFETenant) + " disagree"
}

// UnknownTenantError is the cause carried by a *CertificateTenantError when a
// client certificate's Subject OU and SPIFFE ID trust domain agree on a tenant
// that does not exist. A trust domain names a tenant only once the tenant is
// created; until then, and after it is deleted, the certificate names no one.
type UnknownTenantError struct {
	// TenantID is the tenant both fields named.
	TenantID string
}

// Error names the tenant.
func (e *UnknownTenantError) Error() string {
	return "tenant " + strconv.Quote(e.TenantID) + " does not exist"
}

// TenantLookupError reports that the tenant a client certificate names could
// not be looked up. Like RevocationCheckError it is a refusal, not a denial:
// the certificate may be good and this node could not read the tenant record,
// so transports answer 503 / UNAVAILABLE and the client retries.
type TenantLookupError struct {
	// TenantID is the tenant the certificate named.
	TenantID string
	// Err is the underlying lookup failure.
	Err error
}

// Error names the tenant and the lookup failure.
func (e *TenantLookupError) Error() string {
	msg := "qrdb/auth: tenant " + strconv.Quote(e.TenantID) + " lookup failed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying lookup failure.
func (e *TenantLookupError) Unwrap() error { return e.Err }

// TrustStateUnavailableError reports that a client certificate's chain did
// not verify while this node could not read its client-auth trust state in
// full: a tenant's trust anchors, Key B or the cluster CA did not answer.
// Like TenantLookupError it is a refusal, not a denial: the certificate may
// be one the node admits once the read succeeds, so transports answer 503 /
// UNAVAILABLE and the client retries.
type TrustStateUnavailableError struct {
	// Err is the verifier's refusal, carrying the read that failed.
	Err error
}

// Error describes the refusal and the read that failed.
func (e *TrustStateUnavailableError) Error() string {
	msg := "qrdb/auth: client trust state unavailable"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the verifier's refusal.
func (e *TrustStateUnavailableError) Unwrap() error { return e.Err }

// IssuerTenantMismatchError reports that a verified client certificate's
// issuing CA is neither the cluster CA (Key A) nor the Key B CA registered
// for the tenant the certificate's Subject names. It matches
// ErrInvalidCertificate under errors.Is and errors.As, like
// CertificateTenantError, so every transport classifies it exactly as a
// malformed or untrusted certificate.
//
// This is the enforcement, at the authentication layer, of the rule that
// lets every tenant's Key B CA be trusted on one shared client-facing
// listener at all: a login CA scoped to tenant A signing a leaf whose
// Subject names tenant B must be refused, not merely accepted because SOME
// trusted CA's signature verifies. See
// docs/certificate-authority/architecture.md, "Issuer-tenant binding".
type IssuerTenantMismatchError struct {
	// TenantID is the tenant the certificate's Subject named.
	TenantID string

	// Err is the underlying cause when the mismatch was detected via a
	// failed lookup (e.g. the barrier was sealed) rather than a clean "no".
	Err error
}

// Error returns the invalid-certificate message naming the claimed tenant.
func (e *IssuerTenantMismatchError) Error() string {
	msg := ErrInvalidCertificate.Error() + ": issuer is not tenant " + quoteTenant(e.TenantID) + "'s registered CA"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap exposes both ErrInvalidCertificate and the cause, when one exists.
func (e *IssuerTenantMismatchError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrInvalidCertificate}
	}
	return []error{ErrInvalidCertificate, e.Err}
}

// quoteTenant renders a tenant ID for an error message, naming the system
// tenant explicitly when the ID is empty rather than printing "" unhelpfully.
func quoteTenant(tenantID string) string {
	if tenantID == "" {
		return strconv.Quote("(system)")
	}
	return strconv.Quote(tenantID)
}

// RevocationCheckError reports that a certificate's revocation status could
// not be determined.
//
// It is a REFUSAL, not a denial: the peer certificate may be perfectly good and
// the node simply could not read replicated revocation state. Transports map it
// to 503 / UNAVAILABLE so a client retries, rather than to 401, which would
// tell a legitimate caller its certificate is bad.
//
// Failing closed here is deliberate. The alternative — admitting the request
// when the check cannot run — is a fail-open that reinstates exactly the
// property this whole mechanism exists to remove, and it does so precisely
// during the degraded conditions in which an attacker most wants it. The one
// stated exception is the sealed barrier, where there is no revocation state
// to read at all; see MTLSAuthenticator.Authenticate.
// It carries no serial of its own. The certificate serial has exactly one
// canonical spelling and pkg/cert owns it (cert.FormatSerial); a second
// rendering here would be a second definition, and the wrapped error already
// names the key it could not read.
type RevocationCheckError struct {
	// Err is the underlying lookup failure.
	Err error
}

// Error returns a human-readable description of the failed revocation check.
func (e *RevocationCheckError) Error() string {
	msg := "qrdb/auth: certificate revocation check failed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying lookup failure.
func (e *RevocationCheckError) Unwrap() error { return e.Err }

// OperationError wraps an auth store or key-derivation operation failure
// together with its underlying cause. It preserves errors.Is/errors.As
// against the cause, replacing the inline "auth: <op>: %w" error-wrapping idiom.
type OperationError struct {
	// Op names the operation that failed (e.g. "store get", "store put",
	// "generate salt").
	Op string
	// Cause is the underlying error.
	Cause error
}

// Error returns "auth: <op>: <cause>".
func (e *OperationError) Error() string {
	return "auth: " + e.Op + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *OperationError) Unwrap() error { return e.Cause }
