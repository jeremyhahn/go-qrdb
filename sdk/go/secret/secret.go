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

// Package secret re-exports go-qrdb's config-time secret-reference resolution
// SPI so SDK consumers never import the internal pkg/secret path. All public
// types and constructors are aliases — there is no logic in this file. Keeping
// them in this subpackage (rather than flattened into the root sdk package)
// preserves their natural names without risking collisions with other
// subsystems in the root namespace.
//
// See github.com/jeremyhahn/go-qrdb/pkg/secret for the canonical documentation.
package secret

import "github.com/jeremyhahn/go-qrdb/pkg/secret"

// ---------------------------------------------------------------------------
// Resolution strategy
// ---------------------------------------------------------------------------

// ResolvedStrategy identifies how a SecretValue was produced.
type ResolvedStrategy = secret.ResolvedStrategy

// ResolvedStrategy constants.
const (
	StrategyLiteral    = secret.StrategyLiteral
	StrategyCredential = secret.StrategyCredential
	StrategyLocalKMS   = secret.StrategyLocalKMS
	StrategyRemoteKMS  = secret.StrategyRemoteKMS
	StrategyAutoGen    = secret.StrategyAutoGen
)

// ---------------------------------------------------------------------------
// Core types
// ---------------------------------------------------------------------------

// SecretValue holds a resolved secret sealed in a memguard enclave together
// with resolution metadata. Prefer WithBytes/Open (which keep the plaintext in
// mlocked memory) over Bytes/String (which copy it into the GC heap). Call
// Clear when done to destroy the enclave.
type SecretValue = secret.SecretValue

// SecretResolver resolves raw secret reference URIs to their plaintext values.
type SecretResolver = secret.SecretResolver

// CredentialRetrieveFunc retrieves a named credential's plaintext value.
type CredentialRetrieveFunc = secret.CredentialRetrieveFunc

// CredentialRetriever is the minimal interface for retrieving a named
// credential. It is satisfied by CredentialServicer.
type CredentialRetriever = secret.CredentialRetriever

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// NewSecretValue constructs a SecretValue from a raw reference, byte value, and
// strategy. The value bytes are sealed into a memguard enclave.
var NewSecretValue = secret.NewSecretValue

// NewResolverRegistry creates an empty ResolverRegistry.
var NewResolverRegistry = secret.NewResolverRegistry

// NewCredentialResolver constructs a CredentialResolver backed by the given
// CredentialRetrieveFunc.
var NewCredentialResolver = secret.NewCredentialResolver

// CredentialResolverFromServicer constructs a CredentialResolver from any
// CredentialRetriever (including CredentialServicer).
var CredentialResolverFromServicer = secret.CredentialResolverFromServicer

// NewTenantScopedCredentialRetrieve returns a CredentialRetrieveFunc that
// routes to the per-tenant credential store when a tenant ID is present in the
// context, falling back to systemSvc otherwise.
var NewTenantScopedCredentialRetrieve = secret.NewTenantScopedCredentialRetrieve

// NewConfigProcessor creates a ConfigProcessor that walks and resolves secret
// references in arbitrary config structs.
var NewConfigProcessor = secret.NewConfigProcessor

// WithFailClosed returns a ProcessorOption that makes Process return
// ErrUnresolvedSecretURI when any secret reference cannot be resolved.
var WithFailClosed = secret.WithFailClosed

// ---------------------------------------------------------------------------
// Aggregated types
// ---------------------------------------------------------------------------

// ResolverRegistry dispatches secret resolution to the first registered
// SecretResolver that claims a raw reference value.
type ResolverRegistry = secret.ResolverRegistry

// ConfigProcessor walks config structs via reflection and resolves secret
// reference strings in place.
type ConfigProcessor = secret.ConfigProcessor

// ProcessResult summarises the outcome of a ConfigProcessor.Process walk.
type ProcessResult = secret.ProcessResult

// ProcessorOption is a functional option for ConfigProcessor.
type ProcessorOption = secret.ProcessorOption

// ---------------------------------------------------------------------------
// Error types
// ---------------------------------------------------------------------------

// ErrNilContext is returned when a nil context is passed to a secret method.
var ErrNilContext = secret.ErrNilContext

// ErrUnresolvedSecretURI is returned by ConfigProcessor.Process under
// WithFailClosed when a secret reference cannot be resolved.
var ErrUnresolvedSecretURI = secret.ErrUnresolvedSecretURI

// ErrNilSystemCredentialService is returned by a tenant-scoped
// CredentialRetrieveFunc when a system-scope request arrives but no system
// CredentialService was supplied.
var ErrNilSystemCredentialService = secret.ErrNilSystemCredentialService

// InvalidSecretURIError is returned when a raw secret reference is
// syntactically invalid.
type InvalidSecretURIError = secret.InvalidSecretURIError

// SecretNotFoundError is returned when the named secret does not exist in the
// target backend.
type SecretNotFoundError = secret.SecretNotFoundError

// ResolutionFailedError is returned when a resolver encounters an error during
// resolution.
type ResolutionFailedError = secret.ResolutionFailedError

// ProcessorInputError is returned when an invalid config value is passed to
// ConfigProcessor.Process.
type ProcessorInputError = secret.ProcessorInputError

// MaxDepthExceededError is recorded by ConfigProcessor.Process when the config
// graph is nested too deeply, which usually indicates a reference cycle.
type MaxDepthExceededError = secret.MaxDepthExceededError
