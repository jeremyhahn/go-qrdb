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
	"strings"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// =============================================================================
// Configuration Validation Error Types
// =============================================================================

// ConfigSectionValidationError indicates a config section validation failed.
type ConfigSectionValidationError struct {
	Section string
	Cause   error
}

// Error implements the error interface.
func (e *ConfigSectionValidationError) Error() string {
	return fmt.Sprintf("%s config: %v", e.Section, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ConfigSectionValidationError) Unwrap() error {
	return e.Cause
}

// NewConfigSectionValidationError creates a QRDBError for a config section validation failure.
func NewConfigSectionValidationError(op, section string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &ConfigSectionValidationError{Section: section, Cause: cause},
	}
}

// PositiveValueRequiredError indicates a field must have a positive value.
type PositiveValueRequiredError struct {
	Field string
}

// Error implements the error interface.
func (e *PositiveValueRequiredError) Error() string {
	return e.Field + " must be positive"
}

// NewPositiveValueRequiredError creates a QRDBError for a non-positive required value.
func NewPositiveValueRequiredError(op, field string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &PositiveValueRequiredError{Field: field},
	}
}

// NonNegativeValueRequiredError indicates a field cannot be negative.
type NonNegativeValueRequiredError struct {
	Field string
}

// Error implements the error interface.
func (e *NonNegativeValueRequiredError) Error() string {
	return e.Field + " cannot be negative"
}

// NewNonNegativeValueRequiredError creates a QRDBError for a negative value.
func NewNonNegativeValueRequiredError(op, field string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &NonNegativeValueRequiredError{Field: field},
	}
}

// FloatRangeError indicates a float field is outside its valid range.
type FloatRangeError struct {
	Field string
	Min   float32
	Max   float32
}

// Error implements the error interface.
func (e *FloatRangeError) Error() string {
	return fmt.Sprintf("%s must be between %g and %g", e.Field, e.Min, e.Max)
}

// NewFloatRangeError creates a QRDBError for a float value out of range.
func NewFloatRangeError(op, field string, min, max float32) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &FloatRangeError{Field: field, Min: min, Max: max},
	}
}

// ValueExceedsLimitError indicates a value exceeds another value's constraint.
type ValueExceedsLimitError struct {
	Field    string
	Value    int
	LimitOf  string
	LimitVal int
}

// Error implements the error interface.
func (e *ValueExceedsLimitError) Error() string {
	return fmt.Sprintf("%s (%d) cannot exceed %s (%d)",
		e.Field, e.Value, e.LimitOf, e.LimitVal)
}

// NewValueExceedsLimitError creates a QRDBError for a value that exceeds a limit.
func NewValueExceedsLimitError(op, field string, value int, limitOf string, limitVal int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &ValueExceedsLimitError{Field: field, Value: value, LimitOf: limitOf, LimitVal: limitVal},
	}
}

// EmptyWhenEnabledError indicates a field cannot be empty when a feature is enabled.
type EmptyWhenEnabledError struct {
	Field   string
	Feature string
}

// Error implements the error interface.
func (e *EmptyWhenEnabledError) Error() string {
	return fmt.Sprintf("%s cannot be empty when %s is enabled", e.Field, e.Feature)
}

// NewEmptyWhenEnabledError creates a QRDBError for an empty required field when enabled.
func NewEmptyWhenEnabledError(op, field, feature string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &EmptyWhenEnabledError{Field: field, Feature: feature},
	}
}

// ProviderRequirementError indicates a provider-specific requirement is not met.
type ProviderRequirementError struct {
	Provider    string
	Requirement string
}

// Error implements the error interface.
func (e *ProviderRequirementError) Error() string {
	return fmt.Sprintf("%s provider requires %s", e.Provider, e.Requirement)
}

// NewProviderRequirementError creates a QRDBError for a missing provider requirement.
func NewProviderRequirementError(op, provider, requirement string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &ProviderRequirementError{Provider: provider, Requirement: requirement},
	}
}

