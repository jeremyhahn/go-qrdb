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

package cert

import "strconv"

// CertNotFoundError is returned when a certificate serial does not exist
// in the store.
type CertNotFoundError struct {
	Serial string
}

// Error returns a human-readable description of the missing certificate.
func (e *CertNotFoundError) Error() string {
	return "qrdb/cert: certificate not found: " + e.Serial
}

// CertRevokedError is returned when an operation targets a revoked
// certificate.
type CertRevokedError struct {
	Serial string
}

// Error returns a human-readable description of the revoked certificate.
func (e *CertRevokedError) Error() string {
	return "qrdb/cert: certificate revoked: " + e.Serial
}

// CertGenerateError wraps an error that occurred during certificate
// generation.
type CertGenerateError struct {
	Err error
}

// Error returns a human-readable description of the generation failure.
func (e *CertGenerateError) Error() string {
	msg := "qrdb/cert: certificate generation failed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *CertGenerateError) Unwrap() error {
	return e.Err
}

// CertExportError wraps an error that occurred during certificate export.
type CertExportError struct {
	Err error
}

// Error returns a human-readable description of the export failure.
func (e *CertExportError) Error() string {
	msg := "qrdb/cert: certificate export failed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *CertExportError) Unwrap() error {
	return e.Err
}

// PKCS12FIPSUnavailableError is returned when a PKCS#12 export is requested
// while the Go Cryptographic Module is in strict FIPS 140-3 mode
// (GODEBUG=fips140=only).
//
// The archive's own protection is approved -- Modern2023 is PBKDF2-HMAC-SHA256
// with AES-256-CBC -- but PKCS#12 also carries a localKeyId attribute holding
// the SHA-1 fingerprint of the certificate, which is how every consumer of the
// format (OpenSSL, Java keytool, Windows CryptoAPI) matches a key to its cert.
// That is an interoperability identifier, not a protection mechanism, so it is
// not a compliance violation; but crypto/sha1 panics under strict enforcement,
// so the export cannot be produced there. See docs/seal/fips140-mode.md.
type PKCS12FIPSUnavailableError struct{}

// Error returns a human-readable description.
func (e *PKCS12FIPSUnavailableError) Error() string {
	return "qrdb/cert: PKCS#12 export is unavailable under GODEBUG=fips140=only: " +
		"the format's localKeyId attribute is a SHA-1 certificate fingerprint"
}

// CAKeyNotAvailableError is returned when the CA private key is not
// loaded or has been destroyed.
type CAKeyNotAvailableError struct{}

// Error returns a human-readable description indicating the CA key is
// unavailable.
func (e *CAKeyNotAvailableError) Error() string {
	return "qrdb/cert: CA private key not available"
}

// BarrierSealedError is returned when an operation requires the
// encryption barrier to be unsealed but it is currently sealed.
type BarrierSealedError struct{}

// Error returns a human-readable description indicating the barrier is
// sealed.
func (e *BarrierSealedError) Error() string {
	return "qrdb/cert: encryption barrier is sealed"
}

// StoreError wraps an error from a KVStore operation with the operation
// name and detail context.
type StoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description of the store failure.
func (e *StoreError) Error() string {
	msg := "qrdb/cert: store " + e.Op
	if e.Detail != "" {
		msg += " " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StoreError) Unwrap() error {
	return e.Err
}

// SPIFFEIdentityError is returned by IssueClientCert when the provider has
// a trust domain configured and the tenant-scoped SPIFFE SAN it would emit
// does not parse back to the same trust domain, tenant and username the
// certificate's Subject names (go-qrdb#519).
//
// No certificate is minted. One that were would authenticate as the SAN's
// tenant on a node with a trust-domain pin and as the OU's tenant on a node
// without one, which is worse than carrying no SAN at all.
type SPIFFEIdentityError struct {
	TrustDomain string
	TenantID    string
	Username    string
	Reason      string
}

// Error returns a human-readable description naming the refused identity.
func (e *SPIFFEIdentityError) Error() string {
	return "qrdb/cert: refusing to mint a client certificate for " + strconv.Quote(e.Username) +
		" in tenant " + strconv.Quote(e.TenantID) + " under SPIFFE trust domain " +
		strconv.Quote(e.TrustDomain) + ": " + e.Reason
}

// ValidationError is returned when a request field fails validation.
type ValidationError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description of the validation failure.
func (e *ValidationError) Error() string {
	return "qrdb/cert: invalid " + e.Field + ": " + e.Reason
}

// InvalidCSRError is returned when a certificate signing request cannot be
// issued for: it does not parse, its self-signature does not verify, or its
// key is not one a client certificate is issued for.
type InvalidCSRError struct {
	Reason string
	Err    error
}

// Error returns a human-readable description of the refusal.
func (e *InvalidCSRError) Error() string {
	msg := "qrdb/cert: invalid certificate signing request: " + e.Reason
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the parse or verification cause.
func (e *InvalidCSRError) Unwrap() error { return e.Err }
