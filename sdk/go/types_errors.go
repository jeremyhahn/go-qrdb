// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sdk

import (
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// ---------------------------------------------------------------------------
// Error code type (already partially re-exported in dao.go)
// ---------------------------------------------------------------------------

// ErrorCode classifies database errors into categories.
type ErrorCode = dberrors.ErrorCode

// ErrorCode constants.
const (
	ErrCodeNotFound          = dberrors.ErrNotFound
	ErrCodeInternal          = dberrors.ErrInternal
	ErrCodeConflict          = dberrors.ErrConflict
	ErrCodeTimeout           = dberrors.ErrTimeout
	ErrCodeUnavailable       = dberrors.ErrUnavailable
	ErrCodeInvalidInput      = dberrors.ErrInvalidInput
	ErrCodeAlreadyExists     = dberrors.ErrAlreadyExists
	ErrCodeResourceExhausted = dberrors.ErrResourceExhausted
	ErrCodeCanceled          = dberrors.ErrCanceled
	ErrCodeNotImplemented    = dberrors.ErrNotImplemented
	ErrCodeCircuitOpen       = dberrors.ErrCircuitOpen
)

// ---------------------------------------------------------------------------
// DragonError — the structured database error type
// ---------------------------------------------------------------------------

// DragonError is the structured error type with code, operation, and context.
type DragonError = dberrors.DragonError

// QuotaExceededError is returned when a resource quota is exceeded.
type QuotaExceededError = dberrors.QuotaExceededError

// MessageError is a simple error with a message string.
type MessageError = dberrors.MessageError

// WrappedMessageError is a message error that wraps an underlying error.
type WrappedMessageError = dberrors.WrappedMessageError

// ParameterNotFoundError is returned when a required parameter is missing.
type ParameterNotFoundError = dberrors.ParameterNotFoundError

// ---------------------------------------------------------------------------
// Error constructors
// ---------------------------------------------------------------------------

// NewError creates a new DragonError with the given code and operation.
var NewError = dberrors.New

// NewErrorf creates a new DragonError with a formatted message.
var NewErrorf = dberrors.Newf

// WrapError wraps an existing error with a DragonError code and operation.
var WrapError = dberrors.Wrap

// WrapErrorf wraps an existing error with a formatted DragonError.
var WrapErrorf = dberrors.Wrapf

// ---------------------------------------------------------------------------
// Error query helpers
// ---------------------------------------------------------------------------

// IsErrorCode checks if the error matches the given error code.
var IsErrorCode = dberrors.Is

// AsError extracts a DragonError from the error chain if present.
var AsError = dberrors.As

// ErrorCodeOf returns the ErrorCode from the error chain.
var ErrorCodeOf = dberrors.Code

// ErrorOperationOf returns the operation string from the error chain.
var ErrorOperationOf = dberrors.Operation

// IsRetryable reports whether the error is retryable.
var IsRetryableError = dberrors.IsRetryable

// ErrorContext extracts a context value from the error chain.
var ErrorContext = dberrors.GetContext

// ---------------------------------------------------------------------------
// Error category helpers
// ---------------------------------------------------------------------------

// IsErrorNotFound reports whether the error is a not-found error.
var IsErrorNotFound = dberrors.IsNotFound

// IsErrorCanceled reports whether the error is a canceled error.
var IsErrorCanceled = dberrors.IsCanceled

// IsErrorInvalidInput reports whether the error is an invalid-input error.
var IsErrorInvalidInput = dberrors.IsInvalidInput

// IsErrorResourceExhausted reports whether the error is resource-exhausted.
var IsErrorResourceExhausted = dberrors.IsResourceExhausted

// IsErrorSystemKeyspaceViolation reports whether the error is a system keyspace violation.
var IsErrorSystemKeyspaceViolation = dberrors.IsSystemKeyspaceViolation

// IsErrorCrossTenantAccess reports whether the error is a cross-tenant access violation.
var IsErrorCrossTenantAccess = dberrors.IsCrossTenantAccess

// IsErrorMissingTenantContext reports whether the error is missing tenant context.
var IsErrorMissingTenantContext = dberrors.IsMissingTenantContext

// IsErrorQuotaExceeded reports whether the error is a quota-exceeded error.
var IsErrorQuotaExceeded = dberrors.IsQuotaExceeded

// IsErrorMissingJustification reports whether the error is missing justification.
var IsErrorMissingJustification = dberrors.IsMissingJustification

// IsErrorMissingUserContext reports whether the error is missing user context.
var IsErrorMissingUserContext = dberrors.IsMissingUserContext

// IsErrorCircuitOpen reports whether the error is a circuit-open error.
var IsErrorCircuitOpen = dberrors.IsCircuitOpen

// ---------------------------------------------------------------------------
// Convenience error constructors
// ---------------------------------------------------------------------------

// ErrorNotFound creates a not-found error.
var ErrorNotFound = dberrors.NotFound

// ErrorAlreadyExists creates an already-exists error.
var ErrorAlreadyExists = dberrors.AlreadyExists

// ErrorInvalidInput creates an invalid-input error.
var ErrorInvalidInput = dberrors.InvalidInput

// ErrorInvalidInputf creates a formatted invalid-input error.
var ErrorInvalidInputf = dberrors.InvalidInputf

// ErrorTimeout creates a timeout error.
var ErrorTimeout = dberrors.Timeout

// ErrorInternal creates an internal error.
var ErrorInternal = dberrors.Internal

// ErrorInternalf creates a formatted internal error.
var ErrorInternalf = dberrors.Internalf

// ErrorUnavailable creates an unavailable error.
var ErrorUnavailable = dberrors.Unavailable

// ErrorUnavailablef creates a formatted unavailable error.
var ErrorUnavailablef = dberrors.Unavailablef

// ErrorNotImplemented creates a not-implemented error.
var ErrorNotImplemented = dberrors.NotImplemented

// ErrorCircuitOpen creates a circuit-open error.
var ErrorCircuitOpen = dberrors.CircuitOpen

// NewQuotaExceededError creates a quota-exceeded error.
var NewQuotaExceededError = dberrors.NewQuotaExceededError

// NewSystemKeyspaceViolationError creates a system keyspace violation error.
var NewSystemKeyspaceViolationError = dberrors.NewSystemKeyspaceViolationError

// NewCrossTenantAccessError creates a cross-tenant access error.
var NewCrossTenantAccessError = dberrors.NewCrossTenantAccessError

// NewMissingTenantContextError creates a missing tenant context error.
var NewMissingTenantContextError = dberrors.NewMissingTenantContextError

// NewMissingJustificationError creates a missing justification error.
var NewMissingJustificationError = dberrors.NewMissingJustificationError

// NewMissingUserContextError creates a missing user context error.
var NewMissingUserContextError = dberrors.NewMissingUserContextError
