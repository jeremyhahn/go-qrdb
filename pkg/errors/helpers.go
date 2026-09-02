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

package errors

import (
	"errors"
	"fmt"
)

// New creates a new QRDBError with the specified code, operation, and message.
func New(code ErrorCode, op string, message string) *QRDBError {
	return &QRDBError{
		Code:  code,
		Op:    op,
		Err:   errors.New(message),
		Retry: code.IsRetryable(),
	}
}

// Newf creates a new QRDBError with formatted message.
func Newf(code ErrorCode, op string, format string, args ...interface{}) *QRDBError {
	return &QRDBError{
		Code:  code,
		Op:    op,
		Err:   &MessageError{Message: fmt.Sprintf(format, args...)},
		Retry: code.IsRetryable(),
	}
}

// Wrap wraps err in a QRDBError carrying the CALLER's classification.
//
// The code passed in is the code the result reports, whether or not err
// already contains a *QRDBError, and Retry is derived from that code. The
// inner error stays in the chain, so errors.As still reaches it and every
// sentinel below it still matches through Unwrap.
//
// THIS USED TO PRESERVE THE INNER CODE AND SILENTLY DISCARD THE ARGUMENT
// (go-qrdb#187, owner-ruled 2026-09-02), which made every wrap intended as a
// RECLASSIFICATION a no-op. Two consequences, and the second was the
// dangerous one:
//
//   - Is(err, code) resolves the first *QRDBError via errors.As and compares
//     its Code, so it reported the INNER classification. A caller branching on
//     cancellation silently took the wrong branch.
//   - Retry was inherited along with Code. retry.Do says "I stopped because
//     the context ended" via Wrap(lastErr, ErrCanceled, "retry.Do") and handed
//     back an error marked RETRYABLE, because the inner timeout was. A caller
//     honouring Retry would retry an operation that was cancelled.
//
// The behaviour was also TYPE-DEPENDENT, which is why it survived: wrapping a
// plain fmt.Errorf took the other branch and applied the code correctly. A
// unit test written with a plain error passed while production -- where the
// inner error is virtually always a QRDBError from a real operation -- did
// not. Any new test for this function wraps a real *QRDBError.
//
// Wrapf never had the special case and always honoured its code argument;
// Wrap was the inconsistent one.
func Wrap(err error, code ErrorCode, op string) error {
	if err == nil {
		return nil
	}

	return &QRDBError{
		Code:    code,
		Op:      op,
		Err:     err,
		Context: inheritedContext(err),
		Retry:   code.IsRetryable(),
	}
}

// inheritedContext carries the context map of the innermost wrapped
// *QRDBError forward onto the wrapper, so wrapping does not lose the
// key-values a lower layer attached.
//
// It COPIES rather than aliasing. The previous implementation assigned
// de.Context directly, so the wrapper and the wrapped error shared one map and
// a WithContext call on either mutated both -- an aliasing bug that was
// invisible because nothing wrapped-then-annotated. A copy costs one small
// allocation on a path that is already allocating an error.
func inheritedContext(err error) map[string]interface{} {
	var de *QRDBError
	if !errors.As(err, &de) || len(de.Context) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(de.Context))
	for k, v := range de.Context {
		out[k] = v
	}
	return out
}

// Wrapf wraps an error with additional formatted context.
func Wrapf(err error, code ErrorCode, op string, format string, args ...interface{}) error {
	if err == nil {
		return nil
	}

	return &QRDBError{
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

	var de *QRDBError
	if errors.As(err, &de) {
		return de.Code == code
	}

	return false
}

// As is a helper for errors.As with QRDBError.
func As(err error, target **QRDBError) bool {
	return errors.As(err, target)
}

// Code extracts the error code from an error, or returns ErrUnknown.
func Code(err error) ErrorCode {
	if err == nil {
		return ErrUnknown
	}

	var de *QRDBError
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

	var de *QRDBError
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

	var de *QRDBError
	if errors.As(err, &de) {
		return de.IsRetryable()
	}

	return false
}

// GetContext retrieves context from a QRDBError.
func GetContext(err error, key string) (interface{}, bool) {
	if err == nil {
		return nil, false
	}

	var de *QRDBError
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
	var de *QRDBError
	if errors.As(err, &de) {
		return de.Code == ErrCircuitOpen
	}
	return false
}
