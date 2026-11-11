// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package credentials

import (
	"errors"
	"strconv"
	"strings"
)

var (
	// ErrInvalidStrategy is returned when the configured strategy is not recognized.
	ErrInvalidStrategy = errors.New("credentials: invalid seal strategy")

	// ErrNilStore is returned by BindStore, ForTenant and NewKVStore for a
	// nil store.
	ErrNilStore = errors.New("credentials: nil store")

	// ErrStoreNotAtomic is returned by NewKVStore for a store that serves
	// no conditional writes: a rotation claim needs PutIfAbsent and
	// CompareAndSwap decided once for every node that shares the store.
	ErrStoreNotAtomic = errors.New("credentials: store serves no conditional writes")

	// ErrStoreUnavailable is returned by a barrier-strategy call made before
	// the node has bound the replicated store: the node is still sealed, or
	// its system shard's KV has not started.
	ErrStoreUnavailable = errors.New("credentials: credential store unavailable")

	// ErrNilBarrier is returned by ForTenant when it is given no legacy
	// barrier.
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

	// ErrNoRotationValueSource is the cause RotateDue reports for a due
	// credential when the service was built without
	// WithRotationValueSource: go-qrdb generates no credential values.
	ErrNoRotationValueSource = errors.New("credentials: no rotation value source configured")

	// ErrRotationMetaContended is returned when a rotation-policy update
	// loses its compare-and-swap on the metadata record more times than
	// the retry bound allows.
	ErrRotationMetaContended = errors.New("credentials: rotation metadata update contended")

	// ErrRotationClaimLost is returned when a scheduled rotation's commit
	// finds that its claim no longer holds: the credential was deleted,
	// rotated by another writer, or claimed again while the value source
	// ran. The rotation writes nothing further, so it never recreates a
	// deleted credential or overwrites a newer value.
	ErrRotationClaimLost = errors.New("credentials: scheduled rotation claim lost")

	// ErrInvalidRotationCheckPeriod is returned by Config.Validate for a
	// negative rotation check period.
	ErrInvalidRotationCheckPeriod = errors.New("credentials: rotation check period must be positive")

	// ErrInvalidMigrationResolution is returned when a migration conflict
	// is resolved by neither MigrationImport nor MigrationDiscard.
	ErrInvalidMigrationResolution = errors.New("credentials: migration resolution must be import or discard")

	// ErrMigrationConflictNotFound is returned when the named credential
	// has no unresolved migration conflict on this node.
	ErrMigrationConflictNotFound = errors.New("credentials: migration conflict not found")
)

// InvalidRotationPolicyError is returned by SetRotationPolicy for a policy
// the service will not store: an enabled policy with a non-positive
// interval or fewer than one retained version, or any policy on a manual
// service, whose values are non-resident and cannot be rotated unattended.
type InvalidRotationPolicyError struct {
	// Reason says which rule the policy broke.
	Reason string
}

// Error implements error.
func (e *InvalidRotationPolicyError) Error() string {
	return "credentials: invalid rotation policy: " + e.Reason
}

// RotationError reports why RotateDue did not rotate one due credential.
// RotateDue joins one per failed credential with errors.Join.
type RotationError struct {
	// Name is the credential that was due.
	Name string
	// Cause is the failure.
	Cause error
}

// Error implements error.
func (e *RotationError) Error() string {
	return "credentials: scheduled rotation of " + e.Name + ": " + e.Cause.Error()
}

// Unwrap returns the cause.
func (e *RotationError) Unwrap() error { return e.Cause }

// OperationError wraps a credential store or codec operation failure together
// with its underlying cause. It preserves errors.Is/errors.As against the
// cause, replacing the inline "credentials: <op>: %w" error-wrapping idiom.
type OperationError struct {
	// Op names the operation that failed (e.g. "list barrier",
	// "commit rotation", "decode meta").
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

// CredentialMigrationConflictError reports a credential whose node-local
// copy under the old "credentials/" layout differs from the replicated
// record another node already migrated to "__sys:credentials/". The node
// keeps its old records untouched until an operator resolves the conflict
// by importing or discarding the kept copy, so no secret is lost silently.
type CredentialMigrationConflictError struct {
	// Name is the credential name.
	Name string
	// Value reports that the current value differs.
	Value bool
	// Versions reports that the version history differs.
	Versions bool
	// Policy reports that the rotation policy differs.
	Policy bool
}

// Error implements error.
func (e *CredentialMigrationConflictError) Error() string {
	diff := e.Fields()
	if len(diff) == 0 {
		diff = append(diff, "record")
	}
	return "credentials: migration conflict for " + strconv.Quote(e.Name) +
		": local copy differs in " + strings.Join(diff, ", ")
}

// Fields returns the names of the parts that differ, in the order value,
// versions, policy.
func (e *CredentialMigrationConflictError) Fields() []string {
	var diff []string
	if e.Value {
		diff = append(diff, "value")
	}
	if e.Versions {
		diff = append(diff, "versions")
	}
	if e.Policy {
		diff = append(diff, "policy")
	}
	return diff
}
