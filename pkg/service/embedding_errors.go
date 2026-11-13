// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package service

// EmbeddingProviderRegistrationError is returned when registering a provider
// fails, for example when the name is already in use.
type EmbeddingProviderRegistrationError struct {
	// Provider is the name of the provider that failed to register.
	Provider string

	// Cause is the underlying error that prevented registration.
	Cause error
}

// Error returns a human-readable description of the registration failure.
func (e *EmbeddingProviderRegistrationError) Error() string {
	return "service: embedding provider registration failed: " + e.Provider + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EmbeddingProviderRegistrationError) Unwrap() error {
	return e.Cause
}

// EmbeddingProviderNotFoundError is returned when the requested provider name
// does not exist in the registry.
type EmbeddingProviderNotFoundError struct {
	// Provider is the name that was not found.
	Provider string
}

// Error returns a human-readable description of the missing provider.
func (e *EmbeddingProviderNotFoundError) Error() string {
	return "service: embedding provider not found: " + e.Provider
}

// EmbeddingServiceClosedError is returned when an operation is attempted on
// an embedding service that has already been closed.
type EmbeddingServiceClosedError struct{}

// Error returns a human-readable description of the closed-service condition.
func (e *EmbeddingServiceClosedError) Error() string {
	return "service: embedding service is closed"
}
