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

// Package api provides shared types for all QRDB API transports.
// This file contains error types used by the service layer.
package api

import (
	"fmt"
	"time"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// =============================================================================
// Service Layer Error Types
// =============================================================================

// ServiceClosedError indicates a service has been closed and cannot accept requests.
type ServiceClosedError struct {
	Service string
}

// Error implements the error interface.
func (e *ServiceClosedError) Error() string {
	return e.Service + " is closed"
}

// NewServiceClosedError creates a QRDBError for a closed service.
func NewServiceClosedError(op, service string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &ServiceClosedError{Service: service},
	}
}

// CrossTenantAccessError indicates an attempt to access a resource belonging to another tenant.
type CrossTenantAccessError struct{}

// Error implements the error interface.
func (e *CrossTenantAccessError) Error() string {
	return "cross-tenant access denied"
}

// NewCrossTenantAccessError creates a QRDBError for a cross-tenant access violation.
func NewCrossTenantAccessError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrForbidden,
		Op:   op,
		Err:  &CrossTenantAccessError{},
	}
}

// ChecksumMismatchError indicates a client-provided checksum does not match the computed value.
type ChecksumMismatchError struct{}

// Error implements the error interface.
func (e *ChecksumMismatchError) Error() string {
	return "client checksum does not match computed checksum"
}

// NewChecksumMismatchError creates a QRDBError for a checksum mismatch.
func NewChecksumMismatchError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ChecksumMismatchError{},
	}
}

// AlreadyArchivedError indicates an object is already in archived state.
type AlreadyArchivedError struct{}

// Error implements the error interface.
func (e *AlreadyArchivedError) Error() string {
	return "object is already archived"
}

// NewAlreadyArchivedError creates a QRDBError for an already-archived object.
func NewAlreadyArchivedError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &AlreadyArchivedError{},
	}
}

// NotArchivedError indicates an object is not in archived state.
type NotArchivedError struct{}

// Error implements the error interface.
func (e *NotArchivedError) Error() string {
	return "object is not archived"
}

// NewNotArchivedError creates a QRDBError for a non-archived object.
func NewNotArchivedError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NotArchivedError{},
	}
}

// AEADLimitExceededError indicates an AEAD safety limit has been exceeded.
type AEADLimitExceededError struct {
	KeyType string
	ID      string
}

// Error implements the error interface.
func (e *AEADLimitExceededError) Error() string {
	return "AEAD safety limit exceeded for " + e.KeyType + " " + e.ID + ": key rotation required"
}

// NewAEADLimitExceededError creates a QRDBError for an AEAD safety limit violation.
func NewAEADLimitExceededError(op, keyType, id string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrResourceExhausted,
		Op:   op,
		Err:  &AEADLimitExceededError{KeyType: keyType, ID: id},
	}
}

// ObjectNotAvailableError indicates an object is not available due to its current status.
type ObjectNotAvailableError struct {
	Status string
}

// Error implements the error interface.
func (e *ObjectNotAvailableError) Error() string {
	return "object not available (status: " + e.Status + ")"
}

// NewObjectNotAvailableError creates a QRDBError for an unavailable object.
func NewObjectNotAvailableError(op, status string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &ObjectNotAvailableError{Status: status},
	}
}

// BackendUnavailableError indicates a storage backend is not available.
type BackendUnavailableError struct {
	Cause error
}

// Error implements the error interface.
func (e *BackendUnavailableError) Error() string {
	return "backend not available: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *BackendUnavailableError) Unwrap() error {
	return e.Cause
}

// NewBackendUnavailableError creates a QRDBError for an unavailable backend.
func NewBackendUnavailableError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &BackendUnavailableError{Cause: cause},
	}
}

// InvalidPathError indicates a path validation failure.
type InvalidPathError struct {
	Message string
}

// Error implements the error interface.
func (e *InvalidPathError) Error() string {
	return e.Message
}

// NewInvalidPathError creates a QRDBError for an invalid path.
func NewInvalidPathError(op, message string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidPathError{Message: message},
	}
}

// NilRequestError indicates a nil request was provided.
type NilRequestError struct{}

// Error implements the error interface.
func (e *NilRequestError) Error() string {
	return "request is nil"
}

// NewNilRequestError creates a QRDBError for a nil request.
func NewNilRequestError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NilRequestError{},
	}
}

// WhitespaceFieldError indicates a field is empty or contains only whitespace.
type WhitespaceFieldError struct {
	Field string
}

// Error implements the error interface.
func (e *WhitespaceFieldError) Error() string {
	return e.Field + " cannot be empty or whitespace"
}

// NewWhitespaceFieldError creates a QRDBError for a whitespace-only field.
func NewWhitespaceFieldError(op, field string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &WhitespaceFieldError{Field: field},
	}
}

