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

// Package api provides shared types for all DragonDB API transports
// (MCP, QUIC, REST, gRPC). Error types defined here ensure consistent
// error handling across every transport layer.
package api

import (
	"fmt"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// RequiredFieldError indicates a required field is missing from a request.
type RequiredFieldError struct {
	Field string
}

// Error implements the error interface.
func (e *RequiredFieldError) Error() string {
	return e.Field + " is required"
}

// NewRequiredFieldError creates a DragonError for a missing required field.
func NewRequiredFieldError(field string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &RequiredFieldError{Field: field},
	}
}

// ServiceUnavailableError indicates a service dependency is not configured or available.
type ServiceUnavailableError struct {
	Service string
}

// Error implements the error interface.
func (e *ServiceUnavailableError) Error() string {
	return e.Service + " service not available"
}

// NewServiceUnavailableError creates a DragonError for an unavailable service.
func NewServiceUnavailableError(service string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   "api.service",
		Err:  &ServiceUnavailableError{Service: service},
	}
}

// NotImplementedError indicates a feature has not been implemented yet.
type NotImplementedError struct {
	Feature string
}

// Error implements the error interface.
func (e *NotImplementedError) Error() string {
	if e.Feature != "" {
		return e.Feature + " not yet implemented"
	}
	return "not yet implemented"
}

// NewNotImplementedError creates a DragonError for an unimplemented feature.
func NewNotImplementedError(feature string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotImplemented,
		Op:   "api",
		Err:  &NotImplementedError{Feature: feature},
	}
}

// UnknownToolError indicates an unknown tool was requested.
type UnknownToolError struct {
	Tool string
}

// Error implements the error interface.
func (e *UnknownToolError) Error() string {
	return "unknown tool: " + e.Tool
}

// NewUnknownToolError creates a DragonError for an unrecognized tool name.
func NewUnknownToolError(tool string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.tool",
		Err:  &UnknownToolError{Tool: tool},
	}
}

// InvalidFormatError indicates input data could not be parsed or decoded.
type InvalidFormatError struct {
	Field   string
	Message string
	Cause   error
}

// Error implements the error interface.
func (e *InvalidFormatError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("invalid %s format: %v", e.Field, e.Cause)
	}
	return fmt.Sprintf("invalid %s format: %s", e.Field, e.Message)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *InvalidFormatError) Unwrap() error {
	return e.Cause
}

// NewInvalidFormatError creates a DragonError for an invalid input format.
func NewInvalidFormatError(field string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &InvalidFormatError{Field: field, Cause: cause},
	}
}

// NewInvalidFormatMessageError creates a DragonError for an invalid format with a message string.
func NewInvalidFormatMessageError(field, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &InvalidFormatError{Field: field, Message: message},
	}
}

// UnexpectedResponseError indicates an unexpected response type from an internal service.
type UnexpectedResponseError struct {
	Service string
	Method  string
}

// Error implements the error interface.
func (e *UnexpectedResponseError) Error() string {
	return fmt.Sprintf("unexpected response type from %s %s", e.Service, e.Method)
}

// NewUnexpectedResponseError creates a DragonError for an unexpected service response.
func NewUnexpectedResponseError(service, method string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   "api.response",
		Err:  &UnexpectedResponseError{Service: service, Method: method},
	}
}

// OperationFailedError indicates a service operation failed with an underlying cause.
type OperationFailedError struct {
	Operation string
	Cause     error
}

// Error implements the error interface.
func (e *OperationFailedError) Error() string {
	return fmt.Sprintf("%s failed: %v", e.Operation, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *OperationFailedError) Unwrap() error {
	return e.Cause
}

// NewOperationFailedError creates a DragonError for a failed operation.
func NewOperationFailedError(operation string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   "api." + operation,
		Err:  &OperationFailedError{Operation: operation, Cause: cause},
	}
}

// InvalidBase64Error indicates a base64-encoded field could not be decoded.
type InvalidBase64Error struct {
	Field string
	Cause error
}

// Error implements the error interface.
func (e *InvalidBase64Error) Error() string {
	return fmt.Sprintf("invalid base64 %s: %v", e.Field, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *InvalidBase64Error) Unwrap() error {
	return e.Cause
}

// NewInvalidBase64Error creates a DragonError for a base64 decoding failure.
func NewInvalidBase64Error(field string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &InvalidBase64Error{Field: field, Cause: cause},
	}
}

// StreamError indicates an error receiving from a streaming RPC.
type StreamError struct {
	Cause error
}

// Error implements the error interface.
func (e *StreamError) Error() string {
	return fmt.Sprintf("stream error: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *StreamError) Unwrap() error {
	return e.Cause
}

// NewStreamError creates a DragonError for a stream receive failure.
func NewStreamError(cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   "api.stream",
		Err:  &StreamError{Cause: cause},
	}
}

// BatchItemError indicates an error with a specific item in a batch operation.
type BatchItemError struct {
	Index   int
	Field   string
	Message string
	Cause   error
}

// Error implements the error interface.
func (e *BatchItemError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("item %d: %s: %v", e.Index, e.Field, e.Cause)
	}
	return fmt.Sprintf("item %d: %s", e.Index, e.Message)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *BatchItemError) Unwrap() error {
	return e.Cause
}

// NewBatchItemError creates a DragonError for a batch item failure.
func NewBatchItemError(index int, field string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.batch",
		Err: &BatchItemError{
			Index: index,
			Field: field,
			Cause: cause,
		},
	}
}

