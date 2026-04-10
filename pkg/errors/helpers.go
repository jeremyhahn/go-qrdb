// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package errors

import (
	"errors"
	"fmt"
)

// New creates a new DragonError with the specified code, operation, and message.
func New(code ErrorCode, op string, message string) *DragonError {
	return &DragonError{
		Code:  code,
		Op:    op,
		Err:   errors.New(message),
		Retry: code.IsRetryable(),
	}
}

// Newf creates a new DragonError with formatted message.
func Newf(code ErrorCode, op string, format string, args ...interface{}) *DragonError {
	return &DragonError{
		Code:  code,
		Op:    op,
		Err:   &MessageError{Message: fmt.Sprintf(format, args...)},
		Retry: code.IsRetryable(),
	}
}

// Wrap wraps an existing error with DragonDB error context.
func Wrap(err error, code ErrorCode, op string) error {
	if err == nil {
		return nil
	}

	// If it's already a DragonError, just update the operation
	var de *DragonError
	if errors.As(err, &de) {
		// Preserve the original error but add operation context
		return &DragonError{
			Code:    de.Code,
			Op:      op,
			Err:     de,
			Context: de.Context,
			Retry:   de.Retry,
		}
	}

	return &DragonError{
		Code:  code,
		Op:    op,
		Err:   err,
		Retry: code.IsRetryable(),
	}
}

// Wrapf wraps an error with additional formatted context.
func Wrapf(err error, code ErrorCode, op string, format string, args ...interface{}) error {
	if err == nil {
		return nil
	}

	return &DragonError{
		Code:  code,
		Op:    op,
		Err:   &WrappedMessageError{Message: fmt.Sprintf(format, args...), Cause: err},
		Retry: code.IsRetryable(),
	}
}

// Is checks if an error matches a specific error code.
func Is(err error, code ErrorCode) bool {
	if err == nil {
		return false
	}

	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == code
	}

	return false
}

// As is a helper for errors.As with DragonError.
func As(err error, target **DragonError) bool {
	return errors.As(err, target)
}

// Code extracts the error code from an error, or returns ErrUnknown.
func Code(err error) ErrorCode {
	if err == nil {
		return ErrUnknown
	}

	var de *DragonError
	if errors.As(err, &de) {
		return de.Code
	}

	return ErrUnknown
}

// Operation extracts the operation from an error, or returns empty string.
func Operation(err error) string {
	if err == nil {
		return ""
	}

	var de *DragonError
	if errors.As(err, &de) {
		return de.Op
	}

	return ""
}

// IsRetryable checks if an error is retryable.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	var de *DragonError
	if errors.As(err, &de) {
		return de.IsRetryable()
	}

	return false
}

// GetContext retrieves context from a DragonError.
func GetContext(err error, key string) (interface{}, bool) {
	if err == nil {
		return nil, false
	}

	var de *DragonError
	if errors.As(err, &de) {
		return de.GetContext(key)
	}

	return nil, false
}

// Common error constructors for frequently used errors

// NotFound creates a not found error.
func NotFound(op string, resource string) error {
	return New(ErrNotFound, op, resource+" not found")
}

// AlreadyExists creates an already exists error.
func AlreadyExists(op string, resource string) error {
	return New(ErrAlreadyExists, op, resource+" already exists")
}

// InvalidInput creates an invalid input error.
func InvalidInput(op string, reason string) error {
	return New(ErrInvalidInput, op, reason)
}

// InvalidInputf creates an invalid input error with formatting.
func InvalidInputf(op string, format string, args ...interface{}) error {
	return Newf(ErrInvalidInput, op, format, args...)
}

// Timeout creates a timeout error.
func Timeout(op string, duration string) error {
	return New(ErrTimeout, op, "operation timed out after "+duration)
}

// Internal creates an internal error.
func Internal(op string, reason string) error {
	return New(ErrInternal, op, reason)
}

// Internalf creates an internal error with formatting.
func Internalf(op string, format string, args ...interface{}) error {
	return Newf(ErrInternal, op, format, args...)
}

// Unavailable creates an unavailable error.
func Unavailable(op string, reason string) error {
	return New(ErrUnavailable, op, reason)
}

// Unavailablef creates an unavailable error with formatting.
func Unavailablef(op string, format string, args ...interface{}) error {
	return Newf(ErrUnavailable, op, format, args...)
}

// NotImplemented creates a not implemented error.
func NotImplemented(op string, reason string) error {
	return New(ErrNotImplemented, op, reason)
}

// CircuitOpen creates a circuit open error indicating the circuit breaker is rejecting requests.
func CircuitOpen(op string, name string) error {
	return New(ErrCircuitOpen, op, "circuit breaker '"+name+"' is open")
}

// IsCircuitOpen returns true if the error is a circuit open error.
func IsCircuitOpen(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrCircuitOpen
	}
	return false
}