// IndexedItemError indicates a validation error for a specific indexed item.
type IndexedItemError struct {
	Index   int
	Message string
}

// Error implements the error interface.
func (e *IndexedItemError) Error() string {
	return fmt.Sprintf("%s at index %d cannot be empty", e.Message, e.Index)
}

// NewIndexedItemError creates a QRDBError for an indexed item validation failure.
func NewIndexedItemError(op, message string, index int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &IndexedItemError{Index: index, Message: message},
	}
}

// OverflowError indicates a value exceeds the maximum representable size.
type OverflowError struct {
	Field string
	Value int
	Max   string
}

// Error implements the error interface.
func (e *OverflowError) Error() string {
	return fmt.Sprintf("%s %d exceeds maximum %s", e.Field, e.Value, e.Max)
}

// NewOverflowError creates a QRDBError for a value overflow.
func NewOverflowError(op, field string, value int, max string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &OverflowError{Field: field, Value: value, Max: max},
	}
}

// MaximumValueError indicates a value exceeds the maximum allowed.
type MaximumValueError struct {
	Field   string
	Maximum string
}

// Error implements the error interface.
func (e *MaximumValueError) Error() string {
	return e.Field + " cannot exceed " + e.Maximum
}

// NewMaximumValueError creates a QRDBError for a value above the maximum.
func NewMaximumValueError(op, field, maximum string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &MaximumValueError{Field: field, Maximum: maximum},
	}
}

// MinDurationValueError indicates a duration value is below the minimum allowed.
type MinDurationValueError struct {
	Field   string
	Minimum time.Duration
}

// Error implements the error interface.
func (e *MinDurationValueError) Error() string {
	return fmt.Sprintf("%s must be at least %v", e.Field, e.Minimum)
}

// NewMinDurationValueError creates a QRDBError for a duration below the minimum.
func NewMinDurationValueError(op, field string, minimum time.Duration) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &MinDurationValueError{Field: field, Minimum: minimum},
	}
}

// MaxDurationValueError indicates a duration value exceeds the maximum allowed.
type MaxDurationValueError struct {
	Field   string
	Maximum time.Duration
}

// Error implements the error interface.
func (e *MaxDurationValueError) Error() string {
	return fmt.Sprintf("%s cannot exceed %v", e.Field, e.Maximum)
}

// NewMaxDurationValueError creates a QRDBError for a duration above the maximum.
func NewMaxDurationValueError(op, field string, maximum time.Duration) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &MaxDurationValueError{Field: field, Maximum: maximum},
	}
}

// TTLExceedsMaxError indicates the requested TTL exceeds the maximum allowed.
type TTLExceedsMaxError struct {
	Requested time.Duration
	Maximum   time.Duration
}

// Error implements the error interface.
func (e *TTLExceedsMaxError) Error() string {
	return fmt.Sprintf("requested TTL exceeds max TTL: %v > %v", e.Requested, e.Maximum)
}

// NewTTLExceedsMaxError creates a QRDBError for a TTL that exceeds the maximum.
func NewTTLExceedsMaxError(op string, requested, maximum time.Duration) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &TTLExceedsMaxError{Requested: requested, Maximum: maximum},
	}
}

// NotConfiguredError indicates a required resource is not configured.
type NotConfiguredError struct {
	Resource string
}

// Error implements the error interface.
func (e *NotConfiguredError) Error() string {
	return "no " + e.Resource + " configured"
}

// NewNotConfiguredError creates a QRDBError for an unconfigured resource.
func NewNotConfiguredError(op, resource string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &NotConfiguredError{Resource: resource},
	}
}

// KeyParseError indicates a failure parsing a storage key.
type KeyParseError struct {
	Message string
	Key     string
}

// Error implements the error interface.
func (e *KeyParseError) Error() string {
	return e.Message + ": " + e.Key
}

// NewKeyParseError creates a QRDBError for a key parsing failure.
func NewKeyParseError(op, message, key string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &KeyParseError{Message: message, Key: key},
	}
}

// ReservedEntityPrefixError indicates an entity type uses a reserved prefix.
type ReservedEntityPrefixError struct {
	Prefix     string
	EntityType string
}

// Error implements the error interface.
func (e *ReservedEntityPrefixError) Error() string {
	return "entity type cannot use reserved prefix '" + e.Prefix + "': " + e.EntityType
}

// NewReservedEntityPrefixError creates a QRDBError for a reserved entity prefix.
func NewReservedEntityPrefixError(op, prefix, entityType string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ReservedEntityPrefixError{Prefix: prefix, EntityType: entityType},
	}
}

// SeparatorPrefixError indicates an entity type starts with a separator character.
type SeparatorPrefixError struct {
	Separator  string
	EntityType string
}

