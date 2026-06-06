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