// NewBatchItemMessageError creates a DragonError for a batch item failure with a message.
func NewBatchItemMessageError(index int, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.batch",
		Err: &BatchItemError{
			Index:   index,
			Message: message,
		},
	}
}

// ServiceNotConfiguredError indicates a service dependency is not configured.
type ServiceNotConfiguredError struct {
	Service string
}

// Error implements the error interface.
func (e *ServiceNotConfiguredError) Error() string {
	return e.Service + " service not configured"
}

// NewServiceNotConfiguredError creates a DragonError for a service that is not configured.
func NewServiceNotConfiguredError(service string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   "api.service",
		Err:  &ServiceNotConfiguredError{Service: service},
	}
}

// EmptyCollectionError indicates a required collection (array/map) is empty.
type EmptyCollectionError struct {
	Field string
}

// Error implements the error interface.
func (e *EmptyCollectionError) Error() string {
	return e.Field + " cannot be empty"
}

// NewEmptyCollectionError creates a DragonError for an empty required collection.
func NewEmptyCollectionError(field string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &EmptyCollectionError{Field: field},
	}
}

// RangeError indicates a value is outside its valid range.
type RangeError struct {
	Field string
	Min   int
	Max   int
}

// Error implements the error interface.
func (e *RangeError) Error() string {
	return fmt.Sprintf("%s must be between %d and %d", e.Field, e.Min, e.Max)
}

// NewRangeError creates a DragonError for an out-of-range value.
func NewRangeError(field string, min, max int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &RangeError{Field: field, Min: min, Max: max},
	}
}

// NotFoundError indicates a specific resource was not found.
type NotFoundError struct {
	Resource string
	ID       string
}

// Error implements the error interface.
func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s not found: %s", e.Resource, e.ID)
}

// NewNotFoundError creates a DragonError for a resource not found condition.
func NewNotFoundError(resource, id string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   "api.lookup",
		Err:  &NotFoundError{Resource: resource, ID: id},
	}
}

// NoResponseError indicates no response was received from a streaming operation.
type NoResponseError struct {
	Operation string
}

// Error implements the error interface.
func (e *NoResponseError) Error() string {
	return "no response received from " + e.Operation
}

// NewNoResponseError creates a DragonError for a missing streaming response.
func NewNoResponseError(operation string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   "api." + operation,
		Err:  &NoResponseError{Operation: operation},
	}
}

// MaxLimitError indicates a request exceeds the maximum allowed limit.
type MaxLimitError struct {
	Field string
	Max   int
}

// Error implements the error interface.
func (e *MaxLimitError) Error() string {
	return fmt.Sprintf("maximum %d %s allowed per batch", e.Max, e.Field)
}

// NewMaxLimitError creates a DragonError for exceeding a batch size limit.
func NewMaxLimitError(field string, max int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &MaxLimitError{Field: field, Max: max},
	}
}

// MarshalError indicates JSON marshaling of a result failed.
type MarshalError struct {
	Cause error
}

// Error implements the error interface.
func (e *MarshalError) Error() string {
	return fmt.Sprintf("failed to marshal result: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *MarshalError) Unwrap() error {
	return e.Cause
}

// NewMarshalError creates a DragonError for a JSON marshaling failure.
func NewMarshalError(cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   "api.marshal",
		Err:  &MarshalError{Cause: cause},
	}
}

// InvalidTypeError indicates a field has an unexpected type.
type InvalidTypeError struct {
	Field    string
	Expected string
}

// Error implements the error interface.
func (e *InvalidTypeError) Error() string {
	return fmt.Sprintf("%s must be %s", e.Field, e.Expected)
}

// NewInvalidTypeError creates a DragonError for an invalid field type.
func NewInvalidTypeError(field, expected string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &InvalidTypeError{Field: field, Expected: expected},
	}
}

// =============================================================================
// QUIC Transport Error Types
// =============================================================================

// NilInputError indicates a required input parameter is nil.
type NilInputError struct {
	Name string
}

// Error implements the error interface.
func (e *NilInputError) Error() string {
	return e.Name + " cannot be nil"
}

// NewNilInputError creates a DragonError for a nil input parameter.
func NewNilInputError(op, name string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NilInputError{Name: name},
	}
}

// EncodeError indicates a payload encoding failure.
type EncodeError struct {
	Subject string
	Cause   error
}

// Error implements the error interface.
func (e *EncodeError) Error() string {
	return fmt.Sprintf("failed to encode %s: %v", e.Subject, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *EncodeError) Unwrap() error {
	return e.Cause
}

// NewEncodeError creates a DragonError for an encoding failure.
func NewEncodeError(op, subject string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &EncodeError{Subject: subject, Cause: cause},
	}
}

// DecodeError indicates a payload decoding failure.
type DecodeError struct {
	Subject string
	Cause   error
}

// Error implements the error interface.
func (e *DecodeError) Error() string {
	return fmt.Sprintf("failed to decode %s: %v", e.Subject, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *DecodeError) Unwrap() error {
	return e.Cause
}

// NewDecodeError creates a DragonError for a decoding failure.
func NewDecodeError(op, subject string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &DecodeError{Subject: subject, Cause: cause},
	}
}

// ClientClosedError indicates the client connection has been closed.
type ClientClosedError struct{}

// Error implements the error interface.
func (e *ClientClosedError) Error() string {
	return "client is closed"
}

// NewClientClosedError creates a DragonError for a closed client.
func NewClientClosedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &ClientClosedError{},
	}
}

// ConnectionError indicates a connection-level failure.
type ConnectionError struct {
	Action string
	Target string
	Cause  error
}