// DimensionMismatchError indicates an inconsistency between configured dimensions.
type DimensionMismatchError struct {
	Source    string
	SourceDim int
	Target    string
	TargetDim int
}

// Error implements the error interface.
func (e *DimensionMismatchError) Error() string {
	return fmt.Sprintf("dimension mismatch: %s=%d, %s=%d",
		e.Source, e.SourceDim, e.Target, e.TargetDim)
}

// NewDimensionMismatchError creates a QRDBError for a dimension mismatch.
func NewDimensionMismatchError(op, source string, sourceDim int, target string, targetDim int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &DimensionMismatchError{Source: source, SourceDim: sourceDim, Target: target, TargetDim: targetDim},
	}
}

// UnsupportedBackendError indicates an unsupported storage backend type.
type UnsupportedBackendError struct {
	Backend   string
	Supported []string
}

// Error implements the error interface.
func (e *UnsupportedBackendError) Error() string {
	return fmt.Sprintf("unsupported storage backend: %s (supported: %s)",
		e.Backend, strings.Join(e.Supported, ", "))
}

// NewUnsupportedBackendError creates a QRDBError for an unsupported backend.
func NewUnsupportedBackendError(op, backend string, supported []string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &UnsupportedBackendError{Backend: backend, Supported: supported},
	}
}

// StorageKeyNotFoundError indicates a key was not found in storage.
type StorageKeyNotFoundError struct {
	Key string
}

// Error implements the error interface.
func (e *StorageKeyNotFoundError) Error() string {
	return "key not found: " + e.Key
}

// NewStorageKeyNotFoundError creates a QRDBError for a missing storage key.
func NewStorageKeyNotFoundError(op, key string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &StorageKeyNotFoundError{Key: key},
	}
}

// ArchiveNotSupportedError indicates the storage backend does not support archiving.
type ArchiveNotSupportedError struct {
	Backend string
}

// Error implements the error interface.
func (e *ArchiveNotSupportedError) Error() string {
	return "archive not supported for " + e.Backend + " storage"
}

// NewArchiveNotSupportedError creates a QRDBError for an unsupported archive operation.
func NewArchiveNotSupportedError(op, backend string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnimplemented,
		Op:   op,
		Err:  &ArchiveNotSupportedError{Backend: backend},
	}
}

// BackupNotEnabledError indicates backup is not enabled.
type BackupNotEnabledError struct{}

// Error implements the error interface.
func (e *BackupNotEnabledError) Error() string {
	return "backup is not enabled"
}

// NewBackupNotEnabledError creates a QRDBError for disabled backup.
func NewBackupNotEnabledError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrPreconditionFailed,
		Op:   op,
		Err:  &BackupNotEnabledError{},
	}
}

// BackupBackendRequiredError indicates backup storage backend is required.
type BackupBackendRequiredError struct{}

// Error implements the error interface.
func (e *BackupBackendRequiredError) Error() string {
	return "backup storage backend is required when backup is enabled"
}

// NewBackupBackendRequiredError creates a QRDBError for a missing backup backend.
func NewBackupBackendRequiredError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &BackupBackendRequiredError{},
	}
}

// URLSchemeError indicates a URL has an invalid scheme.
type URLSchemeError struct {
	Field          string
	AllowedSchemes []string
}

// Error implements the error interface.
func (e *URLSchemeError) Error() string {
	return fmt.Sprintf("%s must use %s scheme",
		e.Field, strings.Join(e.AllowedSchemes, " or "))
}

// NewURLSchemeError creates a QRDBError for an invalid URL scheme.
func NewURLSchemeError(op, field string, allowedSchemes []string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &URLSchemeError{Field: field, AllowedSchemes: allowedSchemes},
	}
}

// URLMissingHostError indicates a URL is missing a host component.
type URLMissingHostError struct {
	Field string
}

