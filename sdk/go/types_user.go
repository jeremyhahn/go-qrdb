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
)

const (
	// CertBindingSPKIThumbprint is the canonical default (SHA-256 over SPKI).
	CertBindingSPKIThumbprint = user.CertBindingSPKIThumbprint
	// CertBindingFullCertFingerprint pins the exact certificate.
	CertBindingFullCertFingerprint = user.CertBindingFullCertFingerprint
	// CertBindingPIVAttested marks a PIV-attested binding.
	CertBindingPIVAttested = user.CertBindingPIVAttested
)

// ComputeSPKIThumbprint is THE single canonical mTLS-identity algorithm
// (SHA-256 over SubjectPublicKeyInfo, base64url). Consumers must not reimplement
// it.
var ComputeSPKIThumbprint = user.ComputeSPKIThumbprint

// UserValidationError is returned when a user request fails validation.
type UserValidationError = user.ValidationError
