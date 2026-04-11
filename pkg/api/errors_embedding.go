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

package api

import (
	"fmt"
	"strconv"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// =============================================================================
// Embedding Provider Error Types
// =============================================================================

// EmbeddingInvalidInputError indicates invalid input was provided to an embedding operation.
type EmbeddingInvalidInputError struct {
	Message  string
	Sentinel error
}

// Error implements the error interface.
func (e *EmbeddingInvalidInputError) Error() string {
	return "invalid input: " + e.Message
}

// Unwrap returns the sentinel error for errors.Is support.
func (e *EmbeddingInvalidInputError) Unwrap() error {
	return e.Sentinel
}

// NewEmbeddingInvalidInputError creates a QRDBError for invalid embedding input.
func NewEmbeddingInvalidInputError(op, message string, sentinel error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmbeddingInvalidInputError{Message: message, Sentinel: sentinel},
	}
}

// EmbeddingEmptyResponseError indicates no embeddings were returned from the provider.
type EmbeddingEmptyResponseError struct{}

// Error implements the error interface.
func (e *EmbeddingEmptyResponseError) Error() string {
	return "no embeddings returned"
}

// NewEmbeddingEmptyResponseError creates a QRDBError for an empty embedding response.
func NewEmbeddingEmptyResponseError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingEmptyResponseError{},
	}
}

// EmbeddingEmptyInResponseError indicates no embeddings data in the parsed API response.
type EmbeddingEmptyInResponseError struct{}

// Error implements the error interface.
func (e *EmbeddingEmptyInResponseError) Error() string {
	return "no embeddings in response"
}

// NewEmbeddingEmptyInResponseError creates a QRDBError for an empty parsed embedding response.
func NewEmbeddingEmptyInResponseError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingEmptyInResponseError{},
	}
}

// EmbeddingRetryExhaustedError indicates all retry attempts failed.
type EmbeddingRetryExhaustedError struct {
	Retries int
	Cause   error
}

// Error implements the error interface.
func (e *EmbeddingRetryExhaustedError) Error() string {
	return "failed after " + strconv.Itoa(e.Retries) + " retries: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EmbeddingRetryExhaustedError) Unwrap() error {
	return e.Cause
}

// NewEmbeddingRetryExhaustedError creates a QRDBError for exhausted retries.
func NewEmbeddingRetryExhaustedError(op string, retries int, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &EmbeddingRetryExhaustedError{Retries: retries, Cause: cause},
	}
}

// EmbeddingMarshalRequestError indicates failure to marshal an embedding API request.
type EmbeddingMarshalRequestError struct {
	Cause error
}

// Error implements the error interface.
func (e *EmbeddingMarshalRequestError) Error() string {
	return "failed to marshal request: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EmbeddingMarshalRequestError) Unwrap() error {
	return e.Cause
}

// NewEmbeddingMarshalRequestError creates a QRDBError for a request marshaling failure.
func NewEmbeddingMarshalRequestError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingMarshalRequestError{Cause: cause},
	}
}

// EmbeddingCreateRequestError indicates failure to create an HTTP request.
type EmbeddingCreateRequestError struct {
	Cause error
}

// Error implements the error interface.
func (e *EmbeddingCreateRequestError) Error() string {
	return "failed to create request: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EmbeddingCreateRequestError) Unwrap() error {
	return e.Cause
}

// NewEmbeddingCreateRequestError creates a QRDBError for a request creation failure.
func NewEmbeddingCreateRequestError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingCreateRequestError{Cause: cause},
	}
}

// EmbeddingProviderUnavailableError indicates the embedding provider is not reachable.
type EmbeddingProviderUnavailableError struct {
	Detail string
	Cause  error
}

// Error implements the error interface.
func (e *EmbeddingProviderUnavailableError) Error() string {
	if e.Cause != nil {
		return "provider unavailable: " + e.Cause.Error()
	}
	return "provider unavailable: " + e.Detail
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EmbeddingProviderUnavailableError) Unwrap() error {
	return e.Cause
}

// NewEmbeddingProviderUnavailableError creates a QRDBError for an unavailable provider.
func NewEmbeddingProviderUnavailableError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &EmbeddingProviderUnavailableError{Cause: cause},
	}
}

