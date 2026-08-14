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

package transport

// CeremonyOfficerRequest describes a single officer for M-of-N
// initialization.
type CeremonyOfficerRequest struct {
	Username string `json:"username"`
	CSRPEM   string `json:"csr_pem"`
}

// CeremonyInitializeRequest holds the parameters for initializing the
// security ceremony.
type CeremonyInitializeRequest struct {
	SOPin                  string                   `json:"so_pin"`
	UserPin                string                   `json:"user_pin"`
	CredentialSealStrategy string                   `json:"credential_seal_strategy,omitempty"`
	Threshold              int                      `json:"threshold"`
	Officers               []CeremonyOfficerRequest `json:"officers,omitempty"`
	Hostname               string                   `json:"hostname,omitempty"`
	SANs                   []string                 `json:"sans,omitempty"`
}

// CeremonyInitializeResponse returns the result of a successful ceremony
// initialization.
type CeremonyInitializeResponse struct {
	SPKIPin    string `json:"spki_pin"`
	CACertPEM  string `json:"ca_cert_pem"`
	TLSCertPEM string `json:"tls_cert_pem"`
	State      string `json:"state"`
	ShareCount int    `json:"share_count"`
}

// CeremonyStatusResponse describes the current state of the ceremony.
type CeremonyStatusResponse struct {
	State   string `json:"state"`
	SPKIPin string `json:"spki_pin,omitempty"`
}

// ClaimCertBeginRequest initiates a certificate claim challenge for
// an officer.
type ClaimCertBeginRequest struct {
	Username string `json:"username"`
}

// ClaimCertBeginResponse returns the challenge nonce that the officer
// must sign with their CSR private key.
type ClaimCertBeginResponse struct {
	NonceHex  string `json:"nonce_hex"`
	ExpiresAt string `json:"expires_at"`
}

// ClaimCertCompleteRequest completes a certificate claim by submitting
// a signed nonce.
type ClaimCertCompleteRequest struct {
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature string `json:"signature"`
}

// ClaimCertCompleteResponse returns the issued certificate after a
// successful challenge-response.
type ClaimCertCompleteResponse struct {
	CertPEM   string `json:"cert_pem"`
	CACertPEM string `json:"ca_cert_pem"`
}

// BeginClaimShareRequest starts the Shamir share claim handshake for an
// officer who has already claimed their certificate. No thumbprint travels on
// the wire: the server derives the caller's certificate thumbprint from the
// mTLS peer chain, so the client cannot assert an identity.
type BeginClaimShareRequest struct {
	Username string `json:"username"`
}

// BeginClaimShareResponse carries the challenge nonce the officer must sign
// with the private key matching their enrolled certificate.
type BeginClaimShareResponse struct {
	NonceHex  string `json:"nonce_hex"`
	ExpiresAt string `json:"expires_at"`
}

// CompleteClaimShareRequest carries the signed challenge nonce completing the
// Shamir share claim handshake.
type CompleteClaimShareRequest struct {
	Username  string `json:"username"`
	NonceHex  string `json:"nonce_hex"`
	Signature []byte `json:"signature"`
}

// CompleteClaimShareResponse returns the officer's Shamir share. The share is
// released exactly once, only to the officer whose certificate thumbprint
// matches the enrolment record.
type CompleteClaimShareResponse struct {
	ShareIndex int    `json:"share_index"`
	ShareJSON  string `json:"share_json"`
}

// SignCSRInitRequest submits a CSR for signing during or after the
// ceremony.
type SignCSRInitRequest struct {
	Username string `json:"username"`
	SOPin    string `json:"so_pin"`
	CSRPEM   string `json:"csr_pem"`
	Role     string `json:"role"`
}

// SignCSRInitResponse returns the signed certificate.
type SignCSRInitResponse struct {
	CertPEM string `json:"cert_pem"`
}

// EnrollExternalOfficerRequest carries the inputs for
// CeremonyEnrollExternalOfficer. Exactly one of CertPEM and PubKeyPEM
// must be supplied. AuthFactor must be one of "mtls", "webauthn", or "piv".
type EnrollExternalOfficerRequest struct {
	Username   string `json:"username"`
	CertPEM    string `json:"cert_pem,omitempty"`
	PubKeyPEM  string `json:"pubkey_pem,omitempty"`
	ShareIndex int    `json:"share_index"`
	AuthFactor string `json:"auth_factor"`
	// ShareBytes is the optional plaintext Shamir share to seal to the
	// officer's public key. Empty when only the public-key trust record
	// is being persisted.
	ShareBytes []byte `json:"share_bytes,omitempty"`
}

// EnrollExternalOfficerResponse returns the sealed share envelope (when
// ShareBytes was supplied).
type EnrollExternalOfficerResponse struct {
	SealedShare string `json:"sealed_share,omitempty"`
}
