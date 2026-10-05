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

package dao

import (
	"errors"
	"fmt"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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

// AtomicWriteUnsupportedError is SaveIfAbsent's refusal when the store cannot
// perform an atomic conditional write, for either of the two reasons a store
// can have: its type offers no kvstore.AtomicKVStore at all (Err is nil), or
// it offers one and refused the operation at call time with
// kvstore.ErrAtomicWriteUnsupported (Err is that refusal).
//
// Both are the same condition to a caller — nothing was written, and no retry
// against this store will write it — so both match
// kvstore.ErrAtomicWriteUnsupported, the stack's one marker for the condition,
// and a store's refusal stays reachable through Unwrap. Any other store error
// is an outcome-unknown write and is returned unchanged.
//
// It is a refusal, not a fallback. Emulating the operation with a read followed
// by a write would appear to succeed while allowing two callers to create the
// same entity, which for a single-use credential means issuing it twice.
type AtomicWriteUnsupportedError struct {
	// EntityType is the DAO's entity namespace.
	EntityType string

	// Err is the store's own refusal, or nil when the store has no atomic
	// write at all.
	Err error
}

// Error returns a human-readable description naming the entity type.
func (e *AtomicWriteUnsupportedError) Error() string {
	msg := "dao: " + e.EntityType + ": underlying store does not support atomic conditional writes"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Is reports the canonical kvstore.ErrAtomicWriteUnsupported marker, and
// kvstore.ErrNotApplied: the refusal happened before anything was written.
func (e *AtomicWriteUnsupportedError) Is(target error) bool {
	return target == kvstore.ErrAtomicWriteUnsupported || target == kvstore.ErrNotApplied
}

// Unwrap returns the store's refusal, if any.
func (e *AtomicWriteUnsupportedError) Unwrap() error { return e.Err }

// AtomicBatchUnsupportedError is UnitOfWork.Commit's refusal when the
// underlying store cannot apply the queued operations as one atomic batch,
// for either of the two reasons a store can have: its type offers no
// kvstore.MultiGuardedBatchKVStore at all (Err is nil), or it offers one and
// the backing state machine refused the proposal at call time (Err is that
// refusal, typically a *kvstore.GuardedBatchUnsupportedError).
//
// It is a refusal, not a fallback. Before go-qrdb#589's coordinator ruling,
// Commit degraded to a sequential apply with a best-effort compensating
// rollback on failure when the store offered no batch primitive -- NOT
// atomic against a concurrent reader, since a mid-batch failure could leave
// a move half-applied (the delete landed, the put did not, or the reverse)
// for exactly as long as compensation takes to run, visible the whole time.
// A caller that needs the guarantee now fails closed instead of silently
// getting a weaker one.
type AtomicBatchUnsupportedError struct {
	// Err is the store's own refusal, or nil when the store has no atomic
	// batch primitive at all.
	Err error
}

// Error returns a human-readable description of the refused capability.
func (e *AtomicBatchUnsupportedError) Error() string {
	msg := "dao: unit of work: underlying store does not support atomic multi-key batches"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Is reports the canonical kvstore.ErrAtomicWriteUnsupported marker, and
// kvstore.ErrNotApplied: the refusal happened before anything was written.
func (e *AtomicBatchUnsupportedError) Is(target error) bool {
	return target == kvstore.ErrAtomicWriteUnsupported || target == kvstore.ErrNotApplied
}

// Unwrap returns the store's refusal, if any.
func (e *AtomicBatchUnsupportedError) Unwrap() error { return e.Err }