// Error implements the error interface.
func (e *URLMissingHostError) Error() string {
	return e.Field + " must include a host"
}

// NewURLMissingHostError creates a QRDBError for a URL missing a host.
func NewURLMissingHostError(op, field string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &URLMissingHostError{Field: field},
	}
}

// IntRangeError indicates an integer value is outside its valid range with a custom message.
type IntRangeError struct {
	Field string
	Value int
	Min   int
	Max   int
}

// Error implements the error interface.
func (e *IntRangeError) Error() string {
	return fmt.Sprintf("%s must be between %d and %d (got %d)",
		e.Field, e.Min, e.Max, e.Value)
}

// NewIntRangeError creates a QRDBError for an integer value out of range.
func NewIntRangeError(op, field string, value, min, max int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &IntRangeError{Field: field, Value: value, Min: min, Max: max},
	}
}

// PositiveFloatRequiredError indicates a float field must be positive.
type PositiveFloatRequiredError struct {
	Field string
	Value float64
}

// Error implements the error interface.
func (e *PositiveFloatRequiredError) Error() string {
	return fmt.Sprintf("%s must be positive (got %f)", e.Field, e.Value)
}

// NewPositiveFloatRequiredError creates a QRDBError for a non-positive float.
func NewPositiveFloatRequiredError(op, field string, value float64) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &PositiveFloatRequiredError{Field: field, Value: value},
	}
}

// PositiveIntRequiredError indicates an integer field must be positive with the value shown.
type PositiveIntRequiredError struct {
	Field string
	Value int
}

// Error implements the error interface.
func (e *PositiveIntRequiredError) Error() string {
	return fmt.Sprintf("%s must be positive (got %d)", e.Field, e.Value)
}

// NewPositiveIntRequiredError creates a QRDBError for a non-positive integer with value.
func NewPositiveIntRequiredError(op, field string, value int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &PositiveIntRequiredError{Field: field, Value: value},
	}
}

// EntityConfigValidationError indicates a validation failure for a specific entity config.
type EntityConfigValidationError struct {
	Entity string
	Cause  error
}

// Error implements the error interface.
func (e *EntityConfigValidationError) Error() string {
	return fmt.Sprintf("invalid config for entity %s: %v", e.Entity, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EntityConfigValidationError) Unwrap() error {
	return e.Cause
}

// NewEntityConfigValidationError creates a QRDBError for an entity config validation failure.
func NewEntityConfigValidationError(op, entity string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &EntityConfigValidationError{Entity: entity, Cause: cause},
	}
}

// NilConfigError indicates a nil configuration was provided.
type NilConfigError struct{}

// Error implements the error interface.
func (e *NilConfigError) Error() string {
	return "configuration is nil"
}

// NewNilConfigError creates a QRDBError for a nil configuration.
func NewNilConfigError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NilConfigError{},
	}
}

// PathOperationError indicates a filesystem path operation failed.
type PathOperationError struct {
	Action string
	Cause  error
}

// Error implements the error interface.
func (e *PathOperationError) Error() string {
	return fmt.Sprintf("failed to %s: %v", e.Action, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *PathOperationError) Unwrap() error {
	return e.Cause
}

// NewPathOperationError creates a QRDBError for a path operation failure.
func NewPathOperationError(op, action string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &PathOperationError{Action: action, Cause: cause},
	}
}

// NotADirectoryError indicates a path exists but is not a directory.
type NotADirectoryError struct{}

// Error implements the error interface.
func (e *NotADirectoryError) Error() string {
	return "path exists but is not a directory"
}

// NewNotADirectoryError creates a QRDBError for a non-directory path.
func NewNotADirectoryError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NotADirectoryError{},
	}
}

// DirectoryNotWritableError indicates a directory is not writable.
type DirectoryNotWritableError struct {
	Cause error
}

