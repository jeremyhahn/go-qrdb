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
// Seal Package Error Types
// =============================================================================

// AlreadyInitializedError indicates a component has already been initialized.
type AlreadyInitializedError struct {
	Component string
}

// Error implements the error interface.
func (e *AlreadyInitializedError) Error() string {
	return e.Component + " already initialized"
}

// NewAlreadyInitializedError creates a DragonError for a component that is already initialized.
func NewAlreadyInitializedError(op, component string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &AlreadyInitializedError{Component: component},
	}
}

// UnsealRequiredError indicates the database must be unsealed for the operation.
type UnsealRequiredError struct {
	Operation string
}

// Error implements the error interface.
func (e *UnsealRequiredError) Error() string {
	return "database must be unsealed to " + e.Operation
}

// NewUnsealRequiredError creates a DragonError indicating unsealing is required.
func NewUnsealRequiredError(op, operation string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &UnsealRequiredError{Operation: operation},
	}
}

// FeatureNotEnabledError indicates a feature is not enabled.
type FeatureNotEnabledError struct {
	Feature string
}

// Error implements the error interface.
func (e *FeatureNotEnabledError) Error() string {
	return e.Feature + " not enabled"
}

// NewFeatureNotEnabledError creates a DragonError for a disabled feature.
func NewFeatureNotEnabledError(op, feature string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &FeatureNotEnabledError{Feature: feature},
	}
}

// InsufficientSharesError indicates not enough shares were provided.
type InsufficientSharesError struct {
	Required int
	Provided int
}

// Error implements the error interface.
func (e *InsufficientSharesError) Error() string {
	return fmt.Sprintf("need at least %d shares, got %d", e.Required, e.Provided)
}

// NewInsufficientSharesError creates a DragonError for insufficient shares.
func NewInsufficientSharesError(op string, required, provided int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InsufficientSharesError{Required: required, Provided: provided},
	}
}

// TPMUnavailableError indicates TPM hardware is not available.
type TPMUnavailableError struct{}

// Error implements the error interface.
func (e *TPMUnavailableError) Error() string {
	return "TPM hardware not available"
}

// NewTPMUnavailableError creates a DragonError for unavailable TPM hardware.
func NewTPMUnavailableError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &TPMUnavailableError{},
	}
}

// PCRMismatchError indicates PCR values do not match the sealed policy.
type PCRMismatchError struct{}

// Error implements the error interface.
func (e *PCRMismatchError) Error() string {
	return "PCR values do not match sealed policy"
}

// NewPCRMismatchError creates a DragonError for a PCR policy mismatch.
func NewPCRMismatchError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrPreconditionFailed,
		Op:   op,
		Err:  &PCRMismatchError{},
	}
}

// TPMSealModeRequiredError indicates data was sealed with TPM but TPM is unavailable.
type TPMSealModeRequiredError struct{}

// Error implements the error interface.
func (e *TPMSealModeRequiredError) Error() string {
	return "data was sealed with TPM but TPM is not available"
}

// NewTPMSealModeRequiredError creates a DragonError when TPM-sealed data cannot be unsealed.
func NewTPMSealModeRequiredError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &TPMSealModeRequiredError{},
	}
}

// ChannelFullError indicates a channel buffer is full and cannot accept messages.
type ChannelFullError struct {
	Channel string
}

// Error implements the error interface.
func (e *ChannelFullError) Error() string {
	return e.Channel + " channel full"
}

// NewChannelFullError creates a DragonError for a full channel.
func NewChannelFullError(op, channel string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &ChannelFullError{Channel: channel},
	}
}

// ShareTimeoutError indicates a timeout waiting for shares from peers.
type ShareTimeoutError struct {
	Have int
	Need int
}

// Error implements the error interface.
func (e *ShareTimeoutError) Error() string {
	return fmt.Sprintf("timeout waiting for shares: have %d, need %d", e.Have, e.Need)
}

// NewShareTimeoutError creates a DragonError for a share collection timeout.
func NewShareTimeoutError(op string, have, need int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrTimeout,
		Op:   op,
		Err:  &ShareTimeoutError{Have: have, Need: need},
	}
}

// InvalidShareError indicates a share has an invalid format or index.
type InvalidShareError struct {
	Reason string
}

