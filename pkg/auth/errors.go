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
// tenant and must be refused: a SPIFFE SAN outside the pinned trust domain, a
// malformed SPIFFE SAN, or SANs naming more than one tenant. It matches
// ErrInvalidCertificate under errors.Is and errors.As (so every transport
// classifies it exactly as before) and carries the spiffe cause for callers
// that need to tell the three apart.
type CertificateTenantError struct {
	// Err is the spiffe cause.
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
