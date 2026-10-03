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

	// ServerCertPEM is this node's own TLS server (leaf) certificate.
	// Required when Initialize is ever called with a nil CeremonyCA (a
	// BYO-TLS node: the operator's own certificate and key are the server
	// identity, not a CA this process holds the key for). Initialize
	// computes the SPKI pin it returns from THIS certificate's public key
	// -- not a CA's -- and returns it unchanged as InitResult.TLSCertPEM,
	// since there is no CA to self-issue a different one from. Ignored
	// when a CeremonyCA is configured: that case self-issues its own TLS
	// certificate, exactly as before BYO-TLS support existed.
	ServerCertPEM []byte

	// ClientCACertPEM is this node's configured client-authentication
	// trust anchor PEM (the CA(s) that verify a client certificate this
	// node accepts). It is the default admin-certificate pool
	// (InitializeOptions.AdminCertPoolPEM's fallback, and
	// InitResult.CACertPEM) when Initialize is called with a nil
	// CeremonyCA: the bring-your-own-certificate admin-credential path on
	// a BYO-TLS node validates against it. Ignored when a CeremonyCA is
	// configured: that case falls back to the CA's own bundle instead.
	ClientCACertPEM []byte
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

	// AdminCSRPEM, when non-empty, selects the CSR-sign path for the
	// bootstrap admin's certificate: the ceremony CA signs it as
	// CN=<admin>, OU=[system, administrator], using only the CSR's key.
	// Mutually exclusive with AdminCertPEM.
	AdminCSRPEM []byte

	// AdminCertPEM, when non-empty, selects the bring-your-own-certificate
	// path: a certificate the admin already holds (typically on a PIV card
	// or security key) is validated against AdminCertPoolPEM -- or, when that
	// is empty, against this ceremony's own CA bundle -- and adopted
	// unchanged. Nothing is signed and no key is generated. Mutually
	// exclusive with AdminCSRPEM.
	AdminCertPEM []byte

	// AdminCertPoolPEM is the PEM-encoded trust anchor bundle a presented
	// AdminCertPEM must chain to. Optional: when empty, AdminCertPEM is
	// validated against this ceremony's own CA bundle instead, per
	// docs/firstboot/user.md.
	AdminCertPoolPEM []byte

	// AdminGenerate selects the generate path explicitly: a key pair and CSR
	// are generated in-process and signed by the ceremony CA, returned to
	// the caller exactly once. It is mutually exclusive with AdminCSRPEM and
	// AdminCertPEM (go-qrdb#548 review). NOT suitable for production; see
	// docs/firstboot/user.md. When AdminCSRPEM, AdminCertPEM and
	// AdminGenerate are ALL unset, Initialize refuses with
	// *NoAdminCredentialSelectedError rather than defaulting to generate:
	// there is no credential-less default admin-credential path.
	AdminGenerate bool

	// Secret is the passphrase (or other strategy-specific credential) the
	// system barrier is initialized with, BORROWED for the duration of this
	// call exactly as seal.StorageBarrier.Initialize's own creds parameter
	// is: Initialize copies it into protected memory and destroys its own
	// copy before returning, leaving this slice for the caller to wipe.
	// Required when the barrier's configured strategy needs one (e.g.
	// "passphrase"); must be empty for "none". qrdb init is the only way a
	// fresh node's system barrier is ever initialized (go-qrdb#548): `barrier
	// init` refuses the system barrier outright and names this field's CLI
	// flag instead.
	Secret []byte
}

// AdminCredentialSource records which of the three supported paths produced
// the bootstrap admin's certificate during Initialize.
type AdminCredentialSource string

const (
	// AdminCredentialNone means Initialize did not produce an admin
	// credential (should not occur on a successful Initialize).
	AdminCredentialNone AdminCredentialSource = ""

	// AdminCredentialCSR means the ceremony CA signed an operator-supplied
	// CSR (AdminCSRPEM). The admin's own key never left their machine.
	AdminCredentialCSR AdminCredentialSource = "csr"

	// AdminCredentialBYOC means a certificate the admin already held was
	// validated and adopted unchanged (AdminCertPEM). Nothing was issued.
	AdminCredentialBYOC AdminCredentialSource = "byoc"

	// AdminCredentialGenerated means the ceremony generated the admin's key
	// pair and signed it itself, selected by setting AdminGenerate. It is
	// NOT suitable for production: the private key is returned to the
	// caller over the wire exactly once, and go-qrdb never persists or
	// retains it. See docs/firstboot/user.md.
	AdminCredentialGenerated AdminCredentialSource = "generated"
)

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

	// AdminCertPEM is the bootstrap admin's issued or adopted certificate
	// (go-qrdb#548). Always present on a successful Initialize: signed from
	// AdminCSRPEM, adopted from AdminCertPEM, or generated when neither was
	// supplied. The SAME bytes are persisted as the pending-admin-credential
	// record first boot reads to bind it; this copy is returned so a caller
	// that wants it immediately (the CLI, an SDK embedder) does not have to
	// wait for first boot.
	AdminCertPEM []byte

	// AdminKeyPEM is the generated private key, present ONLY when
	// AdminCredentialSource is AdminCredentialGenerated. It is returned
	// exactly once -- Initialize cannot be called again on the same ceremony
	// -- and go-qrdb never writes it to storage. The caller must capture it
	// immediately; there is no way to retrieve it again.
	AdminKeyPEM []byte

	// AdminCredentialSource records which path produced AdminCertPEM.
	AdminCredentialSource AdminCredentialSource

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
//
// sign-csr issues HOLDER certificates only: a Security Officer and a
// Custodian hold no login account, so naming either role here cannot mint a
// login. admin/operator/user/auditor were REMOVED (go-qrdb#548): sign-csr is
// reachable with only the SO PIN, before unseal, and let an SO name ANY
// username with a login role's profile -- under auth.mtls_binding_policy:
// enrolled, that unenrolled certificate then logged in as whichever account
// the SO named, including the system admin. A login certificate for a
// user-facing role is issued by the tenant Admin through the certs CSR-sign
// or BYOC path (pkg/certificate-authority) instead, which requires the caller
// to already be authenticated and authorized, not merely to know a PIN.
var roleProfileMap = map[string]*roleProfile{
	"so": {
		profile:      holderprofile.SecurityOfficer,
		validityDays: 365,
		keyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	},
	"custodian": {
		profile:      holderprofile.Custodian,
		validityDays: 365,
		keyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		extKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	},
}
