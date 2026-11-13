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
	"errors"
	"fmt"
	"strings"
)

// Configuration validation sentinel errors used by NewManager.
var (
	// ErrProviderRequired is returned when the provider field is empty.
	ErrProviderRequired = errors.New("embedding: provider is required")

	// ErrAPIURLRequired is returned when the API URL is empty.
	ErrAPIURLRequired = errors.New("embedding: api_url is required")

	// ErrAPIKeyRequired is returned when an API key is required but not provided.
	ErrAPIKeyRequired = errors.New("embedding: api_key is required for this provider")

	// ErrModelRequired is returned when the model field is empty.
	ErrModelRequired = errors.New("embedding: model is required")

	// ErrDimensionsPositive is returned when dimensions is negative.
	ErrDimensionsPositive = errors.New("embedding: dimensions must be non-negative")

	// ErrMaxBatchSizePositive is returned when max_batch_size is negative.
	ErrMaxBatchSizePositive = errors.New("embedding: max_batch_size must be non-negative")

	// ErrTimeoutPositive is returned when timeout is negative.
	ErrTimeoutPositive = errors.New("embedding: timeout must be non-negative")
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

// ModelNotFoundError is returned when a provider does not recognise the
// requested model name.
type ModelNotFoundError struct {
	// Model is the unrecognised model identifier.
	Model string

	// Cause is the optional underlying error that triggered this condition.
	Cause error
}

// Error implements the error interface.
func (e *ModelNotFoundError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("embedding: model not found: %s: %v", e.Model, e.Cause)
	}
	return fmt.Sprintf("embedding: model not found: %s", e.Model)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ModelNotFoundError) Unwrap() error {
	return e.Cause
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

// ProviderCloseError wraps a failure returned by an individual provider's
// Close during Manager shutdown. The Manager aggregates these into a single
// CloseError. It preserves errors.Is/errors.As against the cause.
type ProviderCloseError struct {
	// Name is the registered provider name that failed to close.
	Name string
	// Cause is the underlying error returned by the provider's Close.
	Cause error
}

// Error returns "provider <name>: <cause>".
func (e *ProviderCloseError) Error() string {
	return "provider " + e.Name + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ProviderCloseError) Unwrap() error { return e.Cause }

// VoyageInvalidIndexError is returned when the Voyage API responds with a data
// index that falls outside the range of the submitted texts.
type VoyageInvalidIndexError struct {
	// Index is the out-of-range index returned by the provider.
	Index int
	// TextCount is the number of texts that were submitted.
	TextCount int
}

// Error implements the error interface.
func (e *VoyageInvalidIndexError) Error() string {
	return fmt.Sprintf("embedding: voyage returned invalid index %d for %d texts", e.Index, e.TextCount)
}

// VoyageMissingEmbeddingError is returned when the Voyage API response omits an
// embedding for one of the submitted texts.
type VoyageMissingEmbeddingError struct {
	// Index is the position whose embedding was missing.
	Index int
}

// Error implements the error interface.
func (e *VoyageMissingEmbeddingError) Error() string {
	return fmt.Sprintf("embedding: voyage did not return embedding at index %d", e.Index)
}
