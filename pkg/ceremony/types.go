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

package ceremony

import (
	"crypto"
	"crypto/x509"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
)

// CeremonyState represents the current phase of the initialization ceremony.
type CeremonyState string

const (
	// StateAwaitingInit is the initial state before the ceremony begins.
	StateAwaitingInit CeremonyState = "awaiting_init"

	// StateEnrolling is the state during M-of-N officer enrollment,
	// where officers claim their certificates and key shares.
	StateEnrolling CeremonyState = "enrolling"

	// StateOperational is the final state after the ceremony is complete
	// and the system is ready for normal operations.
	StateOperational CeremonyState = "operational"
)

// AuthenticatorType identifies the hardware or software that backs an
// officer's signing credential. The empty value is treated as a
// software-backed credential (see Normalized).
type AuthenticatorType string

const (
	// AuthenticatorSoftware indicates a software-backed credential whose
	// private key lives in process memory or on disk.
	AuthenticatorSoftware AuthenticatorType = "software"

	// AuthenticatorPIV indicates a PIV smart card credential where the
	// AuthenticatorRef is the slot identifier (e.g. "9a").
	AuthenticatorPIV AuthenticatorType = "piv"

	// AuthenticatorFIDO2 indicates a FIDO2 / CTAP2 credential where the
	// AuthenticatorRef is the base64url credential ID.
	AuthenticatorFIDO2 AuthenticatorType = "fido2"
)

// Normalized returns the canonical AuthenticatorType, mapping the empty
// value to AuthenticatorSoftware and otherwise returning the type
// unchanged.
func (a AuthenticatorType) Normalized() AuthenticatorType {
	if a == "" {
		return AuthenticatorSoftware
	}
	return a
}

// Validate reports whether the AuthenticatorType is one of the known
// values. The empty value is valid and defaults to software. Unknown
// values return ErrInvalidAuthenticatorType.
func (a AuthenticatorType) Validate() error {
	switch a {
	case "", AuthenticatorSoftware, AuthenticatorPIV, AuthenticatorFIDO2:
		return nil
	default:
		return ErrInvalidAuthenticatorType
	}
}

// OfficerConfig holds the configuration for a single ceremony officer.
type OfficerConfig struct {
	// Username is the unique identifier for the officer.
	Username string

	// CSRPEM is the PEM-encoded certificate signing request from the officer.
	CSRPEM []byte

	// AuthenticatorType identifies which hardware or software backs the
	// officer's signing credential. An empty value defaults to
	// AuthenticatorSoftware.
	AuthenticatorType AuthenticatorType

	// AuthenticatorRef is an opaque reference to the credential within
	// the backing authenticator. For PIV this is the slot identifier
	// (e.g. "9a"); for FIDO2 it is the credential ID (base64url); for
	// software credentials it is unused.
	AuthenticatorRef string
}

// CeremonyConfig holds the configuration for an initialization ceremony.
type CeremonyConfig struct {
	// SOPin is the Security Officer PIN used for administrative operations.
	SOPin string

	// UserPin is the user-level PIN that protects day-to-day operations.
	UserPin string

	// CredentialSealStrategy identifies how credentials are sealed at rest.
	CredentialSealStrategy string

	// Threshold is the minimum number of officers (M) required to
	// reconstruct the secret in M-of-N Shamir splitting.
	Threshold int

	// Officers is the list of officer configurations for M-of-N enrollment.
	Officers []OfficerConfig

	// Hostname is the primary hostname for TLS certificate generation.
	Hostname string

	// SANs is the list of Subject Alternative Names for TLS certificates.
	SANs []string

	// DataDir is the directory for ceremony-related data storage.
	DataDir string
}