// NewEmbeddingServerNotRunningError creates a QRDBError for a server that is not running.
func NewEmbeddingServerNotRunningError(op, url string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &EmbeddingProviderUnavailableError{Detail: "server not running at " + url},
	}
}

// EmbeddingReadResponseError indicates failure to read an embedding API response body.
type EmbeddingReadResponseError struct {
	Cause error
}

// Error implements the error interface.
func (e *EmbeddingReadResponseError) Error() string {
	return "failed to read response: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EmbeddingReadResponseError) Unwrap() error {
	return e.Cause
}

// NewEmbeddingReadResponseError creates a QRDBError for a response read failure.
func NewEmbeddingReadResponseError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingReadResponseError{Cause: cause},
	}
}

// EmbeddingUnmarshalResponseError indicates failure to unmarshal an embedding API response.
type EmbeddingUnmarshalResponseError struct {
	Cause error
}

// Error implements the error interface.
func (e *EmbeddingUnmarshalResponseError) Error() string {
	return "failed to unmarshal response: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EmbeddingUnmarshalResponseError) Unwrap() error {
	return e.Cause
}

// NewEmbeddingUnmarshalResponseError creates a QRDBError for a response unmarshal failure.
func NewEmbeddingUnmarshalResponseError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingUnmarshalResponseError{Cause: cause},
	}
}

// EmbeddingInvalidIndexError indicates an embedding response contained an invalid index.
type EmbeddingInvalidIndexError struct {
	Index int
}

// Error implements the error interface.
func (e *EmbeddingInvalidIndexError) Error() string {
	return "invalid embedding index: " + strconv.Itoa(e.Index)
}

// NewEmbeddingInvalidIndexError creates a QRDBError for an invalid embedding index.
func NewEmbeddingInvalidIndexError(op string, index int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingInvalidIndexError{Index: index},
	}
}

// EmbeddingEmptyAtIndexError indicates an empty embedding was received at a specific index.
type EmbeddingEmptyAtIndexError struct {
	Index int
}

// Error implements the error interface.
func (e *EmbeddingEmptyAtIndexError) Error() string {
	return "received empty embedding at index " + strconv.Itoa(e.Index)
}

// NewEmbeddingEmptyAtIndexError creates a QRDBError for an empty embedding at a specific index.
func NewEmbeddingEmptyAtIndexError(op string, index int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingEmptyAtIndexError{Index: index},
	}
}

// EmbeddingMissingAtIndexError indicates a missing embedding at a specific index.
type EmbeddingMissingAtIndexError struct {
	Index int
}

// Error implements the error interface.
func (e *EmbeddingMissingAtIndexError) Error() string {
	return "missing embedding at index " + strconv.Itoa(e.Index)
}

// NewEmbeddingMissingAtIndexError creates a QRDBError for a missing embedding at a specific index.
func NewEmbeddingMissingAtIndexError(op string, index int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingMissingAtIndexError{Index: index},
	}
}

// EmbeddingDimensionsMismatchError indicates a mismatch in embedding dimensions.
type EmbeddingDimensionsMismatchError struct {
	Expected int
	Received int
}

// Error implements the error interface.
func (e *EmbeddingDimensionsMismatchError) Error() string {
	return fmt.Sprintf("dimensions mismatch: expected %d dimensions, got %d", e.Expected, e.Received)
}

// NewEmbeddingDimensionsMismatchError creates a QRDBError for a dimensions mismatch.
func NewEmbeddingDimensionsMismatchError(op string, expected, received int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmbeddingDimensionsMismatchError{Expected: expected, Received: received},
	}
}

// EmbeddingHTTPError indicates an HTTP error from an embedding provider.
type EmbeddingHTTPError struct {
	StatusCode int
	Message    string
	Sentinel   error
}

// Error implements the error interface.
func (e *EmbeddingHTTPError) Error() string {
	if e.Sentinel != nil {
		return e.Sentinel.Error() + ": " + e.Message
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Message)
}

// Unwrap returns the sentinel error for errors.Is support.
func (e *EmbeddingHTTPError) Unwrap() error {
	return e.Sentinel
}

