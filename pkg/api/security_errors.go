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

package api

import (
	"fmt"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// =============================================================================
// Security Error Types
// =============================================================================

// ManagerClosedError indicates a manager has been closed and can no longer be used.
type ManagerClosedError struct {
	Manager string
}

// Error implements the error interface.
func (e *ManagerClosedError) Error() string {
	return e.Manager + " is closed"
}

// NewManagerClosedError creates a DragonError for a closed manager.
func NewManagerClosedError(op, manager string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &ManagerClosedError{Manager: manager},
	}
}

// DataTruncatedError indicates binary data is too short or corrupted.
type DataTruncatedError struct {
	Field   string
	Message string
}

// Error implements the error interface.
func (e *DataTruncatedError) Error() string {
	if e.Field != "" {
		return e.Field + " " + e.Message
	}
	return e.Message
}

// NewDataTruncatedError creates a DragonError for truncated or corrupted data.
func NewDataTruncatedError(op, field, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &DataTruncatedError{Field: field, Message: message},
	}
}

// NotInitializedError indicates a component has not been initialized.
type NotInitializedError struct {
	Component string
}

// Error implements the error interface.
func (e *NotInitializedError) Error() string {
	return e.Component + " not initialized"
}

// NewNotInitializedError creates a DragonError for an uninitialized component.
func NewNotInitializedError(op, component string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrPreconditionFailed,
		Op:   op,
		Err:  &NotInitializedError{Component: component},
	}
}

// AlreadyInProgressError indicates an operation is already in progress.
type AlreadyInProgressError struct {
	Operation string
}

// Error implements the error interface.
func (e *AlreadyInProgressError) Error() string {
	return e.Operation + " already in progress"
}

// NewAlreadyInProgressError creates a DragonError for an operation already in progress.
func NewAlreadyInProgressError(op, operation string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrAlreadyExists,
		Op:   op,
		Err:  &AlreadyInProgressError{Operation: operation},
	}
}

// NoActiveOperationError indicates no active operation exists to cancel or query.
type NoActiveOperationError struct {
	Operation string
}

// Error implements the error interface.
func (e *NoActiveOperationError) Error() string {
	return "no active " + e.Operation + " to cancel"
}

// NewNoActiveOperationError creates a DragonError for a missing active operation.
func NewNoActiveOperationError(op, operation string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &NoActiveOperationError{Operation: operation},
	}
}

// UnsupportedProviderError indicates an unsupported key provider.
type UnsupportedProviderError struct {
	Provider string
}

// Error implements the error interface.
func (e *UnsupportedProviderError) Error() string {
	return "unsupported key provider: " + e.Provider
}

// NewUnsupportedProviderError creates a DragonError for an unsupported key provider.
func NewUnsupportedProviderError(op, provider string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &UnsupportedProviderError{Provider: provider},
	}
}

// InitializationRequiredError indicates a backend requires explicit initialization.
type InitializationRequiredError struct {
	Backend     string
	Initializer string
}

// Error implements the error interface.
func (e *InitializationRequiredError) Error() string {
	return e.Backend + " backend requires explicit initialization - use " + e.Initializer
}

// NewInitializationRequiredError creates a DragonError for a backend needing explicit initialization.
func NewInitializationRequiredError(op, backend, initializer string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &InitializationRequiredError{Backend: backend, Initializer: initializer},
	}
}

// UnsupportedOperationError indicates an operation is not supported by this backend.
type UnsupportedOperationError struct {
	Operation string
	Backend   string
}

// Error implements the error interface.
func (e *UnsupportedOperationError) Error() string {
	return e.Operation + " operation not supported for " + e.Backend + " backend"
}

// NewUnsupportedOperationError creates a DragonError for an unsupported operation.
func NewUnsupportedOperationError(op, operation, backend string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotImplemented,
		Op:   op,
		Err:  &UnsupportedOperationError{Operation: operation, Backend: backend},
	}
}

// EnvVarNotFoundError indicates an environment variable was not found.
type EnvVarNotFoundError struct {
	Name string
}

// Error implements the error interface.
func (e *EnvVarNotFoundError) Error() string {
	return "environment variable not found: " + e.Name
}

// NewEnvVarNotFoundError creates a DragonError for a missing environment variable.
func NewEnvVarNotFoundError(op, name string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &EnvVarNotFoundError{Name: name},
	}
}

// KeyVersionNotFoundError indicates an encryption key version was not found.
type KeyVersionNotFoundError struct {
	Version byte
}

// Error implements the error interface.
func (e *KeyVersionNotFoundError) Error() string {
	return fmt.Sprintf("encryption key version %d not found", e.Version)
}

// NewKeyVersionNotFoundError creates a DragonError for a missing encryption key version.
func NewKeyVersionNotFoundError(op string, version byte) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &KeyVersionNotFoundError{Version: version},
	}
}