// Error implements the error interface.
func (e *InvalidShareError) Error() string {
	return e.Reason
}

// NewInvalidShareError creates a DragonError for an invalid share.
func NewInvalidShareError(op, reason string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidShareError{Reason: reason},
	}
}

// InconsistentShareLengthError indicates shares have inconsistent lengths.
type InconsistentShareLengthError struct {
	Index int
}

// Error implements the error interface.
func (e *InconsistentShareLengthError) Error() string {
	return fmt.Sprintf("share %d has inconsistent length", e.Index)
}

// NewInconsistentShareLengthError creates a DragonError for inconsistent share lengths.
func NewInconsistentShareLengthError(op string, index int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InconsistentShareLengthError{Index: index},
	}
}

// DuplicateShareIndexError indicates a duplicate share index was detected.
type DuplicateShareIndexError struct{}

// Error implements the error interface.
func (e *DuplicateShareIndexError) Error() string {
	return "duplicate share index detected"
}

// NewDuplicateShareIndexError creates a DragonError for a duplicate share index.
func NewDuplicateShareIndexError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &DuplicateShareIndexError{},
	}
}

// ConfigValidationError indicates a configuration validation failure.
type ConfigValidationError struct {
	Message string
}

// Error implements the error interface.
func (e *ConfigValidationError) Error() string {
	return e.Message
}

// NewConfigValidationError creates a DragonError for a configuration validation failure.
func NewConfigValidationError(op, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &ConfigValidationError{Message: message},
	}
}

// MasterKeyUnavailableError indicates the master key is not available.
type MasterKeyUnavailableError struct{}

// Error implements the error interface.
func (e *MasterKeyUnavailableError) Error() string {
	return "master key not available"
}

// NewMasterKeyUnavailableError creates a DragonError for an unavailable master key.
func NewMasterKeyUnavailableError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &MasterKeyUnavailableError{},
	}
}

// KeyMismatchError indicates that reconstructed shares do not match the master key.
type KeyMismatchError struct{}

// Error implements the error interface.
func (e *KeyMismatchError) Error() string {
	return "provided shares do not reconstruct the correct master key"
}

// NewKeyMismatchError creates a DragonError for a key reconstruction mismatch.
func NewKeyMismatchError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &KeyMismatchError{},
	}
}

// InvalidSealModeError indicates an invalid seal mode was specified.
type InvalidSealModeError struct {
	Mode string
}

// Error implements the error interface.
func (e *InvalidSealModeError) Error() string {
	return "invalid seal mode: " + e.Mode
}

// NewInvalidSealModeError creates a DragonError for an invalid seal mode.
func NewInvalidSealModeError(op, mode string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &InvalidSealModeError{Mode: mode},
	}
}

// SealModeOnlyError indicates an operation is only available in a specific seal mode.
type SealModeOnlyError struct {
	RequiredMode string
}

// Error implements the error interface.
func (e *SealModeOnlyError) Error() string {
	return "operation only available in " + e.RequiredMode + " mode"
}

// NewSealModeOnlyError creates a DragonError for an operation restricted to a specific mode.
func NewSealModeOnlyError(op, requiredMode string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &SealModeOnlyError{RequiredMode: requiredMode},
	}
}

// BootstrapRequiredError indicates that bootstrap must be performed first.
type BootstrapRequiredError struct{}

// Error implements the error interface.
func (e *BootstrapRequiredError) Error() string {
	return "seal manager not initialized - bootstrap required"
}

// NewBootstrapRequiredError creates a DragonError when bootstrap is needed.
func NewBootstrapRequiredError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &BootstrapRequiredError{},
	}
}

// NoLocalShareError indicates no local share is available for sharing.
type NoLocalShareError struct{}

// Error implements the error interface.
func (e *NoLocalShareError) Error() string {
	return "no local share available"
}

// NewNoLocalShareError creates a DragonError for a missing local share.
func NewNoLocalShareError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &NoLocalShareError{},
	}
}

// CoordinatorAlreadyStartedError indicates the coordinator has already been started.
type CoordinatorAlreadyStartedError struct{}

// Error implements the error interface.
func (e *CoordinatorAlreadyStartedError) Error() string {
	return "coordinator already started"
}