// Error implements the error interface.
func (e *ConnectionError) Error() string {
	if e.Target != "" {
		return fmt.Sprintf("failed to %s %s: %v", e.Action, e.Target, e.Cause)
	}
	return fmt.Sprintf("failed to %s: %v", e.Action, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ConnectionError) Unwrap() error {
	return e.Cause
}

// NewConnectionError creates a DragonError for a connection failure.
func NewConnectionError(op, action, target string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &ConnectionError{Action: action, Target: target, Cause: cause},
	}
}

// WriteError indicates a wire-level write failure.
type WriteError struct {
	Field string
	Cause error
}

// Error implements the error interface.
func (e *WriteError) Error() string {
	return fmt.Sprintf("failed to write %s: %v", e.Field, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *WriteError) Unwrap() error {
	return e.Cause
}

// NewWriteError creates a DragonError for a write failure.
func NewWriteError(op, field string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &WriteError{Field: field, Cause: cause},
	}
}

// ReadError indicates a wire-level read failure.
type ReadError struct {
	Field string
	Cause error
}

// Error implements the error interface.
func (e *ReadError) Error() string {
	return fmt.Sprintf("failed to read %s: %v", e.Field, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ReadError) Unwrap() error {
	return e.Cause
}

// NewReadError creates a DragonError for a read failure.
func NewReadError(op, field string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &ReadError{Field: field, Cause: cause},
	}
}

// SizeLimitError indicates a field exceeds its maximum allowed size.
type SizeLimitError struct {
	Field   string
	Size    uint32
	Maximum uint32
}

// Error implements the error interface.
func (e *SizeLimitError) Error() string {
	return fmt.Sprintf("%s size %d exceeds maximum %d", e.Field, e.Size, e.Maximum)
}

// NewSizeLimitError creates a DragonError for a size limit violation.
func NewSizeLimitError(op, field string, size, maximum uint32) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &SizeLimitError{Field: field, Size: size, Maximum: maximum},
	}
}

// ConfigurationError indicates a configuration-related failure.
type ConfigurationError struct {
	Action string
	Cause  error
}

// Error implements the error interface.
func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("failed to %s: %v", e.Action, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ConfigurationError) Unwrap() error {
	return e.Cause
}

// NewConfigurationError creates a DragonError for a configuration failure.
func NewConfigurationError(op, action string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &ConfigurationError{Action: action, Cause: cause},
	}
}

// ComponentUnavailableError indicates a named component (manager, handler, etc.) is not available.
type ComponentUnavailableError struct {
	Component string
}

// Error implements the error interface.
func (e *ComponentUnavailableError) Error() string {
	return e.Component + " not available"
}

// NewComponentUnavailableError creates a DragonError for an unavailable component.
func NewComponentUnavailableError(component string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   "api.service",
		Err:  &ComponentUnavailableError{Component: component},
	}
}

// BatchItemValidationError indicates a validation failure for a specific item in a batch.
type BatchItemValidationError struct {
	Index   int
	Message string
}

// Error implements the error interface.
func (e *BatchItemValidationError) Error() string {
	return fmt.Sprintf("item %d %s", e.Index, e.Message)
}

// NewBatchItemValidationError creates a DragonError for a batch item validation failure.
func NewBatchItemValidationError(index int, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.batch",
		Err:  &BatchItemValidationError{Index: index, Message: message},
	}
}

// InvalidInputError indicates a generic input validation error with a custom message.
type InvalidInputError struct {
	Message string
	Cause   error
}

// Error implements the error interface.
func (e *InvalidInputError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *InvalidInputError) Unwrap() error {
	return e.Cause
}

// NewInvalidInputError creates a DragonError for an invalid input with a custom message.
func NewInvalidInputError(message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &InvalidInputError{Message: message},
	}
}

// NewInvalidInputCauseError creates a DragonError for an invalid input with a cause.
func NewInvalidInputCauseError(message string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   "api.validate",
		Err:  &InvalidInputError{Message: message, Cause: cause},
	}
}

// =============================================================================
// QUIC Dispatch Error Types
// =============================================================================

// DataTooShortError indicates binary data is shorter than expected.
type DataTooShortError struct {
	Size int
}

// Error implements the error interface.
func (e *DataTooShortError) Error() string {
	return fmt.Sprintf("data too short: %d bytes", e.Size)
}

// NewDataTooShortError creates a DragonError for data that is too short to decode.
func NewDataTooShortError(op string, size int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &DataTooShortError{Size: size},
	}
}

// MissingSubOpcodeError indicates a dispatch request is missing the sub-opcode byte.
type MissingSubOpcodeError struct {
	Service string
}

// Error implements the error interface.
func (e *MissingSubOpcodeError) Error() string {
	return e.Service + " request missing sub-opcode"
}

// NewMissingSubOpcodeError creates a DragonError for a missing sub-opcode.
func NewMissingSubOpcodeError(op, service string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &MissingSubOpcodeError{Service: service},
	}
}

// UnknownSubOpcodeError indicates an unrecognized sub-opcode was received.
type UnknownSubOpcodeError struct {
	Service   string
	SubOpcode byte
}

// Error implements the error interface.
func (e *UnknownSubOpcodeError) Error() string {
	return fmt.Sprintf("unknown %s sub-opcode: 0x%02X", e.Service, e.SubOpcode)
}

// NewUnknownSubOpcodeError creates a DragonError for an unknown sub-opcode.
func NewUnknownSubOpcodeError(op, service string, subOpcode byte) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownSubOpcodeError{Service: service, SubOpcode: subOpcode},
	}
}

