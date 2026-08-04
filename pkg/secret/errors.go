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

// Package secret provides a shared secret-reference resolver for resolving
// URI-addressed secrets (e.g. "credential://db-password") to their plaintext
// values at runtime. It ships a ResolverRegistry for ordered dispatch, a
// zeroizable SecretValue, a CredentialResolver backed by the qrdb credential
// store, tenant-aware credential routing, and a reflection-based
// ConfigProcessor that resolves string fields in arbitrary config structs.
package secret

import "errors"

var (
	// ErrNilContext is returned when a nil context is passed to a method that
	// requires a live context for cancellation and deadline propagation.
	ErrNilContext = errors.New("secret: nil context")

	// ErrUnresolvedSecretURI is returned by ConfigProcessor.Process when
	// WithFailClosed is set and at least one known-scheme secret reference
	// could not be resolved, or when resolution errors were encountered.
	ErrUnresolvedSecretURI = errors.New("secret: unresolved secret reference remains after resolution")
)

// unexported sentinel used inside CredentialResolver.Resolve when the
// retrieve function was never set.
var errNilRetrieve = errors.New("nil credential retrieve function")

// ErrNilSystemCredentialService is returned by a tenant-scoped
// CredentialRetrieveFunc when a system-scope request arrives but no system
// CredentialService was supplied to NewTenantScopedCredentialRetrieve.
var ErrNilSystemCredentialService = errors.New("secret: nil system credential service")

// InvalidSecretURIError is returned when a raw secret reference is syntactically
// invalid — for example when the name portion after stripping the scheme prefix
// is empty.
type InvalidSecretURIError struct {
	// Raw is the original unresolved reference string.
	Raw string
}

// Error implements the error interface.
func (e *InvalidSecretURIError) Error() string {
	return "secret: invalid secret reference: " + e.Raw
}

// SecretNotFoundError is returned when the named secret does not exist in
// the target backend.
type SecretNotFoundError struct {
	// Scheme is the URI scheme of the resolver that reported the miss.
	Scheme string

	// Name is the credential name that was not found.
	Name string
}

// Error implements the error interface.
func (e *SecretNotFoundError) Error() string {
	return "secret: " + e.Scheme + ": not found: " + e.Name
}

// ResolutionFailedError is returned when a SecretResolver whose CanResolve
// returned true subsequently encounters an error during resolution.
type ResolutionFailedError struct {
	// Scheme is the URI scheme of the resolver that failed.
	Scheme string

	// Raw is the original unresolved reference string.
	Raw string

	// Err is the underlying cause.
	Err error
}

// Error implements the error interface.
func (e *ResolutionFailedError) Error() string {
	return "secret: " + e.Scheme + ": resolution failed for " + e.Raw + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.As / errors.Is traversal.
func (e *ResolutionFailedError) Unwrap() error { return e.Err }

// ProcessorInputError is returned by ConfigProcessor.Process when the cfg
// argument is nil, not a pointer, or points to a non-struct value.
type ProcessorInputError struct {
	// Msg describes the validation failure.
	Msg string
}

// Error implements the error interface.
func (e *ProcessorInputError) Error() string { return "secret: processor: " + e.Msg }

// MaxDepthExceededError is recorded by ConfigProcessor.Process when the config
// graph is nested more deeply than the walker's safety limit, which usually
// indicates a reference cycle. The walk stops descending at the offending path
// but continues with the rest of the structure.
type MaxDepthExceededError struct {
	// Path is the dot-notation field path where the limit was reached.
	Path string
}

// Error implements the error interface.
func (e *MaxDepthExceededError) Error() string {
	return "secret: processor: max walk depth exceeded at " + e.Path
}