// NewCoordinatorAlreadyStartedError creates a DragonError for a coordinator that is already running.
func NewCoordinatorAlreadyStartedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &CoordinatorAlreadyStartedError{},
	}
}

// EmptySecretError indicates an empty secret was provided.
type EmptySecretError struct{}

// Error implements the error interface.
func (e *EmptySecretError) Error() string {
	return "secret cannot be empty"
}

// NewEmptySecretError creates a DragonError for an empty secret.
func NewEmptySecretError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmptySecretError{},
	}
}

// NoSharesProvidedError indicates no shares were provided.
type NoSharesProvidedError struct{}

// Error implements the error interface.
func (e *NoSharesProvidedError) Error() string {
	return "no shares provided"
}

// NewNoSharesProvidedError creates a DragonError when no shares are given.
func NewNoSharesProvidedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NoSharesProvidedError{},
	}
}

// SharesEmptyError indicates shares cannot be empty.
type SharesEmptyError struct{}

// Error implements the error interface.
func (e *SharesEmptyError) Error() string {
	return "shares cannot be empty"
}

// NewSharesEmptyError creates a DragonError when shares are empty.
func NewSharesEmptyError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &SharesEmptyError{},
	}
}

// InvalidStrategyError indicates an invalid strategy was specified.
type InvalidStrategyError struct {
	Name     string
	Strategy string
	Allowed  []string
}

// Error implements the error interface.
func (e *InvalidStrategyError) Error() string {
	return fmt.Sprintf("invalid %s: %s (allowed: %v)", e.Name, e.Strategy, e.Allowed)
}

// NewInvalidStrategyError creates a DragonError for an invalid strategy.
func NewInvalidStrategyError(op, name, strategy string, allowed []string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidStrategyError{Name: name, Strategy: strategy, Allowed: allowed},
	}
}

// =============================================================================
// Seal Key Error Types
// =============================================================================

// NoSealedKeyDataError indicates no sealed key data is available in the barrier.
type NoSealedKeyDataError struct{}

// Error implements the error interface.
func (e *NoSealedKeyDataError) Error() string {
	return "no sealed key data"
}

// NewNoSealedKeyDataError creates a DragonError for missing sealed key data.
func NewNoSealedKeyDataError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &NoSealedKeyDataError{},
	}
}

// AutoUnsealRequirementsError indicates required stores are missing for auto unseal.
type AutoUnsealRequirementsError struct{}

// Error implements the error interface.
func (e *AutoUnsealRequirementsError) Error() string {
	return "TPM and share stores are required for auto unseal"
}

// NewAutoUnsealRequirementsError creates a DragonError for missing auto unseal requirements.
func NewAutoUnsealRequirementsError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrPreconditionFailed,
		Op:   op,
		Err:  &AutoUnsealRequirementsError{},
	}
}

// TPMNotCompiledError indicates TPM support was not compiled into the binary.
type TPMNotCompiledError struct {
	DevicePath string
}

// Error implements the error interface.
func (e *TPMNotCompiledError) Error() string {
	return "TPM support not compiled: build with -tags tpm to enable real TPM device at " + e.DevicePath
}

// NewTPMNotCompiledError creates a DragonError for a binary built without TPM support.
func NewTPMNotCompiledError(op, devicePath string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &TPMNotCompiledError{DevicePath: devicePath},
	}
}

// =============================================================================
// TPM Mock Error Types
// =============================================================================

// TPMDeviceClosedError indicates the TPM device has been closed.
type TPMDeviceClosedError struct{}

// Error implements the error interface.
func (e *TPMDeviceClosedError) Error() string {
	return "TPM device is closed"
}

// NewTPMDeviceClosedError creates a DragonError when a TPM device is closed.
func NewTPMDeviceClosedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &TPMDeviceClosedError{},
	}
}

// TPMCommandFailedError indicates a TPM command failed.
type TPMCommandFailedError struct {
	Command string
}

// Error implements the error interface.
func (e *TPMCommandFailedError) Error() string {
	return e.Command + " failed"
}

// NewTPMCommandFailedError creates a DragonError when a TPM command fails.
func NewTPMCommandFailedError(op, command string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &TPMCommandFailedError{Command: command},
	}
}