// InvalidBatchCheckError indicates an invalid check within a batch request.
type InvalidBatchCheckError struct {
	Index int
	Cause error
}

// Error implements the error interface.
func (e *InvalidBatchCheckError) Error() string {
	return fmt.Sprintf("invalid check at index %d: %v", e.Index, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *InvalidBatchCheckError) Unwrap() error {
	return e.Cause
}

// NewInvalidBatchCheckError creates a DragonError for an invalid batch check item.
func NewInvalidBatchCheckError(op string, index int, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidBatchCheckError{Index: index, Cause: cause},
	}
}

// ShardNotFoundError indicates a requested shard was not found on the node.
type ShardNotFoundError struct {
	ShardID uint64
}

// Error implements the error interface.
func (e *ShardNotFoundError) Error() string {
	return fmt.Sprintf("shard %d not found on this node", e.ShardID)
}

// NewShardNotFoundError creates a DragonError for a missing shard.
func NewShardNotFoundError(op string, shardID uint64) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &ShardNotFoundError{ShardID: shardID},
	}
}

// ItemReadError indicates a failure reading a specific item in a batch decode.
type ItemReadError struct {
	Field string
	Index uint32
	Cause error
}

// Error implements the error interface.
func (e *ItemReadError) Error() string {
	return fmt.Sprintf("failed to read %s for item %d: %v", e.Field, e.Index, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *ItemReadError) Unwrap() error {
	return e.Cause
}

// NewItemReadError creates a DragonError for a batch item read failure.
func NewItemReadError(op, field string, index uint32, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ItemReadError{Field: field, Index: index, Cause: cause},
	}
}

// EmptyListError indicates a required list is empty.
type EmptyListError struct {
	Field string
}

// Error implements the error interface.
func (e *EmptyListError) Error() string {
	return e.Field + " is empty"
}

// NewEmptyListError creates a DragonError for an empty required list.
func NewEmptyListError(op, field string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmptyListError{Field: field},
	}
}

// UnknownFederationSubOpError indicates an unknown federation sub-operation.
type UnknownFederationSubOpError struct {
	SubOp byte
}

// Error implements the error interface.
func (e *UnknownFederationSubOpError) Error() string {
	return fmt.Sprintf("unknown federation sub-operation: 0x%02X", e.SubOp)
}

// NewUnknownFederationSubOpError creates a DragonError for an unknown federation sub-operation.
func NewUnknownFederationSubOpError(op string, subOp byte) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownFederationSubOpError{SubOp: subOp},
	}
}

// =============================================================================
// Authentication Error Types
// =============================================================================

// AuthenticationDisabledError indicates authentication is not enabled.
type AuthenticationDisabledError struct{}

// Error implements the error interface.
func (e *AuthenticationDisabledError) Error() string {
	return "authentication is not enabled"
}

// NewAuthenticationDisabledError creates a DragonError for disabled authentication.
func NewAuthenticationDisabledError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &AuthenticationDisabledError{},
	}
}

// PasswordAuthDisabledError indicates password-based authentication is disabled.
type PasswordAuthDisabledError struct{}

// Error implements the error interface.
func (e *PasswordAuthDisabledError) Error() string {
	return "password-based authentication is disabled; use mTLS certificate or FIDO2/WebAuthn authentication"
}

// NewPasswordAuthDisabledError creates a DragonError for disabled password authentication.
func NewPasswordAuthDisabledError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &PasswordAuthDisabledError{},
	}
}

// TokenRequiredError indicates an authentication token is required.
type TokenRequiredError struct{}

// Error implements the error interface.
func (e *TokenRequiredError) Error() string {
	return "authentication token required"
}

// NewTokenRequiredError creates a DragonError for a missing authentication token.
func NewTokenRequiredError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &TokenRequiredError{},
	}
}

// TokenRevokedError indicates a token has been revoked.
type TokenRevokedError struct{}

// Error implements the error interface.
func (e *TokenRevokedError) Error() string {
	return "token has been revoked"
}

// NewTokenRevokedError creates a DragonError for a revoked token.
func NewTokenRevokedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &TokenRevokedError{},
	}
}

// InsufficientPermissionsError indicates insufficient permissions for an operation.
type InsufficientPermissionsError struct{}

// Error implements the error interface.
func (e *InsufficientPermissionsError) Error() string {
	return "insufficient permissions"
}

// NewInsufficientPermissionsError creates a DragonError for insufficient permissions.
func NewInsufficientPermissionsError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrForbidden,
		Op:   op,
		Err:  &InsufficientPermissionsError{},
	}
}

// InvalidCredentialsError indicates invalid authentication credentials.
type InvalidCredentialsError struct{}

// Error implements the error interface.
func (e *InvalidCredentialsError) Error() string {
	return "invalid credentials"
}

// NewInvalidCredentialsError creates a DragonError for invalid credentials.
func NewInvalidCredentialsError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &InvalidCredentialsError{},
	}
}

// CredentialsRequiredError indicates username and password are required.
type CredentialsRequiredError struct{}

// Error implements the error interface.
func (e *CredentialsRequiredError) Error() string {
	return "username and password are required"
}

// NewCredentialsRequiredError creates a DragonError for missing credentials.
func NewCredentialsRequiredError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &CredentialsRequiredError{},
	}
}

// NoPasswordSetError indicates a user has no password configured.
type NoPasswordSetError struct{}

// Error implements the error interface.
func (e *NoPasswordSetError) Error() string {
	return "user has no password set; use IAM authentication"
}

