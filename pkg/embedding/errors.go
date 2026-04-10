//go:build qrdb_vector

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

package embedding

import (
	"fmt"
	"strings"
)

// InvalidInputError is returned when the caller passes an empty or nil text slice.
type InvalidInputError struct{}

// Error implements the error interface.
func (e *InvalidInputError) Error() string {
	return "embedding: input texts must not be empty"
}

// EmptyResponseError is returned when the provider returns a response containing
// no embedding data.
type EmptyResponseError struct{}

// Error implements the error interface.
func (e *EmptyResponseError) Error() string {
	return "embedding: provider returned empty response"
}

// RetryExhaustedError is returned when all retry attempts for a request have
// been consumed without a successful response.
type RetryExhaustedError struct {
	// Attempts is the total number of attempts that were made.
	Attempts int

	// Cause is the last error observed before retries were exhausted.
	Cause error
}

// Error implements the error interface.
func (e *RetryExhaustedError) Error() string {
	return fmt.Sprintf("embedding: all %d retry attempts exhausted: %v", e.Attempts, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *RetryExhaustedError) Unwrap() error {
	return e.Cause
}

// ProviderUnavailableError is returned when the embedding provider cannot be
// reached over the network.
type ProviderUnavailableError struct {
	// BaseURL is the provider endpoint that was unreachable.
	BaseURL string

	// Cause is the network-level error that triggered this error.
	Cause error
}

// Error implements the error interface.
func (e *ProviderUnavailableError) Error() string {
	return fmt.Sprintf("embedding: provider unavailable at %s: %v", e.BaseURL, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ProviderUnavailableError) Unwrap() error {
	return e.Cause
}

// HTTPError is returned when the provider responds with a non-2xx HTTP status code.
type HTTPError struct {
	// StatusCode is the HTTP status code received.
	StatusCode int

	// Body is the response body text, useful for diagnostics.
	Body string
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	return fmt.Sprintf("embedding: HTTP %d: %s", e.StatusCode, e.Body)
}

// DimensionMismatchError is returned when the embedding dimensions returned by
// the provider do not match the expected dimensions.
type DimensionMismatchError struct {
	// Expected is the number of dimensions that was anticipated.
	Expected int

	// Received is the number of dimensions that the provider actually returned.
	Received int
}

// Error implements the error interface.
func (e *DimensionMismatchError) Error() string {
	return fmt.Sprintf("embedding: dimension mismatch: expected %d, received %d", e.Expected, e.Received)
}

// MarshalError is returned when the client fails to marshal the request body.
type MarshalError struct {
	// Cause is the underlying marshalling error.
	Cause error
}

// Error implements the error interface.
func (e *MarshalError) Error() string {
	return fmt.Sprintf("embedding: failed to marshal request: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *MarshalError) Unwrap() error {
	return e.Cause
}

// UnmarshalError is returned when the client fails to parse the provider response.
type UnmarshalError struct {
	// Cause is the underlying unmarshalling error.
	Cause error
}

// Error implements the error interface.
func (e *UnmarshalError) Error() string {
	return fmt.Sprintf("embedding: failed to unmarshal response: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *UnmarshalError) Unwrap() error {
	return e.Cause
}

// CacheCapacityError is returned when an invalid cache capacity is provided.
type CacheCapacityError struct {
	// Capacity is the invalid value that was supplied.
	Capacity int
}

// Error implements the error interface.
func (e *CacheCapacityError) Error() string {
	return fmt.Sprintf("embedding: cache capacity must be positive, got %d", e.Capacity)
}

// ProviderAlreadyRegisteredError is returned when attempting to register a
// provider under a name that is already in use.
type ProviderAlreadyRegisteredError struct {
	// Name is the duplicate provider name.
	Name string
}

// Error implements the error interface.
func (e *ProviderAlreadyRegisteredError) Error() string {
	return fmt.Sprintf("embedding: provider already registered: %s", e.Name)
}

// ProviderNotFoundError is returned when a requested provider name does not
// exist in the registry.
type ProviderNotFoundError struct {
	// Name is the provider name that could not be found.
	Name string
}

// Error implements the error interface.
func (e *ProviderNotFoundError) Error() string {
	return fmt.Sprintf("embedding: provider not found: %s", e.Name)
}

// CloseError is returned when one or more errors occur during cleanup.
type CloseError struct {
	// Errors is the list of errors that occurred while closing resources.
	Errors []error
}

// Error implements the error interface.
func (e *CloseError) Error() string {
	msgs := make([]string, len(e.Errors))
	for i, err := range e.Errors {
		msgs[i] = err.Error()
	}
	return fmt.Sprintf("embedding: close errors: [%s]", strings.Join(msgs, "; "))
}

// Unwrap returns the list of underlying errors to support errors.Join semantics.
func (e *CloseError) Unwrap() []error {
	return e.Errors
}
