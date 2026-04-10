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
	SOCertPEM  string `json:"so_cert_pem,omitempty"`
	SOKeyPEM   string `json:"so_key_pem,omitempty"`
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

// ClaimShareRequest requests the Shamir share for an officer who has
// already claimed their certificate.
type ClaimShareRequest struct {
	Username string `json:"username"`
}

// ClaimShareResponse returns the officer's Shamir share.
type ClaimShareResponse struct {
	ShareJSON string `json:"share_json"`
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