// NewNoPasswordSetError creates a DragonError for a user without a password.
func NewNoPasswordSetError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &NoPasswordSetError{},
	}
}

// InvalidTokenError indicates a JWT token is invalid.
type InvalidTokenError struct {
	Cause error
}

// Error implements the error interface.
func (e *InvalidTokenError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("invalid token: %v", e.Cause)
	}
	return "invalid token"
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *InvalidTokenError) Unwrap() error {
	return e.Cause
}

// NewInvalidTokenError creates a DragonError for an invalid token.
func NewInvalidTokenError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &InvalidTokenError{Cause: cause},
	}
}

// InvalidTokenClaimsError indicates invalid token claims.
type InvalidTokenClaimsError struct{}

// Error implements the error interface.
func (e *InvalidTokenClaimsError) Error() string {
	return "invalid token claims"
}

// NewInvalidTokenClaimsError creates a DragonError for invalid token claims.
func NewInvalidTokenClaimsError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &InvalidTokenClaimsError{},
	}
}

// UnexpectedSigningMethodError indicates an unexpected JWT signing method.
type UnexpectedSigningMethodError struct {
	Algorithm interface{}
}

// Error implements the error interface.
func (e *UnexpectedSigningMethodError) Error() string {
	return fmt.Sprintf("unexpected signing method: %v", e.Algorithm)
}

// NewUnexpectedSigningMethodError creates a DragonError for an unexpected signing method.
func NewUnexpectedSigningMethodError(op string, algorithm interface{}) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &UnexpectedSigningMethodError{Algorithm: algorithm},
	}
}

// InvalidRefreshTokenError indicates an invalid refresh token.
type InvalidRefreshTokenError struct{}

// Error implements the error interface.
func (e *InvalidRefreshTokenError) Error() string {
	return "invalid refresh token"
}

// NewInvalidRefreshTokenError creates a DragonError for an invalid refresh token.
func NewInvalidRefreshTokenError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnauthorized,
		Op:   op,
		Err:  &InvalidRefreshTokenError{},
	}
}

// AlreadyExistsError indicates a resource already exists.
type AlreadyExistsError struct {
	Resource string
	ID       string
}

// Error implements the error interface.
func (e *AlreadyExistsError) Error() string {
	return e.Resource + " already exists: " + e.ID
}

// NewAlreadyExistsError creates a DragonError for a resource that already exists.
func NewAlreadyExistsError(op, resource, id string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrAlreadyExists,
		Op:   op,
		Err:  &AlreadyExistsError{Resource: resource, ID: id},
	}
}

// InvalidUserTypeError indicates an unexpected user type assertion failure.
type InvalidUserTypeError struct{}

// Error implements the error interface.
func (e *InvalidUserTypeError) Error() string {
	return "invalid user type"
}

// NewInvalidUserTypeError creates a DragonError for an invalid user type.
func NewInvalidUserTypeError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &InvalidUserTypeError{},
	}
}

// BuiltInRoleError indicates an attempt to modify or delete a built-in role.
type BuiltInRoleError struct {
	Action   string
	RoleName string
}

// Error implements the error interface.
func (e *BuiltInRoleError) Error() string {
	return fmt.Sprintf("cannot %s built-in role: %s", e.Action, e.RoleName)
}

// NewBuiltInRoleError creates a DragonError for a built-in role modification attempt.
func NewBuiltInRoleError(op, action, roleName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &BuiltInRoleError{Action: action, RoleName: roleName},
	}
}

// UserHasRoleError indicates a user already has the specified role.
type UserHasRoleError struct {
	RoleName string
}

// Error implements the error interface.
func (e *UserHasRoleError) Error() string {
	return "user already has role: " + e.RoleName
}

// NewUserHasRoleError creates a DragonError for a duplicate role assignment.
func NewUserHasRoleError(op, roleName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UserHasRoleError{RoleName: roleName},
	}
}

// UserMissingRoleError indicates a user does not have the specified role.
type UserMissingRoleError struct {
	RoleName string
}

// Error implements the error interface.
func (e *UserMissingRoleError) Error() string {
	return "user does not have role: " + e.RoleName
}

// NewUserMissingRoleError creates a DragonError for a missing role revocation.
func NewUserMissingRoleError(op, roleName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UserMissingRoleError{RoleName: roleName},
	}
}

// RolesNotFoundError indicates one or more roles do not exist.
type RolesNotFoundError struct{}

// Error implements the error interface.
func (e *RolesNotFoundError) Error() string {
	return "one or more roles do not exist"
}

// NewRolesNotFoundError creates a DragonError for missing roles.
func NewRolesNotFoundError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &RolesNotFoundError{},
	}
}

// ReservedKeyPrefixError indicates a key uses a reserved internal prefix.
type ReservedKeyPrefixError struct {
	Prefix string
}

// Error implements the error interface.
func (e *ReservedKeyPrefixError) Error() string {
	return "key uses reserved internal prefix '" + e.Prefix + "'"
}

// NewReservedKeyPrefixError creates a DragonError for a reserved key prefix violation.
func NewReservedKeyPrefixError(op, prefix string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ReservedKeyPrefixError{Prefix: prefix},
	}
}

// DeprecatedOperationError indicates an operation that has been deprecated.
type DeprecatedOperationError struct {
	Message string
}

// Error implements the error interface.
func (e *DeprecatedOperationError) Error() string {
	return e.Message
}

// NewDeprecatedOperationError creates a DragonError for a deprecated operation.
func NewDeprecatedOperationError(op, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnimplemented,
		Op:   op,
		Err:  &DeprecatedOperationError{Message: message},
	}
}

