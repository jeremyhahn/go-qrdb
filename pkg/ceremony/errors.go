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
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	// ErrNilConfig is returned when a nil CeremonyConfig is provided.
	ErrNilConfig = errors.New("ceremony: nil config provided")

	// ErrNilBarrier is returned when a nil StorageBarrier is provided.
	ErrNilBarrier = errors.New("ceremony: nil barrier provided")

	// ErrNilLogger is returned when a nil logger is provided.
	ErrNilLogger = errors.New("ceremony: nil logger provided")

	// ErrNilStore is returned when a nil StorageBackend is provided.
	ErrNilStore = errors.New("ceremony: nil storage backend provided")

	// ErrAlreadyInitialized is returned when Initialize is called after
	// the ceremony has already completed.
	ErrAlreadyInitialized = errors.New("ceremony: ceremony already completed")

	// ErrInvalidSOPIN is returned when the SO PIN is empty or invalid.
	ErrInvalidSOPIN = errors.New("ceremony: SO PIN is empty or invalid")

	// ErrInvalidUserPIN is returned when the user PIN is empty or invalid.
	ErrInvalidUserPIN = errors.New("ceremony: user PIN is empty or invalid")

	// ErrInvalidThreshold is returned when the Shamir threshold is less
	// than 2 or greater than the number of officers.
	ErrInvalidThreshold = errors.New("ceremony: threshold < 2 or threshold > officers count")

	// ErrNoOfficers is returned when M-of-N is requested but no officers
	// are configured.
	ErrNoOfficers = errors.New("ceremony: M-of-N requires at least one officer")

	// ErrInvalidCSR is returned when CSR validation fails.
	ErrInvalidCSR = errors.New("ceremony: CSR validation failed")

	// ErrDuplicateUsername is returned when an officer username is
	// specified more than once.
	ErrDuplicateUsername = errors.New("ceremony: officer username already exists")

	// ErrShareAlreadyClaimed is returned when a share has already been
	// claimed by the officer.
	ErrShareAlreadyClaimed = errors.New("ceremony: share already claimed")

	// ErrShareNotFound is returned when a share does not exist for the
	// given username.
	ErrShareNotFound = errors.New("ceremony: share not found")

	// ErrShareExpired is returned when a share was distributed to a holder
	// but was wiped unclaimed at the end of its time-to-live. Shares are never
	// persisted, so the only remedy is a rekey, which distributes fresh ones.
	ErrShareExpired = errors.New("ceremony: share expired unclaimed; a rekey is required to redistribute")

	// ErrShareUnreadable is returned when a stored share's protected buffer
	// yields no bytes — the shape a concurrent destroy (a shutdown
	// enclave.WipeAll) leaves behind.
	// Claim fails closed on it rather than returning a zero-filled slice of
	// the right length and marking the real share consumed (go-qrdb#74).
	ErrShareUnreadable = errors.New("ceremony: share buffer yielded no bytes")

	// ErrChallengeVerificationFailed is returned when the nonce signature
	// verification fails.
	ErrChallengeVerificationFailed = errors.New("ceremony: nonce signature verification failed")

	// ErrUserNotFound is returned when a user is not found during a
	// claim operation.
	ErrUserNotFound = errors.New("ceremony: user not found during claim")

	// ErrThresholdNotSupported is returned when the HSM does not support
	// native threshold operations.
	ErrThresholdNotSupported = errors.New("ceremony: HSM does not support native threshold")

	// ErrVendorNotRegistered is returned when the requested vendor is not
	// registered in the threshold registry.
	ErrVendorNotRegistered = errors.New("ceremony: vendor not registered in threshold registry")

	// ErrVendorNotAvailable is returned when the vendor hardware is not
	// present or accessible.
	ErrVendorNotAvailable = errors.New("ceremony: vendor hardware not present")

	// ErrInitFailed is returned when the ceremony initialization fails.
	ErrInitFailed = errors.New("ceremony: initialization failed")

	// ErrNotImplemented is returned when an operation is not yet implemented.
	ErrNotImplemented = errors.New("ceremony: operation not implemented")

	// ErrEnrollmentClosed is returned when an unauthenticated caller (a
	// request the boot-time sealed-window listener admitted, see
	// sealedwindow.Anonymous) registers an officer after enrollment has
	// closed (EnrollmentOpen). Officers are registered after that through an
	// authenticated listener.
	ErrEnrollmentClosed = errors.New("ceremony: officer enrollment is closed to unauthenticated callers")

	// ErrCANotInitialized is returned when a CA operation is attempted
	// before the CA has been set via Initialize.
	ErrCANotInitialized = errors.New("ceremony: CA not initialized")

	// ErrMissingCSRPEM is returned when a CSR PEM is required but not provided.
	ErrMissingCSRPEM = errors.New("ceremony: CSR PEM is required")

	// ErrMissingUsername is returned when a username is required but not provided.
	ErrMissingUsername = errors.New("ceremony: username is required")

	// ErrMissingRole is returned when a role is required but not provided.
	ErrMissingRole = errors.New("ceremony: role is required")

	// ErrInvalidRole is returned when the requested role is not one
	// sign-csr issues. sign-csr mints HOLDER certificates only ("so",
	// "custodian") -- a login certificate for a user-facing role (admin,
	// operator, user, auditor) is issued by the tenant Admin through the
	// certs CSR-sign or BYOC path (pkg/certificate-authority), never here,
	// because sign-csr is reachable with only the SO PIN, before unseal, for
	// anyone to name any account.
	ErrInvalidRole = errors.New("ceremony: invalid role: sign-csr issues holder certificates only (so, custodian); " +
		"a login certificate for a user-facing role is issued by the tenant Admin through the certs CSR-sign or BYOC path")

	// ErrCSRSigningFailed is returned when CSR signing fails.
	ErrCSRSigningFailed = errors.New("ceremony: CSR signing failed")

	// ErrNotInitializedOrEnrolling is returned when sign-csr is called
	// outside the enrolling state. sign-csr is pre-operational only: it
	// issues holder certificates while an M-of-N ceremony is enrolling, and
	// is refused both before initialization and once the ceremony is
	// operational.
	ErrNotInitializedOrEnrolling = errors.New("ceremony: sign-csr requires the enrolling state; holder certificates are issued only before the ceremony is operational")

	// ErrNotInitialized is returned by an operation that needs an
	// initialized ceremony when none has been initialized yet ("qrdb init"
	// has not run).
	ErrNotInitialized = errors.New("ceremony: not initialized; run qrdb init first")

	// ErrChallengeExpired is returned when a challenge nonce has expired.
	ErrChallengeExpired = errors.New("ceremony: challenge has expired")

	// ErrChallengeNotFound is returned when no active challenge exists
	// for the given username.
	ErrChallengeNotFound = errors.New("ceremony: no active challenge for username")

	// ErrInvalidOfficerRecord is returned when a nil or otherwise invalid
	// officer record is supplied to the persistent registry.
	ErrInvalidOfficerRecord = errors.New("ceremony: invalid officer record")

	// ErrInvalidPubKeyPEM is returned when PubKeyPEM cannot be parsed.
	ErrInvalidPubKeyPEM = errors.New("ceremony: invalid public key PEM")

	// ErrCertThumbprintMismatch is returned when the caller's mTLS certificate
	// thumbprint does not match the officer's registered CertThumbprint.
	ErrCertThumbprintMismatch = errors.New("ceremony: certificate thumbprint mismatch")

	// ErrInsufficientOfficers is returned when fewer than threshold officers
	// are enrolled for a SealAndDistribute operation.
	ErrInsufficientOfficers = errors.New("ceremony: insufficient officers enrolled for threshold")

	// ErrOfficerNotFound is returned when the requested officer is not enrolled
	// in the registry.
	ErrOfficerNotFound = errors.New("ceremony: officer not found")

	// ErrNoPendingShare is returned when a claim is attempted but no sealed
	// share exists for the officer (share was never distributed or was already claimed).
	ErrNoPendingShare = errors.New("ceremony: no pending share for officer")

	// ErrNilBackend is returned when a nil StorageBackend is provided to a
	// constructor that requires one.
	ErrNilBackend = errors.New("ceremony: nil storage backend provided")

	// ErrNilCA is returned when a nil CeremonyCA is provided to a constructor
	// that requires one.
	ErrNilCA = errors.New("ceremony: nil CA provided")

	// ErrEmptyTenantID is returned when an empty tenant ID is provided to
	// ScopedCeremonyService or ScopeTenant.
	ErrEmptyTenantID = errors.New("ceremony: tenant ID must not be empty")

	// ErrEmptyDatabase is returned when an empty database name is provided to
	// a database barrier's holder authority.
	ErrEmptyDatabase = errors.New("ceremony: database name must not be empty")
)

