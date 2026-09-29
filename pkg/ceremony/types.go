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
	"crypto/x509"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// CeremonyState represents the current phase of the initialization ceremony.
type CeremonyState string

const (
	// StateAwaitingInit is the initial state before the ceremony begins.
	StateAwaitingInit CeremonyState = "awaiting_init"

	// StateEnrolling is the state after an M-of-N initialization, while the
	// share holders claim their share envelopes.
	StateEnrolling CeremonyState = "enrolling"

	// StateOperational is the final state after the ceremony is complete
	// and the system is ready for normal operations.
	StateOperational CeremonyState = "operational"
)

// OfficerConfig names one share holder enrolled by the initialization
// ceremony.
type OfficerConfig struct {
	// Username is the unique identifier for the holder.
	Username string

	// Role is the holder's policy role; empty means so.
	Role seal.HolderRole

	// CSRPEM is the PEM-encoded certificate signing request from the holder.
	// It supplies the holder's public key; the CA assigns the identity.
	CSRPEM []byte
}

// CeremonyConfig holds the configuration for an initialization ceremony. It
// carries no PIN: PINs are supplied per request and stored only as hashes by
// the injected PINBackend.
type CeremonyConfig struct {
	// DataDir is the directory for ceremony-related data storage.
	DataDir string

	// ShareTTL bounds how long a distributed share envelope is held for its
	// holder to claim before it is wiped. Zero selects DefaultShareTTL. It
	// applies to the system and recovery scopes; tenant scopes use
	// DefaultShareTTL.
	ShareTTL time.Duration
}

// InitializeOptions carries the per-request parameters of an initialization
// ceremony.
type InitializeOptions struct {
	// SOPin is the Security Officer PIN. Required. It is established (or, when
	// barrier initialization already established it, verified) through the
	// PINBackend and never retained by the ceremony.
	SOPin string

	// UserPin is the user-level PIN. Required; handled like SOPin.
	UserPin string

	// CredentialSealStrategy identifies how credentials are sealed at rest.
	CredentialSealStrategy string

	// Threshold is M. With Threshold >= 2 the ceremony enrols Officers as
	// share holders and initializes the barrier's Shamir custody, splitting
	// the barrier unseal key M-of-len(Officers) to them.
	Threshold int

	// Policy is the factor policy of the initial share set; empty selects
	// seal.FactorPolicyDesignated.
	Policy seal.FactorPolicy

	// Officers are the share holders to enrol, Security Officers and key
	// custodians alike.
	Officers []OfficerConfig

	// Enroller is the authenticated principal running the ceremony. It may
	// not appear among Officers.
	Enroller string

	// Hostname is the primary hostname for TLS certificate generation.
	Hostname string

	// SANs is the list of Subject Alternative Names for TLS certificates.
	SANs []string
}

// ChallengeResponse holds the nonce issued for a share-claim handshake.
type ChallengeResponse struct {
	// Nonce is the random challenge bytes the holder must sign.
	Nonce []byte

	// Username is the holder being challenged.
	Username string

	// ExpiresAt is the deadline by which the holder must respond.
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

	// ShareCount is the number of share envelopes distributed (0 for single
	// admin).
	ShareCount int

	// Holders is each enrolled holder's issued certificate, in Officers
	// order. The certificates are public; each must reach its holder, who
	// then claims their share envelope with it.
	Holders []*RegisterOfficerResult
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