// UnknownManagerTypeError indicates an unknown manager type was provided.
type UnknownManagerTypeError struct {
	TypeName string
}

// Error implements the error interface.
func (e *UnknownManagerTypeError) Error() string {
	return "unknown auth manager type: " + e.TypeName
}

// NewUnknownManagerTypeError creates a DragonError for an unknown manager type.
func NewUnknownManagerTypeError(op, typeName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &UnknownManagerTypeError{TypeName: typeName},
	}
}

// IteratorError indicates an error from a storage iterator.
type IteratorError struct {
	Cause error
}

// Error implements the error interface.
func (e *IteratorError) Error() string {
	return fmt.Sprintf("iterator error: %v", e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *IteratorError) Unwrap() error {
	return e.Cause
}

// NewIteratorError creates a DragonError for an iterator failure.
func NewIteratorError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &IteratorError{Cause: cause},
	}
}

// =============================================================================
// Unix Socket Error Types
// =============================================================================

// UnknownOpcodeError indicates an unrecognized opcode was received.
type UnknownOpcodeError struct {
	Opcode byte
}

// Error implements the error interface.
func (e *UnknownOpcodeError) Error() string {
	return fmt.Sprintf("unknown opcode: 0x%02X", e.Opcode)
}

// NewUnknownOpcodeError creates a DragonError for an unknown opcode.
func NewUnknownOpcodeError(op string, opcode byte) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownOpcodeError{Opcode: opcode},
	}
}

// HandlerClosedError indicates the handler has been closed and cannot accept requests.
type HandlerClosedError struct{}

// Error implements the error interface.
func (e *HandlerClosedError) Error() string {
	return "handler is closed"
}

// NewHandlerClosedError creates a DragonError for a closed handler.
func NewHandlerClosedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &HandlerClosedError{},
	}
}

// ProtocolStatusError indicates a protocol-level error received from a status code.
type ProtocolStatusError struct {
	Message string
}

// Error implements the error interface.
func (e *ProtocolStatusError) Error() string {
	return e.Message
}

// NewProtocolStatusError creates a DragonError for a protocol status error with a given error code.
func NewProtocolStatusError(op string, code dberrors.ErrorCode, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: code,
		Op:   op,
		Err:  &ProtocolStatusError{Message: message},
	}
}

// FeatureUnsupportedError indicates a service does not support a requested feature.
type FeatureUnsupportedError struct {
	Feature string
}

// Error implements the error interface.
func (e *FeatureUnsupportedError) Error() string {
	return e.Feature + " is not supported"
}

// NewFeatureUnsupportedError creates a DragonError for an unsupported feature.
func NewFeatureUnsupportedError(op, feature string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &FeatureUnsupportedError{Feature: feature},
	}
}

// =============================================================================
// Codec Error Types
// =============================================================================

// NilEntityError indicates a nil entity was provided to an encode or decode operation.
type NilEntityError struct {
	Operation string
}

// Error implements the error interface.
func (e *NilEntityError) Error() string {
	return "cannot " + e.Operation + " nil entity"
}

// NewNilEntityError creates a DragonError for a nil entity in a codec operation.
func NewNilEntityError(op, operation string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NilEntityError{Operation: operation},
	}
}

// EmptyDataError indicates empty data was provided to a decode operation.
type EmptyDataError struct{}

// Error implements the error interface.
func (e *EmptyDataError) Error() string {
	return "cannot decode empty data"
}

// NewEmptyDataError creates a DragonError for empty data in a decode operation.
func NewEmptyDataError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmptyDataError{},
	}
}

// CodecNotFoundError indicates a codec was not found in the registry.
type CodecNotFoundError struct {
	Codec string
}

// Error implements the error interface.
func (e *CodecNotFoundError) Error() string {
	return "codec " + e.Codec + " not found in registry"
}

// NewCodecNotFoundError creates a DragonError for a missing codec in the registry.
func NewCodecNotFoundError(op, codec string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &CodecNotFoundError{Codec: codec},
	}
}

// NoSuitableCodecError indicates no codec could be found for the given entity type.
type NoSuitableCodecError struct {
	EntityType string
}

// Error implements the error interface.
func (e *NoSuitableCodecError) Error() string {
	return "no suitable codec found for entity type " + e.EntityType
}

// NewNoSuitableCodecError creates a DragonError for a missing suitable codec.
func NewNoSuitableCodecError(op, entityType string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &NoSuitableCodecError{EntityType: entityType},
	}
}

// NonPointerError indicates a non-pointer value was provided where a pointer was required.
type NonPointerError struct {
	TypeName string
}

// Error implements the error interface.
func (e *NonPointerError) Error() string {
	return "entity must be a pointer, got " + e.TypeName
}

// NewNonPointerError creates a DragonError for a non-pointer entity in a decode operation.
func NewNonPointerError(op, typeName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NonPointerError{TypeName: typeName},
	}
}

// =============================================================================
// Server Error Types
// =============================================================================

// UnsupportedOpcodeError indicates an unsupported binary protocol opcode.
type UnsupportedOpcodeError struct {
	Opcode byte
}

// Error implements the error interface.
func (e *UnsupportedOpcodeError) Error() string {
	return fmt.Sprintf("unsupported operation: 0x%02x", e.Opcode)
}

// NewUnsupportedOpcodeError creates a DragonError for an unsupported opcode.
func NewUnsupportedOpcodeError(op string, opcode byte) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedOpcodeError{Opcode: opcode},
	}
}

