// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import "errors"

var (
	// ErrInvalidStrategy is returned when the configured strategy is not recognized.
	ErrInvalidStrategy = errors.New("credentials: invalid seal strategy")

	// ErrNilBarrier is returned when the barrier strategy is selected but no barrier is provided.
	ErrNilBarrier = errors.New("credentials: nil barrier")

	// ErrNilLogger is returned when a nil logger is provided to the constructor.
	ErrNilLogger = errors.New("credentials: nil logger")

	// ErrEmptyCredentialName is returned when a credential operation receives an empty name.
	ErrEmptyCredentialName = errors.New("credentials: empty credential name")

	// ErrEmptyCredentialValue is returned when a credential operation receives an empty value.
	ErrEmptyCredentialValue = errors.New("credentials: empty credential value")

	// ErrCredentialNotFound is returned when the requested credential does not exist.
	ErrCredentialNotFound = errors.New("credentials: credential not found")

	// ErrCredentialAlreadySubmitted is returned when a manual credential has already been submitted.
	ErrCredentialAlreadySubmitted = errors.New("credentials: credential already submitted")

	// ErrEnumerationNotSupported is returned by ListCredentials when the
	// active backend cannot enumerate stored credentials (e.g. an
	// HSM-backed strategy that only supports get/set by name).
	ErrEnumerationNotSupported = errors.New("credentials: enumeration not supported by backend")

	// ErrVersionNotFound is returned when RetrieveCredentialVersion is
	// asked for a version that was never written.
	ErrVersionNotFound = errors.New("credentials: version not found")

	// ErrVersionExpired is returned when RetrieveCredentialVersion is
	// asked for a historical version whose grace period has elapsed.
	ErrVersionExpired = errors.New("credentials: version grace period expired")

	// ErrInvalidGracePeriod is returned when RotateCredential is called
	// with a negative grace period.
	ErrInvalidGracePeriod = errors.New("credentials: grace period must not be negative")
)

// OperationError wraps a credential store or codec operation failure together
// with its underlying cause. It preserves errors.Is/errors.As against the
// cause, replacing the inline "credentials: <op>: %w" error-wrapping idiom.
type OperationError struct {
	// Op names the operation that failed (e.g. "list barrier",
	// "write current", "decode meta").
	Op string
	// Cause is the underlying error.
	Cause error
}

// Error returns "credentials: <op>: <cause>".
func (e *OperationError) Error() string {
	return "credentials: " + e.Op + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *OperationError) Unwrap() error { return e.Cause }