// Error implements the error interface.
func (e *SeparatorPrefixError) Error() string {
	return "entity type cannot start with separator '" + e.Separator + "': " + e.EntityType
}

// NewSeparatorPrefixError creates a QRDBError for an entity type starting with a separator.
func NewSeparatorPrefixError(op, separator, entityType string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &SeparatorPrefixError{Separator: separator, EntityType: entityType},
	}
}

// ContainsObjectsError indicates a backend contains objects and cannot be removed.
type ContainsObjectsError struct {
	Count   int64
	Message string
}

// Error implements the error interface.
func (e *ContainsObjectsError) Error() string {
	return fmt.Sprintf("backend contains %d objects; %s", e.Count, e.Message)
}

// NewContainsObjectsError creates a QRDBError for a backend that contains objects.
func NewContainsObjectsError(op string, count int64, message string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ContainsObjectsError{Count: count, Message: message},
	}
}

// GCOperationNotFoundError indicates a GC operation was not found.
type GCOperationNotFoundError struct {
	OperationID string
}

// Error implements the error interface.
func (e *GCOperationNotFoundError) Error() string {
	return "GC operation not found: " + e.OperationID
}

// NewGCOperationNotFoundError creates a QRDBError for a missing GC operation.
func NewGCOperationNotFoundError(op, operationID string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &GCOperationNotFoundError{OperationID: operationID},
	}
}

// SourceLocationNotFoundError indicates a source location was not found in the catalog.
type SourceLocationNotFoundError struct {
	Location string
}

// Error implements the error interface.
func (e *SourceLocationNotFoundError) Error() string {
	return "source location not found in catalog: " + e.Location
}

// NewSourceLocationNotFoundError creates a QRDBError for a missing source location.
func NewSourceLocationNotFoundError(op, location string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &SourceLocationNotFoundError{Location: location},
	}
}

// DestinationExistsError indicates a destination key already exists.
type DestinationExistsError struct{}

// Error implements the error interface.
func (e *DestinationExistsError) Error() string {
	return "destination key already exists"
}

// NewDestinationExistsError creates a QRDBError for a duplicate destination key.
func NewDestinationExistsError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrAlreadyExists,
		Op:   op,
		Err:  &DestinationExistsError{},
	}
}

// NoTargetSpecifiedError indicates no target was specified for an operation.
type NoTargetSpecifiedError struct {
	Target string
}

// Error implements the error interface.
func (e *NoTargetSpecifiedError) Error() string {
	return "no " + e.Target + " specified"
}

// NewNoTargetSpecifiedError creates a QRDBError for a missing target.
func NewNoTargetSpecifiedError(op, target string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NoTargetSpecifiedError{Target: target},
	}
}

// InvalidEmailFormatError indicates an invalid email format.
type InvalidEmailFormatError struct{}

// Error implements the error interface.
func (e *InvalidEmailFormatError) Error() string {
	return "invalid email format"
}

// NewInvalidEmailFormatError creates a QRDBError for an invalid email format.
func NewInvalidEmailFormatError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidEmailFormatError{},
	}
}

// GeneratorConfigError indicates an invalid generator configuration.
type GeneratorConfigError struct {
	Cause error
}

// Error implements the error interface.
func (e *GeneratorConfigError) Error() string {
	return "invalid generator config: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *GeneratorConfigError) Unwrap() error {
	return e.Cause
}

// NewGeneratorConfigError creates a QRDBError for an invalid generator configuration.
func NewGeneratorConfigError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &GeneratorConfigError{Cause: cause},
	}
}

// EntityTypeEmptyError indicates an entity type is empty.
type EntityTypeEmptyError struct{}

// Error implements the error interface.
func (e *EntityTypeEmptyError) Error() string {
	return "entity type cannot be empty"
}

// NewEntityTypeEmptyError creates a QRDBError for an empty entity type.
func NewEntityTypeEmptyError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EntityTypeEmptyError{},
	}
}

// AlreadyClosedError indicates a component has already been closed.
type AlreadyClosedError struct {
	Component string
}

// Error implements the error interface.
func (e *AlreadyClosedError) Error() string {
	return e.Component + " already closed"
}

// NewAlreadyClosedError creates a QRDBError for an already-closed component.
func NewAlreadyClosedError(op, component string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &AlreadyClosedError{Component: component},
	}
}

// NotFoundWithCauseError indicates a resource was not found with an underlying cause.
type NotFoundWithCauseError struct {
	Resource string
	Cause    error
}

// Error implements the error interface.
func (e *NotFoundWithCauseError) Error() string {
	return e.Resource + " not found: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *NotFoundWithCauseError) Unwrap() error {
	return e.Cause
}