// OfficerRegistryError wraps an error from persistent-registry operations.
type OfficerRegistryError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *OfficerRegistryError) Error() string {
	return fmt.Sprintf("ceremony: officer registry %s failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *OfficerRegistryError) Unwrap() error {
	return e.Err
}

// ShamirSplitError wraps an error from the Shamir split operation.
type ShamirSplitError struct {
	Err error
}

// Error returns a human-readable description of the Shamir split failure.
func (e *ShamirSplitError) Error() string {
	return fmt.Sprintf("ceremony: shamir split failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *ShamirSplitError) Unwrap() error {
	return e.Err
}

// CertSubjectError is returned when a ceremony certificate cannot be given
// the identity the ceremony registered: SignCSROptions omits the TenantID or
// Username, or a CeremonyCA returned a certificate whose Subject or SANs
// differ from what it was told to sign (Field names what differs). Nothing
// is issued.
type CertSubjectError struct {
	Field string
}

// Error names the field.
func (e *CertSubjectError) Error() string {
	return "ceremony: certificate subject " + e.Field + " is missing or not the registered identity"
}

// IssuedCertCapabilityError is returned when a CeremonyCA returned a
// certificate that can do more than SignCSROptions asked: it is a CA
// (Field "IsCA"), its key usage includes certificate signing or otherwise
// differs from SignCSROptions.KeyUsage ("KeyUsage"), or its extended key
// usages are not the set SignCSROptions.ExtKeyUsage names ("ExtKeyUsage").
// Nothing is issued.
type IssuedCertCapabilityError struct {
	Field string
}

// Error names the field.
func (e *IssuedCertCapabilityError) Error() string {
	return "ceremony: issued certificate " + e.Field + " exceeds what was requested"
}

// CSRParseError wraps an error from CSR parsing.
type CSRParseError struct {
	Err error
}

// Error returns a human-readable description of the CSR parse failure.
func (e *CSRParseError) Error() string {
	return fmt.Sprintf("ceremony: CSR parse failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CSRParseError) Unwrap() error {
	return e.Err
}

// CertEncodeError wraps an error from certificate encoding.
type CertEncodeError struct {
	Err error
}

// Error returns a human-readable description of the cert encode failure.
func (e *CertEncodeError) Error() string {
	return fmt.Sprintf("ceremony: cert encode failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CertEncodeError) Unwrap() error {
	return e.Err
}

// CACertParseError wraps an error from CA certificate parsing.
type CACertParseError struct {
	Err error
}

// Error returns a human-readable description of the CA certificate parse failure.
func (e *CACertParseError) Error() string {
	return fmt.Sprintf("ceremony: CA certificate parse failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CACertParseError) Unwrap() error {
	return e.Err
}

// CAKeyParseError wraps an error from CA private key parsing.
type CAKeyParseError struct {
	Err error
}

// Error returns a human-readable description of the CA key parse failure.
func (e *CAKeyParseError) Error() string {
	return fmt.Sprintf("ceremony: CA key parse failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CAKeyParseError) Unwrap() error {
	return e.Err
}

// ShareStoreError wraps an error from share storage operations.
type ShareStoreError struct {
	Err error
}

// Error returns a human-readable description of the share store failure.
func (e *ShareStoreError) Error() string {
	return fmt.Sprintf("ceremony: share store failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *ShareStoreError) Unwrap() error {
	return e.Err
}

// CABundleError wraps an error from CA bundle operations.
type CABundleError struct {
	Err error
}

// Error returns a human-readable description of the CA bundle failure.
func (e *CABundleError) Error() string {
	return fmt.Sprintf("ceremony: CA bundle failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *CABundleError) Unwrap() error {
	return e.Err
}

// ServerTLSError wraps a failure to auto-provision a server TLS
// configuration from the node's CeremonyCA (see ServerTLSConfig): either the
// CA could not issue the leaf certificate or the issued PEM material could not
// be assembled into a tls.Certificate.
type ServerTLSError struct {
	Err error
}

// Error returns a human-readable description of the server TLS provisioning
// failure.
func (e *ServerTLSError) Error() string {
	return fmt.Sprintf("ceremony: server TLS auto-provision failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *ServerTLSError) Unwrap() error {
	return e.Err
}

// SPKIPinError wraps an error from SPKI pin computation.
type SPKIPinError struct {
	Err error
}

// Error returns a human-readable description of the SPKI pin failure.
func (e *SPKIPinError) Error() string {
	return fmt.Sprintf("ceremony: SPKI pin computation failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *SPKIPinError) Unwrap() error {
	return e.Err
}

// ErrNilFactorRegistry indicates an enrollment plan was checked against a nil
// auth-factor registry.
var ErrNilFactorRegistry = errors.New("ceremony: factor registry is nil")

// UnsupportedFactorError indicates an admin factor this binary cannot enroll:
// either the name is not an enrollable factor, or no implementation of it is
// registered. It is deliberately not a silent skip — an operator who asked for a
// passkey and got only a certificate would not discover it until the passkey
// failed to work.
type UnsupportedFactorError struct {
	Factor AdminFactor
}

// Error names the rejected factor alongside what is accepted, so an operator
// who mistyped one can fix it without reading the source.
func (e *UnsupportedFactorError) Error() string {
	return fmt.Sprintf("ceremony: admin factor %q is not supported; accepted: %s",
		e.Factor, strings.Join(AdminFactorNames(), ", "))
}

// FactorUnavailableError indicates a supported factor whose enrollment cannot
// proceed right now — typically absent hardware. Reason carries the
// operator-facing explanation.
type FactorUnavailableError struct {
	Factor AdminFactor
	Reason string
}

// Error returns a human-readable description of why the factor is unavailable.
func (e *FactorUnavailableError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("ceremony: admin factor %q is unavailable", e.Factor)
	}
	return fmt.Sprintf("ceremony: admin factor %q is unavailable: %s", e.Factor, e.Reason)
}

// FactorEnrollmentError wraps a failure enrolling one admin factor, naming the
// factor so an operator knows which credential is missing rather than that
// "enrollment failed".
type FactorEnrollmentError struct {
	Factor AdminFactor
	Err    error
}

// Error describes which factor failed to enroll.
func (e *FactorEnrollmentError) Error() string {
	return fmt.Sprintf("ceremony: enrolling admin factor %q failed: %v", e.Factor, e.Err)
}

// Unwrap returns the underlying error.
func (e *FactorEnrollmentError) Unwrap() error { return e.Err }

// InvalidEnrollmentSpecError is returned when an operator-supplied
// "<username>:<kind>:<path>" argument cannot be parsed.
type InvalidEnrollmentSpecError struct {
	Arg    string
	Reason string
}

// Error implements error.
func (e *InvalidEnrollmentSpecError) Error() string {
	return "ceremony: invalid enrollment spec " + strconv.Quote(e.Arg) + " (" + e.Reason +
		"); kind is one of: " + strings.Join(EnrollmentKindNames(), ", ")
}

// DuplicateEnrollmentUsernameError is returned when the same username appears
// in more than one enrollment argument.
type DuplicateEnrollmentUsernameError struct {
	Username string
}

// Error implements error.
func (e *DuplicateEnrollmentUsernameError) Error() string {
	return "ceremony: username " + strconv.Quote(e.Username) +
		" appears more than once; each identity is enrolled exactly once"
}

// NilCeremonyCAProviderError is returned by NewProviderCA when given a nil
// cakey.Provider. A nil seam is refused rather than silently leaving the
// resulting ProviderCA unable to sign anything.
type NilCeremonyCAProviderError struct{}

// Error implements error.
func (e *NilCeremonyCAProviderError) Error() string {
	return "ceremony: nil cakey.Provider"
}

// ProviderCASignerError wraps a failure resolving the signer from a
// ProviderCA's cakey.Provider (e.g. the barrier is sealed, or an injected
// HSM/TPM/KMS backend is unreachable).
type ProviderCASignerError struct {
	Err error
}

// Error implements error.
func (e *ProviderCASignerError) Error() string {
	return fmt.Sprintf("ceremony: resolving the CA signer failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *ProviderCASignerError) Unwrap() error {
	return e.Err
}

// ProviderCACertificateError wraps a failure resolving the certificate from
// a ProviderCA's cakey.Provider.
type ProviderCACertificateError struct {
	Err error
}

// Error implements error.
func (e *ProviderCACertificateError) Error() string {
	return fmt.Sprintf("ceremony: resolving the CA certificate failed: %v", e.Err)
}

// Unwrap returns the underlying error.
func (e *ProviderCACertificateError) Unwrap() error {
	return e.Err
}

// ConflictingAdminCredentialInputsError is returned when an initialize
// request carries both AdminCSRPEM and AdminCertPEM (go-qrdb#548). The two
// select different bootstrap-admin credential paths for the same identity --
// CSR-sign and bring-your-own-certificate -- and there is no defensible way
// to choose on the operator's behalf: signing the CSR would leave the
// presented certificate unbound, and adopting the certificate would silently
// discard the CSR.
type ConflictingAdminCredentialInputsError struct{}

// Error implements error.
func (e *ConflictingAdminCredentialInputsError) Error() string {
	return "ceremony: AdminCSRPEM and AdminCertPEM are mutually exclusive: supply exactly one admin credential path"
}

// NoAdminCredentialSelectedError is returned when an initialize request
// selects none of the three admin-credential paths: AdminCSRPEM,
// AdminCertPEM and AdminGenerate are all unset (go-qrdb#548 review). There is
// no credential-less default: a caller that wants the generate path (NOT
// suitable for production) must set AdminGenerate explicitly, the same way
// it must set AdminCSRPEM or AdminCertPEM to select either other path.
type NoAdminCredentialSelectedError struct{}

// Error implements error.
func (e *NoAdminCredentialSelectedError) Error() string {
	return "ceremony: no admin credential path selected: set exactly one of AdminCSRPEM, AdminCertPEM or AdminGenerate"
}

// AdminCredentialRejectedError wraps the reason the bootstrap admin's
// requested credential (a CSR, a presented certificate, or the generated
// key/CSR pair) could not be turned into a bound-ready certificate: an
// unparseable trust pool, a certificate outside its validity window, a
// certificate that does not chain to the given (or default cluster) CA, or a
// CSR the ceremony CA refused.
type AdminCredentialRejectedError struct {
	Cause error
}

// Error implements error.
func (e *AdminCredentialRejectedError) Error() string {
	return "ceremony: bootstrap admin credential rejected: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *AdminCredentialRejectedError) Unwrap() error { return e.Cause }

// AdminCredentialIssuanceError wraps a failure signing the bootstrap admin's
// certificate, whether the CSR came from the operator (Path B) or was
// generated in-process (Path A).
type AdminCredentialIssuanceError struct {
	Cause error
}

// Error implements error.
func (e *AdminCredentialIssuanceError) Error() string {
	return "ceremony: bootstrap admin credential issuance failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *AdminCredentialIssuanceError) Unwrap() error { return e.Cause }

// SystemBarrierInitError wraps a failure initializing or unsealing the
// system barrier as part of Initialize (go-qrdb#548): qrdb init is the only
// system bootstrap, so this is the one place that barrier is ever brought
// up on a fresh node.
type SystemBarrierInitError struct {
	Cause error
}

// Error implements error.
func (e *SystemBarrierInitError) Error() string {
	return "ceremony: system barrier initialization failed: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *SystemBarrierInitError) Unwrap() error { return e.Cause }

// SystemBarrierCredentialMismatchError is returned by Initialize when the
// system barrier is ALREADY unsealed and the secret the caller just
// supplied does NOT unseal the same key the barrier currently holds
// (go-qrdb#548 review, item 1). A barrier being unsealed already says
// nothing about whether this secret is the right one, and reporting the
// ceremony operational under a secret that does not actually open the
// barrier would let a caller believe it holds a working credential when it
// does not. Distinct from SystemBarrierInitError, which covers a SEALED
// barrier failing to come up.
type SystemBarrierCredentialMismatchError struct {
	Cause error
}

// Error implements error.
func (e *SystemBarrierCredentialMismatchError) Error() string {
	return "ceremony: system barrier is already unsealed under a different credential: " + e.Cause.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *SystemBarrierCredentialMismatchError) Unwrap() error { return e.Cause }

// NoServerCertificateError is returned by Initialize when it is called with
// a nil CeremonyCA (a BYO-TLS node: the operator's own leaf certificate is
// the server identity, so there is no CA to self-issue one from) and
// CeremonyConfig.ServerCertPEM is empty or unparseable. The composer
// (cmd/qrdbd, sdk/go/node) must set it from the node's own configured TLS
// certificate whenever it constructs a CeremonyService with no CeremonyCA.
type NoServerCertificateError struct {
	// Cause is the parse failure, when ServerCertPEM was non-empty but
	// unparseable. Nil when it was empty.
	Cause error
}

// Error implements error.
func (e *NoServerCertificateError) Error() string {
	if e.Cause != nil {
		return "ceremony: configured server certificate could not be parsed: " + e.Cause.Error()
	}
	return "ceremony: no CeremonyCA configured and no server certificate available to compute the SPKI pin from"
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *NoServerCertificateError) Unwrap() error { return e.Cause }

// AdminCredentialRequiresCAError is returned by issueAdminCredential when
// the CSR-sign or generate admin-credential path is selected (or defaulted
// to) but no CeremonyCA is configured to sign with -- a BYO-TLS node, where
// the operator holds the signing key, not this process. Path names
// "csr-sign" and "generate". The bring-your-own-certificate path
// (--admin-cert) needs no signing CA at all and is the one this error
// names as supported.
//
// The cluster CA provider (cakey.Provider) exists today, but a BYO-TLS node
// is composed with NO cluster CA key by design (Node.ClusterCAProv stays
// nil; cmd/qrdbd withholds the operator's certificate from the provider,
// per TLSBundle.ExplicitTLS), so this refusal is gated purely on ca == nil
// and stays in effect for that shape regardless of the cluster CA's own
// availability elsewhere in the deployment.
type AdminCredentialRequiresCAError struct {
	Path string
}

// Error implements error.
func (e *AdminCredentialRequiresCAError) Error() string {
	return "ceremony: the " + e.Path + " admin-credential path requires a configured CeremonyCA, " +
		"which this node has none of (BYO-TLS); use --admin-cert (bring-your-own-certificate) instead"
}