// TPMHandleNotFoundError indicates a TPM object was not found at the given handle.
type TPMHandleNotFoundError struct {
	Handle uint32
}

// Error implements the error interface.
func (e *TPMHandleNotFoundError) Error() string {
	return fmt.Sprintf("object not found at handle 0x%x", e.Handle)
}

// NewTPMHandleNotFoundError creates a DragonError when a TPM handle is not found.
func NewTPMHandleNotFoundError(op string, handle uint32) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &TPMHandleNotFoundError{Handle: handle},
	}
}

// TPMSessionNotFoundError indicates a TPM session was not found at the given handle.
type TPMSessionNotFoundError struct {
	Handle uint32
}

// Error implements the error interface.
func (e *TPMSessionNotFoundError) Error() string {
	return fmt.Sprintf("session not found at handle 0x%x", e.Handle)
}

// NewTPMSessionNotFoundError creates a DragonError when a TPM session is not found.
func NewTPMSessionNotFoundError(op string, handle uint32) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &TPMSessionNotFoundError{Handle: handle},
	}
}

// TPMCryptoError indicates a TPM cryptographic operation failed.
type TPMCryptoError struct {
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *TPMCryptoError) Error() string {
	return "failed to " + e.Operation + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause error.
func (e *TPMCryptoError) Unwrap() error {
	return e.Cause
}

// NewTPMCryptoError creates a DragonError for a TPM crypto operation failure.
func NewTPMCryptoError(op, operation string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrEncryption,
		Op:   op,
		Err:  &TPMCryptoError{Operation: operation, Cause: cause},
	}
}

// TPMEmptyDataError indicates data provided to a TPM operation cannot be empty.
type TPMEmptyDataError struct {
	Field string
}

// Error implements the error interface.
func (e *TPMEmptyDataError) Error() string {
	return e.Field + " cannot be empty"
}

// NewTPMEmptyDataError creates a DragonError when TPM data is empty.
func NewTPMEmptyDataError(op, field string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &TPMEmptyDataError{Field: field},
	}
}

// TPMInvalidSealedDataError indicates the sealed data format is invalid.
type TPMInvalidSealedDataError struct {
	Cause error
}

// Error implements the error interface.
func (e *TPMInvalidSealedDataError) Error() string {
	return "invalid sealed data format: " + e.Cause.Error()
}

// Unwrap returns the underlying cause error.
func (e *TPMInvalidSealedDataError) Unwrap() error {
	return e.Cause
}

// NewTPMInvalidSealedDataError creates a DragonError for invalid sealed data.
func NewTPMInvalidSealedDataError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &TPMInvalidSealedDataError{Cause: cause},
	}
}

// TPMDecryptionError indicates a TPM decryption operation failed.
type TPMDecryptionError struct {
	Cause error
}

// Error implements the error interface.
func (e *TPMDecryptionError) Error() string {
	return "decryption failed: " + e.Cause.Error()
}

// Unwrap returns the underlying cause error.
func (e *TPMDecryptionError) Unwrap() error {
	return e.Cause
}

// NewTPMDecryptionError creates a DragonError for a TPM decryption failure.
func NewTPMDecryptionError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrDecryption,
		Op:   op,
		Err:  &TPMDecryptionError{Cause: cause},
	}
}

// SimulatedWriteError indicates a simulated write failure for testing.
type SimulatedWriteError struct{}

// Error implements the error interface.
func (e *SimulatedWriteError) Error() string {
	return "simulated write error"
}

// NewSimulatedWriteError creates a DragonError for a simulated write failure.
func NewSimulatedWriteError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &SimulatedWriteError{},
	}
}

// SimulatedReadError indicates a simulated read failure for testing.
type SimulatedReadError struct{}

// Error implements the error interface.
func (e *SimulatedReadError) Error() string {
	return "simulated read error"
}

// NewSimulatedReadError creates a DragonError for a simulated read failure.
func NewSimulatedReadError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &SimulatedReadError{},
	}
}

// CacheMissError indicates the requested key was not found in the cache.
type CacheMissError struct{}

// Error implements the error interface.
func (e *CacheMissError) Error() string {
	return "cache miss"
}

// NewCacheMissError creates a DragonError for a cache miss.
func NewCacheMissError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &CacheMissError{},
	}
}