// NewNotFoundWithCauseError creates a QRDBError for a resource not found with a cause.
func NewNotFoundWithCauseError(op, resource string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &NotFoundWithCauseError{Resource: resource, Cause: cause},
	}
}

// NotFoundByNameError indicates a named resource was not found.
type NotFoundByNameError struct {
	Resource string
	Name     string
}

// Error implements the error interface.
func (e *NotFoundByNameError) Error() string {
	return e.Resource + " not found: " + e.Name
}

// NewNotFoundByNameError creates a QRDBError for a named resource not found.
func NewNotFoundByNameError(op, resource, name string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &NotFoundByNameError{Resource: resource, Name: name},
	}
}

// NoAvailableBackendError indicates no available backend for an object.
type NoAvailableBackendError struct {
	Cause error
}

// Error implements the error interface.
func (e *NoAvailableBackendError) Error() string {
	return "no available backend for object: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *NoAvailableBackendError) Unwrap() error {
	return e.Cause
}

// NewNoAvailableBackendError creates a QRDBError for no available backend.
func NewNoAvailableBackendError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &NoAvailableBackendError{Cause: cause},
	}
}

// SentinelError is a simple error type for sentinel (package-level) error values.
type SentinelError struct {
	Message string
}

// Error implements the error interface.
func (e *SentinelError) Error() string {
	return e.Message
}

// GRPCMessageError wraps a gRPC status message as a typed error.
type GRPCMessageError struct {
	Message string
}

// Error implements the error interface.
func (e *GRPCMessageError) Error() string {
	return e.Message
}

// WrappedMessageError provides a typed wrapper for error messages that wrap an underlying error.
type WrappedMessageError struct {
	Message string
	Cause   error
}

// Error implements the error interface.
func (e *WrappedMessageError) Error() string {
	return e.Message + ": " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *WrappedMessageError) Unwrap() error {
	return e.Cause
}

// MessageError provides a typed wrapper for simple error messages.
type MessageError struct {
	Message string
}

// Error implements the error interface.
func (e *MessageError) Error() string {
	return e.Message
}

// =============================================================================
// Additional Service Layer Error Types
// =============================================================================

// MigrationTargetNotFoundError indicates a migration target backend was not found.
type MigrationTargetNotFoundError struct {
	Target string
}

// Error implements the error interface.
func (e *MigrationTargetNotFoundError) Error() string {
	return "migration target not found: " + e.Target
}

// NewMigrationTargetNotFoundError creates a QRDBError for a missing migration target.
func NewMigrationTargetNotFoundError(op, target string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &MigrationTargetNotFoundError{Target: target},
	}
}

// DefaultTTLExceedsMaxError indicates the default TTL exceeds the maximum TTL.
type DefaultTTLExceedsMaxError struct{}

// Error implements the error interface.
func (e *DefaultTTLExceedsMaxError) Error() string {
	return "default TTL cannot exceed max TTL"
}

// NewDefaultTTLExceedsMaxError creates a QRDBError for a default TTL exceeding max TTL.
func NewDefaultTTLExceedsMaxError(op string) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &DefaultTTLExceedsMaxError{},
	}
}

// BackendCountOverflowError indicates the backend count exceeds the maximum representable value.
type BackendCountOverflowError struct {
	Count int
}

// Error implements the error interface.
func (e *BackendCountOverflowError) Error() string {
	return fmt.Sprintf("backend count %d exceeds maximum int32", e.Count)
}

// NewBackendCountOverflowError creates a QRDBError for a backend count overflow.
func NewBackendCountOverflowError(op string, count int) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &BackendCountOverflowError{Count: count},
	}
}

// SourceBackendUnavailableError indicates a source backend is unavailable for a repair operation.
type SourceBackendUnavailableError struct {
	Cause error
}

// Error implements the error interface.
func (e *SourceBackendUnavailableError) Error() string {
	return "source backend unavailable: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *SourceBackendUnavailableError) Unwrap() error {
	return e.Cause
}

// NewSourceBackendUnavailableError creates a QRDBError for an unavailable source backend.
func NewSourceBackendUnavailableError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrStorage,
		Op:   op,
		Err:  &SourceBackendUnavailableError{Cause: cause},
	}
}

// GeneratorNotFoundError indicates a credential generator was not found.
type GeneratorNotFoundError struct {
	Cause error
}

// Error implements the error interface.
func (e *GeneratorNotFoundError) Error() string {
	return "generator not found: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *GeneratorNotFoundError) Unwrap() error {
	return e.Cause
}

// NewGeneratorNotFoundError creates a QRDBError for a missing credential generator.
func NewGeneratorNotFoundError(op string, cause error) *dberrors.QRDBError {
	return &dberrors.QRDBError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &GeneratorNotFoundError{Cause: cause},
	}
}
