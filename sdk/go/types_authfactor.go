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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/auth"

// ---------------------------------------------------------------------------
// Auth factor types
// ---------------------------------------------------------------------------

// AuthFactor is a single enrolled authentication factor for a principal.
type AuthFactor = auth.AuthFactor

// AuthFactorType identifies the kind of an authentication factor.
type AuthFactorType = auth.AuthFactorType

// AuthFactorType constants.
const (
	// AuthFactorMTLS is the mutual-TLS authentication factor.
	AuthFactorMTLS = auth.AuthFactorMTLS

	// AuthFactorFIDO2 is the FIDO2/WebAuthn authentication factor.
	AuthFactorFIDO2 = auth.AuthFactorFIDO2

	// AuthFactorPIV is the PIV smartcard authentication factor.
	AuthFactorPIV = auth.AuthFactorPIV
)

// Factor provenance constants.
const (
	// FactorCreatedBySelf marks a factor enrolled by the principal.
	FactorCreatedBySelf = auth.FactorCreatedBySelf

	// FactorCreatedByAdmin marks a factor enrolled by an administrator.
	FactorCreatedByAdmin = auth.FactorCreatedByAdmin
)

// CredentialKey hashes raw certificate/blob bytes into a stable lookup key
// for a stored credential factor. (Aliases pkg/auth.CredentialKey.)
var CredentialKey = auth.CredentialKey

// Principal is an authenticated subject resolved during login.
type Principal = auth.Principal

// ---------------------------------------------------------------------------
// Login resolution
// ---------------------------------------------------------------------------

// FactorStore is the interface for persisting and querying auth factors.
type FactorStore = auth.FactorStore

// UserLookup resolves principals by identifier during login.
type UserLookup = auth.UserLookup

// LoginResolver resolves a login attempt to a principal using enrolled
// factors.
type LoginResolver = auth.LoginResolver

// NewLoginResolver creates a new LoginResolver.
var NewLoginResolver = auth.NewLoginResolver

// ---------------------------------------------------------------------------
// Auth factor errors
// ---------------------------------------------------------------------------

// LastFactorError is returned when removing the last active factor.
type LastFactorError = auth.LastFactorError

// UserHasNoFactorsError is returned when a principal has no enrolled factors.
type UserHasNoFactorsError = auth.UserHasNoFactorsError

// FactorRevokedError is returned when verification is attempted against a
// revoked authentication factor.
type FactorRevokedError = auth.FactorRevokedError

// FactorTypeUnsupportedError is returned when a verifier receives a factor of
// an unsupported type.
type FactorTypeUnsupportedError = auth.FactorTypeUnsupportedError

// ErrFactorRevoked is returned when a factor has been revoked.
var ErrFactorRevoked = auth.ErrFactorRevoked

// ErrLastFactor is returned when attempting to remove the last active factor.
var ErrLastFactor = auth.ErrLastFactor
