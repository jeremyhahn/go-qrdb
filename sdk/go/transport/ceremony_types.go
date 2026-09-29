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

// CeremonyOfficerRequest describes one initial share holder. The node's CA
// assigns the certificate identity; the CSR supplies only the public key,
// which is also the key the holder's share envelope is sealed to and the
// key its mtls factor proves possession of. Role is "so" (the default) or
// "custodian".
type CeremonyOfficerRequest struct {
	Username string `json:"username"`
	Role     string `json:"role,omitempty"`
	CSRPEM   string `json:"csr_pem"`
}

// CeremonyInitializeRequest holds the parameters for initializing the
// security ceremony. A Threshold of 2 or more initializes Shamir custody
// over Officers under Policy ("designated" when empty).
type CeremonyInitializeRequest struct {
	SOPin                  string                   `json:"so_pin"`
	UserPin                string                   `json:"user_pin"`
	CredentialSealStrategy string                   `json:"credential_seal_strategy,omitempty"`
	Threshold              int                      `json:"threshold"`
	Policy                 string                   `json:"policy,omitempty"`
	Officers               []CeremonyOfficerRequest `json:"officers,omitempty"`
	Hostname               string                   `json:"hostname,omitempty"`
	SANs                   []string                 `json:"sans,omitempty"`
}

// CeremonyHolderCert is the CA-issued certificate of one initial holder.
type CeremonyHolderCert struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	CertPEM  string `json:"cert_pem"`
}

// CeremonyInitializeResponse returns the result of a successful ceremony
// initialization. Holders carries each initial holder's certificate; the
// share envelopes are not part of it, each holder claims its own.
type CeremonyInitializeResponse struct {
	SPKIPin    string               `json:"spki_pin"`
	CACertPEM  string               `json:"ca_cert_pem"`
	TLSCertPEM string               `json:"tls_cert_pem"`
	State      string               `json:"state"`
	ShareCount int                  `json:"share_count"`
	Holders    []CeremonyHolderCert `json:"holders,omitempty"`
}

// CeremonyStatusResponse describes the current state of the ceremony.
type CeremonyStatusResponse struct {
	State   string `json:"state"`
	SPKIPin string `json:"spki_pin,omitempty"`
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