// PEMDecodeError indicates a PEM block could not be decoded.
type PEMDecodeError struct {
	Subject string
}

// Error implements the error interface.
func (e *PEMDecodeError) Error() string {
	return "failed to decode " + e.Subject + " PEM"
}

// NewPEMDecodeError creates a DragonError for a PEM decoding failure.
func NewPEMDecodeError(op, subject string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &PEMDecodeError{Subject: subject},
	}
}

// SealedError indicates the database is sealed and cannot perform the operation.
type SealedError struct{}

// Error implements the error interface.
func (e *SealedError) Error() string {
	return "database is sealed"
}

// NewSealedError creates a DragonError for a sealed database condition.
func NewSealedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &SealedError{},
	}
}

// CryptoError indicates a cryptographic operation failed.
type CryptoError struct {
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *CryptoError) Error() string {
	if e.Cause != nil {
		return e.Operation + " failed: " + e.Cause.Error()
	}
	return e.Operation + " failed"
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *CryptoError) Unwrap() error {
	return e.Cause
}

// NewCryptoError creates a DragonError for a cryptographic operation failure.
func NewCryptoError(op, operation string, code dberrors.ErrorCode, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: code,
		Op:   op,
		Err:  &CryptoError{Operation: operation, Cause: cause},
	}
}

// WrappingKeyError indicates a wrapping key validation failure.
type WrappingKeyError struct {
	Message string
}

// Error implements the error interface.
func (e *WrappingKeyError) Error() string {
	return e.Message
}

// NewWrappingKeyError creates a DragonError for a wrapping key issue.
func NewWrappingKeyError(op, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &WrappingKeyError{Message: message},
	}
}

// BatchEncryptionError indicates an encryption failure for a specific item in a batch.
type BatchEncryptionError struct {
	Index     int
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *BatchEncryptionError) Error() string {
	return fmt.Sprintf("failed to %s item %d: %v", e.Operation, e.Index, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *BatchEncryptionError) Unwrap() error {
	return e.Cause
}

// NewBatchEncryptionError creates a DragonError for a batch encryption/decryption failure.
func NewBatchEncryptionError(op, operation string, index int, code dberrors.ErrorCode, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: code,
		Op:   op,
		Err:  &BatchEncryptionError{Index: index, Operation: operation, Cause: cause},
	}
}

// NoActiveKeyError indicates no active encryption key is available.
type NoActiveKeyError struct{}

// Error implements the error interface.
func (e *NoActiveKeyError) Error() string {
	return "no active encryption key available"
}

// NewNoActiveKeyError creates a DragonError for a missing active encryption key.
func NewNoActiveKeyError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &NoActiveKeyError{},
	}
}

// CiphertextTooShortError indicates the ciphertext is too short to decrypt.
type CiphertextTooShortError struct {
	Detail string
}

// Error implements the error interface.
func (e *CiphertextTooShortError) Error() string {
	if e.Detail != "" {
		return "ciphertext too short: " + e.Detail
	}
	return "ciphertext too short"
}

// NewCiphertextTooShortError creates a DragonError for a ciphertext that is too short.
func NewCiphertextTooShortError(op, detail string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrDecryption,
		Op:   op,
		Err:  &CiphertextTooShortError{Detail: detail},
	}
}

// NoEncrypterError indicates no encrypter is available.
type NoEncrypterError struct{}

// Error implements the error interface.
func (e *NoEncrypterError) Error() string {
	return "no encrypter available"
}

// NewNoEncrypterError creates a DragonError for a missing encrypter.
func NewNoEncrypterError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &NoEncrypterError{},
	}
}

// UnsupportedAlgorithmError indicates an unsupported encryption algorithm.
type UnsupportedAlgorithmError struct {
	Algorithm string
	Cause     error
}

// Error implements the error interface.
func (e *UnsupportedAlgorithmError) Error() string {
	if e.Cause != nil {
		return "unsupported algorithm: " + e.Algorithm + ": " + e.Cause.Error()
	}
	return "unsupported algorithm: " + e.Algorithm
}

// Unwrap returns the underlying cause.
func (e *UnsupportedAlgorithmError) Unwrap() error {
	return e.Cause
}

// NewUnsupportedAlgorithmError creates a DragonError for an unsupported algorithm.
func NewUnsupportedAlgorithmError(op, algorithm string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedAlgorithmError{Algorithm: algorithm, Cause: cause},
	}
}

// StorageOperationError indicates a storage operation failed.
type StorageOperationError struct {
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *StorageOperationError) Error() string {
	return e.Operation + " failed: " + e.Cause.Error()
}

// Unwrap returns the underlying cause.
func (e *StorageOperationError) Unwrap() error {
	return e.Cause
}

// NewStorageOperationError creates a DragonError for a storage operation failure.
func NewStorageOperationError(op, operation string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrStorage,
		Op:   op,
		Err:  &StorageOperationError{Operation: operation, Cause: cause},
	}
}
