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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/user"

// User identity model re-exports. This is the canonical user / mTLS cert-binding
// surface for SDK consumers. Higher-level projects (xkms, quicbaas, platform)
// MUST consume these via the SDK — never import pkg/user directly.
type (
	// User is the canonical passwordless user (mTLS core + extensible factors).
	User = user.User

	// UserServicer is the service-layer interface for user operations.
	UserServicer = user.UserServicer
	// CertBinding binds an mTLS certificate to a user by SPKI thumbprint.
	CertBinding = user.CertBinding
	// CertBindingKind selects the cert-matching scheme.
	CertBindingKind = user.CertBindingKind
	// FactorEnrollment is an opaque auth-factor enrollment (webauthn/fido2/piv).
	FactorEnrollment = user.FactorEnrollment
	// RoleBinding grants a role within a tenant scope (tenant-scoped RBAC).
	RoleBinding = user.RoleBinding

	// Lifecycle is the canonical account state — pending, active, suspended,
	// locked, disabled or deleting. It is the SINGLE source of truth for
	// whether an account may authenticate; User.Enabled is a derived projection
	// of it. Consumers must not carry a parallel state field of their own.
	Lifecycle = user.Lifecycle
)

const (
	// LifecyclePending is an account created but not yet activated.
	LifecyclePending = user.LifecyclePending
	// LifecycleActive is the only state in which an account may authenticate.
	LifecycleActive = user.LifecycleActive
	// LifecycleSuspended is an administrative hold.
	LifecycleSuspended = user.LifecycleSuspended
	// LifecycleLocked is an automated lockout.
	LifecycleLocked = user.LifecycleLocked
	// LifecycleDisabled is an administratively deactivated account.
	LifecycleDisabled = user.LifecycleDisabled
	// LifecycleDeleting marks a deletion in progress. It is terminal: nothing
	// transitions out of it to another state, which is what lets a credential
	// ceremony refuse against it without racing a concurrent enable.
	LifecycleDeleting = user.LifecycleDeleting
)

// Lifecycle and identity-set errors. A consumer distinguishes "you may not"
// from "the store failed" by matching these with errors.As.
type (
	// InvalidLifecycleError reports a state that is undefined, or defined but
	// not permitted where it was used (creating an account as suspended, say).
	InvalidLifecycleError = user.InvalidLifecycleError

	// LifecycleTransitionError reports a move the transition table does not
	// connect — most importantly any move out of LifecycleDeleting.
	LifecycleTransitionError = user.LifecycleTransitionError
)

// NormalizeGroupIDs is THE canonical normalization for a group-membership set:
// trimmed, de-duplicated, first-seen order preserved, case PRESERVED because a
// group identifier is an opaque minted token. A consumer that de-duplicates its
// own copy differently will disagree with the store about what the user belongs
// to.
var NormalizeGroupIDs = user.NormalizeGroupIDs

// NormalizeAuthMethods is THE canonical normalization for a permitted-method
// set: trimmed, LOWERCASED, de-duplicated, first-seen order preserved. Case is
// folded here (unlike a group id) because these are protocol names from a
// closed vocabulary, and a list holding both "WebAuthn" and "webauthn" is a
// policy that will eventually deny what it meant to allow.
var NormalizeAuthMethods = user.NormalizeAuthMethods

const (
	// CertBindingSPKIThumbprint is the canonical default (SHA-256 over SPKI).
	CertBindingSPKIThumbprint = user.CertBindingSPKIThumbprint
	// CertBindingFullCertFingerprint pins the exact certificate.
	CertBindingFullCertFingerprint = user.CertBindingFullCertFingerprint
	// CertBindingPIVAttested marks a PIV-attested binding.
	CertBindingPIVAttested = user.CertBindingPIVAttested
)

// NormalizeEmail is THE single canonical normalization for the email login key
// (lowercase, trimmed). Every layer that stores, indexes, or compares an email
// must route through it: a consumer that lowercases its own copy differently
// reintroduces the two-accounts-for-one-person split this key exists to close.
var NormalizeEmail = user.NormalizeEmail

// ComputeSPKIThumbprint is THE single canonical mTLS-identity algorithm
// (SHA-256 over SubjectPublicKeyInfo, base64url). Consumers must not reimplement
// it.
var ComputeSPKIThumbprint = user.ComputeSPKIThumbprint

// UserValidationError is returned when a user request fails validation.
type UserValidationError = user.ValidationError
