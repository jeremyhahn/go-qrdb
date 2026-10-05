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

// SecretValue holds a resolved secret sealed in a go-enclave sealed enclave
// together with resolution metadata. Read the plaintext through WithBytes or
// Open, both of which keep it in mlocked memory rather than copying it onto
// the GC heap. Every default display path is already redacted — fmt verbs go
// through fmt.Formatter and structured logging through slog.LogValuer, both
// of which render Redacted() — so there is no String or Bytes accessor to
// bypass that accidentally. Call Clear when done to destroy the enclave.
//
// SecretValue is also the ConfigProcessor's protected resolution target:
// declare a config field as SecretValue instead of string and the resolved
// plaintext is sealed into the field's own enclave rather than written back as
// a plain Go string. It decodes from, and re-encodes to, the plain reference
// string in YAML and JSON, so config documents are unchanged.
//
// WithBytes holds the transient buffer's borrow for fn's entire call — a
// behaviour change in go-qrdb#114. Previously fn received an alias whose
// borrow had already been released. Now a concurrent WipeAll skips the buffer
// (counting it in WipeAll's remaining total) instead of unmapping it under fn,
// and, with the borrow guard armed (secure.GuardBorrows /
// secure.EnableBorrowGuard), a callback that calls back into sdk/go/secure is
// reported as a nested borrow. fn must still not retain the slice, and must
// not block for longer than the caller is willing to delay a shutdown sweep of
// that buffer.
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

// NewSecretValue constructs a SecretValue from a raw reference, byte value,
// and strategy. The value bytes are sealed into a go-enclave sealed enclave.
// Returns (nil, *secret.SealFailedError) if sealing fails — BREAKING as of
// go-qrdb#114: this used to swallow a sealing failure and return a
// SecretValue whose accessors silently reported empty. Every caller must be
// updated to check the error.
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

// WithNoPlaintextStrings returns a ProcessorOption that makes Process refuse to
// write resolved plaintext into a plain string field, reporting each such field
// in ProcessResult.PlaintextSkipped instead. It is the migration ratchet onto
// protected SecretValue config fields.
var WithNoPlaintextStrings = secret.WithNoPlaintextStrings

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

// SecretValueDecodeError is returned when a config node cannot be decoded into
// a SecretValue because it is not a scalar reference string.
type SecretValueDecodeError = secret.SecretValueDecodeError

// UnwritableTargetError is recorded by ConfigProcessor.Process when a
// SecretValue target is reachable but cannot be written to, so a fail-closed
// walk reports it rather than silently accepting it.
type UnwritableTargetError = secret.UnwritableTargetError

// UnresolvableTargetError is recorded by ConfigProcessor.Process when a
// protected SecretValue target holds nothing it can resolve — an empty value,
// an inline "literal://" secret, a bare value, or an unclaimed scheme.
type UnresolvableTargetError = secret.UnresolvableTargetError

// SealFailedError is returned by NewSecretValue when sealing the resolved
// plaintext into protected memory fails. Its Ref field is already redacted —
// see the canonical documentation — so it never carries the plaintext under
// any field, printed or not (go-qrdb#114 review, round 3). Re-exported so an
// SDK-only consumer can errors.As against it without importing the internal
// pkg/secret path.
type SealFailedError = secret.SealFailedError

// ResolvedReadError is recorded by ConfigProcessor.Process when a plain string
// field's reference resolved but the resolved value could not be read back out
// of protected memory. The field keeps its reference, is reported Unresolved,
// and is never overwritten with "" or counted as resolved.
type ResolvedReadError = secret.ResolvedReadError