// InvalidDataError indicates binary protocol data is malformed or incomplete.
type InvalidDataError struct {
	Context string
}

// Error implements the error interface.
func (e *InvalidDataError) Error() string {
	return "invalid data: " + e.Context
}

// NewInvalidDataError creates a DragonError for invalid binary data.
func NewInvalidDataError(op, context string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &InvalidDataError{Context: context},
	}
}

// UnsupportedQueryTypeError indicates an unsupported query type was provided.
type UnsupportedQueryTypeError struct {
	TypeName string
}

// Error implements the error interface.
func (e *UnsupportedQueryTypeError) Error() string {
	return "unsupported query type: " + e.TypeName
}

// NewUnsupportedQueryTypeError creates a DragonError for an unsupported query type.
func NewUnsupportedQueryTypeError(op, typeName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedQueryTypeError{TypeName: typeName},
	}
}

// UnexpectedResultTypeError indicates a result had an unexpected type.
type UnexpectedResultTypeError struct {
	TypeName string
}

// Error implements the error interface.
func (e *UnexpectedResultTypeError) Error() string {
	return "unexpected result type: " + e.TypeName
}

// NewUnexpectedResultTypeError creates a DragonError for an unexpected result type.
func NewUnexpectedResultTypeError(op, typeName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &UnexpectedResultTypeError{TypeName: typeName},
	}
}

// UnsupportedModeOperationError indicates an operation is not supported in the current mode.
type UnsupportedModeOperationError struct {
	Operation string
	Mode      string
}

// Error implements the error interface.
func (e *UnsupportedModeOperationError) Error() string {
	return e.Operation + " not supported in " + e.Mode
}

// NewUnsupportedModeOperationError creates a DragonError for an operation unsupported in the current mode.
func NewUnsupportedModeOperationError(op, operation, mode string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnsupportedModeOperationError{Operation: operation, Mode: mode},
	}
}

// ShardNotFoundInModeError indicates a shard was not found in the current operating mode.
type ShardNotFoundInModeError struct {
	ShardID uint64
	Mode    string
}

// Error implements the error interface.
func (e *ShardNotFoundInModeError) Error() string {
	return fmt.Sprintf("shard %d not found in %s", e.ShardID, e.Mode)
}

// NewShardNotFoundInModeError creates a DragonError for a shard not found in the current mode.
func NewShardNotFoundInModeError(op string, shardID uint64, mode string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &ShardNotFoundInModeError{ShardID: shardID, Mode: mode},
	}
}

// UnknownServerModeError indicates an unrecognized server mode was specified.
type UnknownServerModeError struct {
	Mode string
}

// Error implements the error interface.
func (e *UnknownServerModeError) Error() string {
	return fmt.Sprintf("unknown server mode %q, valid modes are: standalone, cluster, devcluster", e.Mode)
}

// NewUnknownServerModeError creates a DragonError for an unknown server mode.
func NewUnknownServerModeError(op, mode string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &UnknownServerModeError{Mode: mode},
	}
}

// ServerStateError indicates an invalid server state for the requested operation.
type ServerStateError struct {
	State string
}

// Error implements the error interface.
func (e *ServerStateError) Error() string {
	return e.State
}

// NewServerStateError creates a DragonError for an invalid server state.
func NewServerStateError(op, state string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &ServerStateError{State: state},
	}
}

// NoListenersConfiguredError indicates no network listeners were configured.
type NoListenersConfiguredError struct {
	Message string
}

// Error implements the error interface.
func (e *NoListenersConfiguredError) Error() string {
	return e.Message
}

// NewNoListenersConfiguredError creates a DragonError for no configured listeners.
func NewNoListenersConfiguredError(op, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &NoListenersConfiguredError{Message: message},
	}
}

// ShutdownError indicates the server shutdown completed with errors.
type ShutdownError struct {
	Count  int
	Errors []error
}

// Error implements the error interface.
func (e *ShutdownError) Error() string {
	return fmt.Sprintf("shutdown completed with %d errors: %v", e.Count, e.Errors)
}

// NewShutdownError creates a DragonError for a shutdown with errors.
func NewShutdownError(op string, errs []error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &ShutdownError{Count: len(errs), Errors: errs},
	}
}

// UnknownProviderError indicates an unrecognized provider was specified.
type UnknownProviderError struct {
	Provider string
}

// Error implements the error interface.
func (e *UnknownProviderError) Error() string {
	return "unknown provider: " + e.Provider
}

// NewUnknownProviderError creates a DragonError for an unknown provider.
func NewUnknownProviderError(op, provider string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &UnknownProviderError{Provider: provider},
	}
}

// EntityShardPinError indicates an entity type is pinned to a non-existent shard.
type EntityShardPinError struct {
	EntityType string
	ShardID    uint64
}

// Error implements the error interface.
func (e *EntityShardPinError) Error() string {
	return fmt.Sprintf("entity %q pinned to unknown shard %d", e.EntityType, e.ShardID)
}

// NewEntityShardPinError creates a DragonError for an invalid entity shard pin.
func NewEntityShardPinError(op, entityType string, shardID uint64) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &EntityShardPinError{EntityType: entityType, ShardID: shardID},
	}
}

// EncryptionKeySizeError indicates an encryption key has the wrong size.
type EncryptionKeySizeError struct {
	Expected int
	Actual   int
}

// Error implements the error interface.
func (e *EncryptionKeySizeError) Error() string {
	return fmt.Sprintf("encryption key must be %d bytes, got %d bytes", e.Expected, e.Actual)
}