// NewEmbeddingHTTPError creates a QRDBError for an HTTP embedding provider error.
func NewEmbeddingHTTPError(op string, statusCode int, message string, sentinel error) *dberrors.QRDBError {
	code := dberrors.ErrInternal
	if sentinel != nil {
		switch {
		case statusCode == 401 || statusCode == 403:
			code = dberrors.ErrUnauthorized
		case statusCode == 400:
			code = dberrors.ErrInvalidInput
		case statusCode == 404:
			code = dberrors.ErrNotFound
		case statusCode == 429:
			code = dberrors.ErrRateLimited
		case statusCode >= 500:
			code = dberrors.ErrUnavailable
		}
	}
	return &dberrors.QRDBError{
		Code: code,
		Op:   op,
		Err:  &EmbeddingHTTPError{StatusCode: statusCode, Message: message, Sentinel: sentinel},
	}
}

// NewEmbeddingHTTPModelNotFoundError creates a QRDBError for a model not found HTTP error.
func NewEmbeddingHTTPModelNotFoundError(op, model, message string, sentinel error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &EmbeddingHTTPError{StatusCode: 404, Message: "model '" + model + "' - " + message, Sentinel: sentinel},
	}
}

// NewEmbeddingHTTPUnknownError creates a QRDBError for an unknown HTTP status error.
func NewEmbeddingHTTPUnknownError(op string, statusCode int, message string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingHTTPError{StatusCode: statusCode, Message: message},
	}
}

// EmbeddingProviderAlreadyRegisteredError indicates a provider has already been registered.
type EmbeddingProviderAlreadyRegisteredError struct {
	Name string
}

// Error implements the error interface.
func (e *EmbeddingProviderAlreadyRegisteredError) Error() string {
	return "provider " + e.Name + " already registered"
}

// NewEmbeddingProviderAlreadyRegisteredError creates a QRDBError for a duplicate provider registration.
func NewEmbeddingProviderAlreadyRegisteredError(op, name string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrAlreadyExists,
		Op:   op,
		Err:  &EmbeddingProviderAlreadyRegisteredError{Name: name},
	}
}

// EmbeddingCloseProviderError indicates failure to close a specific provider.
type EmbeddingCloseProviderError struct {
	Name  string
	Cause error
}

// Error implements the error interface.
func (e *EmbeddingCloseProviderError) Error() string {
	return "failed to close provider " + e.Name + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EmbeddingCloseProviderError) Unwrap() error {
	return e.Cause
}

// EmbeddingCloseError indicates errors occurred while closing multiple providers.
type EmbeddingCloseError struct {
	Errors []error
}

// Error implements the error interface.
func (e *EmbeddingCloseError) Error() string {
	return fmt.Sprintf("errors closing providers: %v", e.Errors)
}

// NewEmbeddingCloseError creates a QRDBError for errors closing providers.
func NewEmbeddingCloseError(op string, errs []error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EmbeddingCloseError{Errors: errs},
	}
}

// EmbeddingCacheCapacityError indicates an invalid cache capacity was provided.
type EmbeddingCacheCapacityError struct{}

// Error implements the error interface.
func (e *EmbeddingCacheCapacityError) Error() string {
	return "capacity must be positive"
}

// NewEmbeddingCacheCapacityError creates a QRDBError for an invalid cache capacity.
func NewEmbeddingCacheCapacityError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmbeddingCacheCapacityError{},
	}
}

// EmbeddingModelNotFoundError indicates the requested embedding model was not found.
type EmbeddingModelNotFoundError struct {
	Model    string
	Sentinel error
}

// Error implements the error interface.
func (e *EmbeddingModelNotFoundError) Error() string {
	if e.Sentinel != nil {
		return e.Sentinel.Error() + ": " + e.Model
	}
	return "model not found: " + e.Model
}

// Unwrap returns the sentinel error for errors.Is support.
func (e *EmbeddingModelNotFoundError) Unwrap() error {
	return e.Sentinel
}

// NewEmbeddingModelNotFoundError creates a QRDBError for a model not found condition.
func NewEmbeddingModelNotFoundError(op, model string, sentinel error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &EmbeddingModelNotFoundError{Model: model, Sentinel: sentinel},
	}
}