// InitializeOptions carries per-request parameters for an initialization
// ceremony. It exists so callers can supply the Security Officer and user
// PINs — and the remaining ceremony configuration — on the Initialize call
// itself rather than mutating the shared CeremonyConfig — mutating shared
// config is not concurrency-safe.
//
// Per-request values take precedence over the corresponding CeremonyConfig
// fields when non-empty (non-zero for Threshold, non-empty for the slice
// fields); empty/zero fields fall back to the configured value.
type InitializeOptions struct {
	// SOPin is the Security Officer PIN supplied for this ceremony. When
	// empty the configured CeremonyConfig.SOPin is used.
	SOPin string

	// UserPin is the user-level PIN supplied for this ceremony. When empty
	// the configured CeremonyConfig.UserPin is used.
	UserPin string

	// CredentialSealStrategy identifies how credentials are sealed at rest.
	// When empty the configured CeremonyConfig.CredentialSealStrategy is used.
	CredentialSealStrategy string

	// Threshold is the minimum number of officers (M) required to
	// reconstruct the secret in M-of-N Shamir splitting. When zero the
	// configured CeremonyConfig.Threshold is used.
	Threshold int

	// Officers is the list of officer configurations for M-of-N enrollment.
	// When empty the configured CeremonyConfig.Officers is used.
	Officers []OfficerConfig

	// Hostname is the primary hostname for TLS certificate generation.
	// When empty the configured CeremonyConfig.Hostname is used.
	Hostname string

	// SANs is the list of Subject Alternative Names for TLS certificates.
	// When empty the configured CeremonyConfig.SANs is used.
	SANs []string
}

// PendingCert holds a certificate that has been issued but not yet
// claimed by its officer.
type PendingCert struct {
	// Username is the officer who owns this certificate.
	Username string

	// CertPEM is the PEM-encoded certificate.
	CertPEM []byte

	// CSRPubKey is the public key extracted from the officer's CSR,
	// used for nonce signature verification during claim.
	CSRPubKey crypto.PublicKey

	// Claimed indicates whether the officer has claimed this certificate.
	Claimed bool

	// AuthenticatorType identifies the authenticator that the officer
	// will use to sign the claim challenge nonce. Preserved from the
	// OfficerConfig used at ceremony initialization.
	AuthenticatorType AuthenticatorType

	// AuthenticatorRef is the opaque credential reference within the
	// authenticator (PIV slot, FIDO2 credential ID, or unused for
	// software). Preserved from the OfficerConfig used at ceremony
	// initialization.
	AuthenticatorRef string
}

// ChallengeResponse holds the challenge nonce issued during the
// certificate claim handshake.
type ChallengeResponse struct {
	// Nonce is the random challenge bytes the officer must sign.
	Nonce []byte

	// Username is the officer being challenged.
	Username string

	// ExpiresAt is the deadline by which the officer must respond.
	ExpiresAt time.Time
}

// InitResult contains the output of a successful ceremony initialization.
type InitResult struct {
	// SPKIPin is the base64-encoded SHA-256 hash of the CA certificate's
	// SubjectPublicKeyInfo, used for certificate pinning.
	SPKIPin string

	// CACertPEM is the PEM-encoded CA certificate.
	CACertPEM []byte

	// TLSCertPEM is the PEM-encoded TLS certificate issued for the server.
	TLSCertPEM []byte

	// State is the ceremony state after initialization.
	State CeremonyState

	// ShareCount is the number of Shamir shares generated (0 for single admin).
	ShareCount int
}

// ClaimCertResult contains the output of a successful certificate claim.
type ClaimCertResult struct {
	// CertPEM is the PEM-encoded officer certificate.
	CertPEM []byte

	// CACertPEM is the PEM-encoded CA certificate for trust chain verification.
	CACertPEM []byte
}

// SignCSRInitResult contains the output of a successful CSR signing operation.
type SignCSRInitResult struct {
	// CertPEM is the PEM-encoded certificate issued from the CSR.
	CertPEM []byte
}

// roleProfile maps a role name to its certificate profile parameters.
type roleProfile struct {
	profile      string
	validityDays int
	keyUsage     x509.KeyUsage
	extKeyUsage  []x509.ExtKeyUsage
}

// roleProfileMap maps recognized role names to their certificate profiles.
var roleProfileMap = map[string]*roleProfile{
	"so": {
		profile:      holderprofile.SecurityOfficer,
		validityDays: 365,
		keyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	},
	"admin": {
		profile:      "administrator",
		validityDays: 365,
		keyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	},
	"operator": {
		profile:      "operator",
		validityDays: 180,
		keyUsage:     x509.KeyUsageDigitalSignature,
		extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	},
	"user": {
		profile:      "user",
		validityDays: 90,
		keyUsage:     x509.KeyUsageDigitalSignature,
		extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	},
	"auditor": {
		profile:      "auditor",
		validityDays: 180,
		keyUsage:     x509.KeyUsageDigitalSignature,
		extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	},
	"custodian": {
		profile:      holderprofile.Custodian,
		validityDays: 365,
		keyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	},
}