// Error implements the error interface.
func (e *DirectoryNotWritableError) Error() string {
	return fmt.Sprintf("directory is not writable: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *DirectoryNotWritableError) Unwrap() error {
	return e.Cause
}

// NewDirectoryNotWritableError creates a QRDBError for a non-writable directory.
func NewDirectoryNotWritableError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &DirectoryNotWritableError{Cause: cause},
	}
}

// FlagBindError indicates a CLI flag binding failure.
type FlagBindError struct {
	Flag  string
	Cause error
}

// Error implements the error interface.
func (e *FlagBindError) Error() string {
	return fmt.Sprintf("failed to bind flag %s: %v", e.Flag, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *FlagBindError) Unwrap() error {
	return e.Cause
}

// NewFlagBindError creates a QRDBError for a flag binding failure.
func NewFlagBindError(op, flag string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &FlagBindError{Flag: flag, Cause: cause},
	}
}

// ConfigFileReadError indicates a config file could not be read.
type ConfigFileReadError struct {
	Cause error
}

// Error implements the error interface.
func (e *ConfigFileReadError) Error() string {
	return fmt.Sprintf("failed to read config file: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ConfigFileReadError) Unwrap() error {
	return e.Cause
}

// NewConfigFileReadError creates a QRDBError for a config file read failure.
func NewConfigFileReadError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &ConfigFileReadError{Cause: cause},
	}
}

// ConfigUnmarshalError indicates config data could not be unmarshaled.
type ConfigUnmarshalError struct {
	Cause error
}

// Error implements the error interface.
func (e *ConfigUnmarshalError) Error() string {
	return fmt.Sprintf("failed to unmarshal config: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ConfigUnmarshalError) Unwrap() error {
	return e.Cause
}

// NewConfigUnmarshalError creates a QRDBError for a config unmarshal failure.
func NewConfigUnmarshalError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &ConfigUnmarshalError{Cause: cause},
	}
}

// ConfigFileWriteError indicates a config file could not be written.
type ConfigFileWriteError struct {
	Path  string
	Cause error
}

// Error implements the error interface.
func (e *ConfigFileWriteError) Error() string {
	return fmt.Sprintf("failed to write config file %s: %v", e.Path, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ConfigFileWriteError) Unwrap() error {
	return e.Cause
}

// NewConfigFileWriteError creates a QRDBError for a config file write failure.
func NewConfigFileWriteError(op, path string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &ConfigFileWriteError{Path: path, Cause: cause},
	}
}

// ConfigMarshalError indicates config data could not be marshaled.
type ConfigMarshalError struct {
	Cause error
}

// Error implements the error interface.
func (e *ConfigMarshalError) Error() string {
	return fmt.Sprintf("failed to marshal config: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ConfigMarshalError) Unwrap() error {
	return e.Cause
}

// NewConfigMarshalError creates a QRDBError for a config marshal failure.
func NewConfigMarshalError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &ConfigMarshalError{Cause: cause},
	}
}

// InvalidURLError indicates a URL could not be parsed.
type InvalidURLError struct {
	Field string
	Cause error
}

// Error implements the error interface.
func (e *InvalidURLError) Error() string {
	return fmt.Sprintf("invalid %s: %v", e.Field, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *InvalidURLError) Unwrap() error {
	return e.Cause
}

// NewInvalidURLError creates a QRDBError for an invalid URL.
func NewInvalidURLError(op, field string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &InvalidURLError{Field: field, Cause: cause},
	}
}

// RequiredWhenEnabledError indicates a required field when a condition is true.
type RequiredWhenEnabledError struct {
	Field     string
	Condition string
}

// Error implements the error interface.
func (e *RequiredWhenEnabledError) Error() string {
	return fmt.Sprintf("%s cannot be empty when %s is true", e.Field, e.Condition)
}

// NewRequiredWhenEnabledError creates a QRDBError for a missing required field.
func NewRequiredWhenEnabledError(op, field, condition string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrValidation,
		Op:   op,
		Err:  &RequiredWhenEnabledError{Field: field, Condition: condition},
	}
}
