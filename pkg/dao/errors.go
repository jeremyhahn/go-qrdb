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

package dao

import (
	"errors"
	"fmt"
)

var (
	// ErrStopIteration is returned to signal early termination of iteration.
	// This is not an error condition; it simply stops the iteration.
	ErrStopIteration = errors.New("dao: stop iteration")
)

// NotFoundError indicates the requested entity was not found.
type NotFoundError struct {
	EntityType string
	ID         uint64
}

func (e NotFoundError) Error() string {
	return fmt.Sprintf("dao: %s with ID %d not found", e.EntityType, e.ID)
}

// IsNotFound returns true if the error is a NotFoundError.
// It handles both value and pointer error types.
func IsNotFound(err error) bool {
	var notFound NotFoundError
	var notFoundPtr *NotFoundError
	return errors.As(err, &notFound) || errors.As(err, &notFoundPtr)
}

// SerializationFailedError indicates entity serialization failed.
type SerializationFailedError struct {
	EntityType string
	Cause      error
}

func (e SerializationFailedError) Error() string {
	return fmt.Sprintf("dao: failed to serialize %s: %v", e.EntityType, e.Cause)
}

func (e SerializationFailedError) Unwrap() error {
	return e.Cause
}

// DeserializationFailedError indicates entity deserialization failed.
type DeserializationFailedError struct {
	EntityType string
	Cause      error
}

func (e DeserializationFailedError) Error() string {
	return fmt.Sprintf("dao: failed to deserialize %s: %v", e.EntityType, e.Cause)
}

func (e DeserializationFailedError) Unwrap() error {
	return e.Cause
}

// InvalidEntityIDError indicates an operation was attempted with an invalid entity ID.
type InvalidEntityIDError struct {
	EntityType string
	Reason     string
}

func (e InvalidEntityIDError) Error() string {
	return fmt.Sprintf("dao: invalid entity ID for %s: %s", e.EntityType, e.Reason)
}

// CodecCreationError indicates codec creation failed.
type CodecCreationError struct {
	CodecType string
	Cause     error
}

func (e CodecCreationError) Error() string {
	return fmt.Sprintf("dao: failed to create codec %s: %v", e.CodecType, e.Cause)
}

func (e CodecCreationError) Unwrap() error {
	return e.Cause
}

// InvalidConfigError indicates invalid DAO configuration.
type InvalidConfigError struct {
	Field  string
	Reason string
}

func (e InvalidConfigError) Error() string {
	return fmt.Sprintf("dao: invalid config %s: %s", e.Field, e.Reason)
}

// TransactionClosedError indicates an operation was attempted on a closed transaction.
type TransactionClosedError struct {
	Op string
}

func (e TransactionClosedError) Error() string {
	return fmt.Sprintf("dao: transaction is closed, cannot perform %s", e.Op)
}

// IsTransactionClosed returns true if the error is a TransactionClosedError.
func IsTransactionClosed(err error) bool {
	var tcErr TransactionClosedError
	var tcErrPtr *TransactionClosedError
	return errors.As(err, &tcErr) || errors.As(err, &tcErrPtr)
}

// TransactionCommitError indicates a transaction commit failed.
type TransactionCommitError struct {
	Cause error
}

func (e TransactionCommitError) Error() string {
	return fmt.Sprintf("dao: transaction commit failed: %v", e.Cause)
}

func (e TransactionCommitError) Unwrap() error {
	return e.Cause
}

// IsTransactionCommitError returns true if the error is a TransactionCommitError.
func IsTransactionCommitError(err error) bool {
	var tcErr TransactionCommitError
	var tcErrPtr *TransactionCommitError
	return errors.As(err, &tcErr) || errors.As(err, &tcErrPtr)
}

// InvalidEntityError indicates an invalid entity was provided.
type InvalidEntityError struct {
	Reason string
}

func (e InvalidEntityError) Error() string {
	return fmt.Sprintf("dao: invalid entity: %s", e.Reason)
}

// IsInvalidEntity returns true if the error is an InvalidEntityError.
func IsInvalidEntity(err error) bool {
	var ieErr InvalidEntityError
	var ieErrPtr *InvalidEntityError
	return errors.As(err, &ieErr) || errors.As(err, &ieErrPtr)
}

// InvalidConditionError indicates an invalid condition was provided.
type InvalidConditionError struct {
	Reason string
}

func (e InvalidConditionError) Error() string {
	return fmt.Sprintf("dao: invalid condition: %s", e.Reason)
}

// IsInvalidCondition returns true if the error is an InvalidConditionError.
func IsInvalidCondition(err error) bool {
	var icErr InvalidConditionError
	var icErrPtr *InvalidConditionError
	return errors.As(err, &icErr) || errors.As(err, &icErrPtr)
}

// ConditionFailedError indicates a precondition check failed.
type ConditionFailedError struct {
	Condition Condition
	Reason    string
}

func (e ConditionFailedError) Error() string {
	return fmt.Sprintf("dao: condition failed (%s): %s", e.Condition.String(), e.Reason)
}

// IsConditionFailed returns true if the error is a ConditionFailedError.
func IsConditionFailed(err error) bool {
	var cfErr ConditionFailedError
	var cfErrPtr *ConditionFailedError
	return errors.As(err, &cfErr) || errors.As(err, &cfErrPtr)
}

// UnitOfWorkClosedError indicates an operation was attempted on a closed UnitOfWork.
type UnitOfWorkClosedError struct {
	Op string
}

func (e UnitOfWorkClosedError) Error() string {
	return fmt.Sprintf("dao: unit of work is closed, cannot perform %s", e.Op)
}

// IsUnitOfWorkClosed returns true if the error is a UnitOfWorkClosedError.
func IsUnitOfWorkClosed(err error) bool {
	var uowErr UnitOfWorkClosedError
	var uowErrPtr *UnitOfWorkClosedError
	return errors.As(err, &uowErr) || errors.As(err, &uowErrPtr)
}

// UnitOfWorkCommitError indicates a UnitOfWork commit failed at a specific operation.
// OpIndex identifies which operation in the batch failed.
// Key is the storage key that failed.
type UnitOfWorkCommitError struct {
	OpIndex int
	Key     string
	Cause   error
}

func (e UnitOfWorkCommitError) Error() string {
	return fmt.Sprintf("dao: unit of work commit failed at operation %d (key=%s): %v",
		e.OpIndex, e.Key, e.Cause)
}

func (e UnitOfWorkCommitError) Unwrap() error {
	return e.Cause
}

// IsUnitOfWorkCommitError returns true if the error is a UnitOfWorkCommitError.
func IsUnitOfWorkCommitError(err error) bool {
	var uowErr UnitOfWorkCommitError
	var uowErrPtr *UnitOfWorkCommitError
	return errors.As(err, &uowErr) || errors.As(err, &uowErrPtr)
}