// NewEncryptionKeySizeError creates a DragonError for an invalid encryption key size.
func NewEncryptionKeySizeError(op string, expected, actual int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrConfiguration,
		Op:   op,
		Err:  &EncryptionKeySizeError{Expected: expected, Actual: actual},
	}
}

// CertGenerationError indicates a failure during certificate generation.
type CertGenerationError struct {
	Step  string
	Cause error
}

// Error implements the error interface.
func (e *CertGenerationError) Error() string {
	return fmt.Sprintf("failed to %s: %v", e.Step, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *CertGenerationError) Unwrap() error {
	return e.Cause
}

// NewCertGenerationError creates a DragonError for a certificate generation failure.
func NewCertGenerationError(op, step string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &CertGenerationError{Step: step, Cause: cause},
	}
}

// DataDirOperationError indicates a failure during a data directory operation.
type DataDirOperationError struct {
	Action string
	Path   string
	Cause  error
}

// Error implements the error interface.
func (e *DataDirOperationError) Error() string {
	return fmt.Sprintf("failed to %s %s: %v", e.Action, e.Path, e.Cause)
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *DataDirOperationError) Unwrap() error {
	return e.Cause
}

// NewDataDirOperationError creates a DragonError for a data directory operation failure.
func NewDataDirOperationError(op, action, path string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &DataDirOperationError{Action: action, Path: path, Cause: cause},
	}
}

// =============================================================================
// State Machine Error Types
// =============================================================================

// StateMachineClosedError indicates the state machine has been closed.
type StateMachineClosedError struct{}

// Error implements the error interface.
func (e *StateMachineClosedError) Error() string {
	return "state machine is closed"
}

// NewStateMachineClosedError creates a DragonError for a closed state machine.
func NewStateMachineClosedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &StateMachineClosedError{},
	}
}

// NilFieldError indicates a required field is nil.
type NilFieldError struct {
	Field string
}

// Error implements the error interface.
func (e *NilFieldError) Error() string {
	return e.Field + " must not be nil"
}

// NewNilFieldError creates a DragonError for a nil required field.
func NewNilFieldError(op, field string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NilFieldError{Field: field},
	}
}

// EmptyFieldError indicates a required field is empty.
type EmptyFieldError struct {
	Field string
}

// Error implements the error interface.
func (e *EmptyFieldError) Error() string {
	return e.Field + " must not be empty"
}

// NewEmptyFieldError creates a DragonError for an empty required field.
func NewEmptyFieldError(op, field string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &EmptyFieldError{Field: field},
	}
}

// KeyLengthError indicates a key has an unexpected length.
type KeyLengthError struct {
	Expected int
	Actual   int
}

// Error implements the error interface.
func (e *KeyLengthError) Error() string {
	return fmt.Sprintf("expected key length %d, got %d", e.Expected, e.Actual)
}

// NewKeyLengthError creates a DragonError for a key length mismatch.
func NewKeyLengthError(op string, expected, actual int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &KeyLengthError{Expected: expected, Actual: actual},
	}
}

// KeyTooShortError indicates a key is too short for the expected prefix.
type KeyTooShortError struct {
	Component string
}

// Error implements the error interface.
func (e *KeyTooShortError) Error() string {
	return "key too short for " + e.Component
}

// NewKeyTooShortError creates a DragonError for a key that is too short.
func NewKeyTooShortError(op, component string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &KeyTooShortError{Component: component},
	}
}

// FeatureDisabledError indicates a feature is not enabled or available.
type FeatureDisabledError struct {
	Feature string
}

// Error implements the error interface.
func (e *FeatureDisabledError) Error() string {
	return e.Feature + " not enabled"
}

// NewFeatureDisabledError creates a DragonError for a disabled feature.
func NewFeatureDisabledError(op, feature string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrPreconditionFailed,
		Op:   op,
		Err:  &FeatureDisabledError{Feature: feature},
	}
}

// ZeroValueError indicates a required numeric field has a zero value.
type ZeroValueError struct {
	Field string
}

// Error implements the error interface.
func (e *ZeroValueError) Error() string {
	return e.Field + " must not be zero"
}

// NewZeroValueError creates a DragonError for a zero-valued required field.
func NewZeroValueError(op, field string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &ZeroValueError{Field: field},
	}
}

// =============================================================================
// HTTP and Remote Error Types
// =============================================================================

// HTTPStatusError indicates an HTTP request returned a non-success status code.
type HTTPStatusError struct {
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

// NewHTTPStatusError creates a DragonError for an HTTP response with a non-success status code.
func NewHTTPStatusError(op string, code dberrors.ErrorCode, statusCode int, body string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: code,
		Op:   op,
		Err:  &HTTPStatusError{StatusCode: statusCode, Body: body},
	}
}

// RemoteError indicates a remote service returned an error message.
type RemoteError struct {
	Message string
}

// Error implements the error interface.
func (e *RemoteError) Error() string {
	return e.Message
}

// NewRemoteError creates a DragonError for a remote service error message.
func NewRemoteError(op string, code dberrors.ErrorCode, message string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: code,
		Op:   op,
		Err:  &RemoteError{Message: message},
	}
}

// MinimumValueError indicates a field value is below the required minimum.
type MinimumValueError struct {
	Field   string
	Minimum string
}

// Error implements the error interface.
func (e *MinimumValueError) Error() string {
	return e.Field + " must be at least " + e.Minimum
}

// NewMinimumValueError creates a DragonError for a value below minimum.
func NewMinimumValueError(op, field, minimum string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &MinimumValueError{Field: field, Minimum: minimum},
	}
}
