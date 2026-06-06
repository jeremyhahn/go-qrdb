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

import "github.com/jeremyhahn/go-qrdb/pkg/credentials"

// ---------------------------------------------------------------------------
// Versioned credential types
// ---------------------------------------------------------------------------

// CredentialVersion describes a single version of a named credential.
type CredentialVersion = credentials.CredentialVersion

// RotateOptions configures a credential rotation operation.
type RotateOptions = credentials.RotateOptions

// RotateResult holds the result of a credential rotation.
type RotateResult = credentials.RotateResult

// ---------------------------------------------------------------------------
// Credential configuration
// ---------------------------------------------------------------------------

// CredentialConfig configures the versioned credential subsystem. (Aliases
// pkg/credentials.Config; named CredentialConfig to avoid colliding with
// other Config aliases in the flat SDK package.)
type CredentialConfig = credentials.Config

// CredentialStrategyManual is the manual credential rotation strategy
// identifier. (Aliases pkg/credentials.StrategyManual.)
const CredentialStrategyManual = credentials.StrategyManual

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

// ErrCredentialNotFound is returned when a named credential does not exist.
var ErrCredentialNotFound = credentials.ErrCredentialNotFound

// ErrEmptyCredentialName is returned when a credential name is empty.
var ErrEmptyCredentialName = credentials.ErrEmptyCredentialName

// ErrEmptyCredentialValue is returned when a credential value is empty.
var ErrEmptyCredentialValue = credentials.ErrEmptyCredentialValue

// ErrEnumerationNotSupported is returned when the backend cannot enumerate
// credentials.
var ErrEnumerationNotSupported = credentials.ErrEnumerationNotSupported

// ErrInvalidGracePeriod is returned when a rotation grace period is invalid.
var ErrInvalidGracePeriod = credentials.ErrInvalidGracePeriod

// ErrInvalidStrategy is returned when a credential rotation strategy is
// invalid.
var ErrInvalidStrategy = credentials.ErrInvalidStrategy

// ErrVersionExpired is returned when a credential version has expired.
var ErrVersionExpired = credentials.ErrVersionExpired

// ErrVersionNotFound is returned when a credential version does not exist.
var ErrVersionNotFound = credentials.ErrVersionNotFound

// ---------------------------------------------------------------------------
// Versioned credential service constructor
// ---------------------------------------------------------------------------

// NewVersionedCredentialService creates a new versioned credential service
// (pkg/credentials.CredentialService) backed by the given barrier. This is
// distinct from NewCredentialService, which wraps the result in the
// service-layer CredentialServicer.
var NewVersionedCredentialService = credentials.NewCredentialService

// VersionedCredentialService is the concrete versioned credential store
// (pkg/credentials.CredentialService), distinct from the service-layer
// CredentialService alias.
type VersionedCredentialService = credentials.CredentialService
