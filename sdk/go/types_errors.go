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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Error code type (already partially re-exported in dao.go)
// ---------------------------------------------------------------------------

// ErrorCode classifies database errors into categories.
type ErrorCode = dberrors.ErrorCode

// ErrorCode constants — canonical Err* names matching pkg/errors.
// Note: ErrNotFound, ErrInternal, ErrConflict, ErrTimeout, and ErrUnavailable
// are already declared in dao.go and are not repeated here.
const (
	// ErrUnknown represents an unknown or unspecified error.
	ErrUnknown = dberrors.ErrUnknown

	// ErrInvalidInput indicates invalid input parameters.
	ErrInvalidInput = dberrors.ErrInvalidInput

	// ErrUnauthorized indicates authentication is required.
	ErrUnauthorized = dberrors.ErrUnauthorized

	// ErrForbidden indicates the operation is not permitted.
	ErrForbidden = dberrors.ErrForbidden

	// ErrStorage indicates a storage layer error.
	ErrStorage = dberrors.ErrStorage

	// ErrRaft indicates a Raft consensus error.
	ErrRaft = dberrors.ErrRaft

	// ErrEncryption indicates an encryption/decryption error.
	ErrEncryption = dberrors.ErrEncryption

	// ErrDecryption indicates a decryption error.
	ErrDecryption = dberrors.ErrDecryption

	// ErrValidation indicates a validation error.
	ErrValidation = dberrors.ErrValidation

	// ErrConfiguration indicates a configuration error.
	ErrConfiguration = dberrors.ErrConfiguration

	// ErrCanceled indicates the operation was canceled.
	ErrCanceled = dberrors.ErrCanceled

	// ErrDeadlineExceeded indicates the deadline was exceeded.
	ErrDeadlineExceeded = dberrors.ErrDeadlineExceeded

	// ErrAlreadyExists indicates the resource already exists.
	ErrAlreadyExists = dberrors.ErrAlreadyExists

	// ErrResourceExhausted indicates resources are exhausted.
	ErrResourceExhausted = dberrors.ErrResourceExhausted

	// ErrPreconditionFailed indicates a precondition check failed.
	ErrPreconditionFailed = dberrors.ErrPreconditionFailed

	// ErrAborted indicates the operation was aborted.
	ErrAborted = dberrors.ErrAborted

	// ErrOutOfRange indicates a value is out of range.
	ErrOutOfRange = dberrors.ErrOutOfRange

	// ErrUnimplemented indicates the operation is not implemented.
	ErrUnimplemented = dberrors.ErrUnimplemented

	// ErrDataLoss indicates unrecoverable data loss or corruption.
	ErrDataLoss = dberrors.ErrDataLoss

	// ErrSnapshot indicates a snapshot operation error.
	ErrSnapshot = dberrors.ErrSnapshot

	// ErrReplication indicates a replication error.
	ErrReplication = dberrors.ErrReplication

	// ErrLeaderNotAvailable indicates no Raft leader is available.
	ErrLeaderNotAvailable = dberrors.ErrLeaderNotAvailable

	// ErrClusterNotReady indicates the cluster is not ready.
	ErrClusterNotReady = dberrors.ErrClusterNotReady

	// ErrQuorumLost indicates the cluster has lost quorum.
	ErrQuorumLost = dberrors.ErrQuorumLost

	// ErrSplitBrain indicates a split-brain scenario has been detected.
	ErrSplitBrain = dberrors.ErrSplitBrain

	// ErrRateLimited indicates the operation was rate limited.
	ErrRateLimited = dberrors.ErrRateLimited

	// ErrNotImplemented indicates functionality is not yet implemented.
	ErrNotImplemented = dberrors.ErrNotImplemented

	// ErrNotAllowed indicates the operation is not allowed.
	ErrNotAllowed = dberrors.ErrNotAllowed

	// ErrSystemKeyspaceViolation indicates an attempt to access the reserved system keyspace.
	ErrSystemKeyspaceViolation = dberrors.ErrSystemKeyspaceViolation

	// ErrCrossTenantAccess indicates an attempt to access another tenant's data.
	ErrCrossTenantAccess = dberrors.ErrCrossTenantAccess

	// ErrMissingTenantContext indicates tenant context is required but not present.
	ErrMissingTenantContext = dberrors.ErrMissingTenantContext

	// ErrMissingJustification indicates a justification is required but not provided.
	ErrMissingJustification = dberrors.ErrMissingJustification

	// ErrMissingUserContext indicates user context is required but not present.
	ErrMissingUserContext = dberrors.ErrMissingUserContext

	// ErrCircuitOpen indicates the circuit breaker is open and rejecting requests.
	ErrCircuitOpen = dberrors.ErrCircuitOpen
)

// ---------------------------------------------------------------------------
// QRDBError — the structured database error type
// ---------------------------------------------------------------------------

// QRDBError is the structured error type with code, operation, and context.
type QRDBError = dberrors.QRDBError

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

// NewError creates a new QRDBError with the given code and operation.
var NewError = dberrors.New

// NewErrorf creates a new QRDBError with a formatted message.
var NewErrorf = dberrors.Newf

// WrapError wraps an existing error with a QRDBError code and operation.
var WrapError = dberrors.Wrap

// WrapErrorf wraps an existing error with a formatted QRDBError.
var WrapErrorf = dberrors.Wrapf

// ---------------------------------------------------------------------------
// Error query helpers
// ---------------------------------------------------------------------------

// IsErrorCode checks if the error matches the given error code.
var IsErrorCode = dberrors.Is

// AsError extracts a QRDBError from the error chain if present.
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

// ---------------------------------------------------------------------------
// pkg/errors-compatible short aliases
// These allow code previously importing go-qrdb/pkg/errors to switch to
// go-qrdb/sdk/go without renaming every call site.
// ---------------------------------------------------------------------------

// New creates a new QRDBError with the given code, operation, and message.
var New = dberrors.New

// Newf creates a new QRDBError with a formatted message.
var Newf = dberrors.Newf

// Wrap wraps an existing error with a QRDBError code and operation.
var Wrap = dberrors.Wrap

// Wrapf wraps an error with additional formatted context.
var Wrapf = dberrors.Wrapf

// Is checks if an error matches a specific error code.
var Is = dberrors.Is

// As is a helper for errors.As with QRDBError.
var As = dberrors.As

// Code extracts the error code from an error, or returns ErrUnknown.
var Code = dberrors.Code

// Operation extracts the operation from an error, or returns empty string.
var Operation = dberrors.Operation

// IsRetryable reports whether the error is retryable.
var IsRetryable = dberrors.IsRetryable

// GetContext retrieves a context value from the error chain.
var GetContext = dberrors.GetContext

// NotFound creates a not-found error.
var NotFound = dberrors.NotFound

// AlreadyExists creates an already-exists error.
var AlreadyExists = dberrors.AlreadyExists

// InvalidInput creates an invalid-input error.
var InvalidInput = dberrors.InvalidInput

// InvalidInputf creates a formatted invalid-input error.
var InvalidInputf = dberrors.InvalidInputf

// Timeout creates a timeout error.
var Timeout = dberrors.Timeout

// Internal creates an internal error.
var Internal = dberrors.Internal

// Internalf creates a formatted internal error.
var Internalf = dberrors.Internalf

// Unavailable creates an unavailable error.
var Unavailable = dberrors.Unavailable

// Unavailablef creates a formatted unavailable error.
var Unavailablef = dberrors.Unavailablef

// NotImplemented creates a not-implemented error.
var NotImplemented = dberrors.NotImplemented

// CircuitOpen creates a circuit-open error.
var CircuitOpen = dberrors.CircuitOpen

// IsNotFound returns true if the error is a not-found error.
var IsNotFound = dberrors.IsNotFound

// IsCanceled returns true if the error is a canceled error.
var IsCanceled = dberrors.IsCanceled

// IsInvalidInput returns true if the error is an invalid-input error.
var IsInvalidInput = dberrors.IsInvalidInput

// IsResourceExhausted returns true if the error is a resource-exhausted error.
var IsResourceExhausted = dberrors.IsResourceExhausted

// IsSystemKeyspaceViolation returns true if the error is a system keyspace violation.
var IsSystemKeyspaceViolation = dberrors.IsSystemKeyspaceViolation

// IsCrossTenantAccess returns true if the error is a cross-tenant access violation.
var IsCrossTenantAccess = dberrors.IsCrossTenantAccess

// IsMissingTenantContext returns true if the error is a missing tenant context error.
var IsMissingTenantContext = dberrors.IsMissingTenantContext

// IsQuotaExceeded returns true if the error is a quota-exceeded error.
var IsQuotaExceeded = dberrors.IsQuotaExceeded

// IsMissingJustification returns true if the error is a missing justification error.
var IsMissingJustification = dberrors.IsMissingJustification

// IsMissingUserContext returns true if the error is a missing user context error.
var IsMissingUserContext = dberrors.IsMissingUserContext

// IsCircuitOpen returns true if the error is a circuit-open error.
var IsCircuitOpen = dberrors.IsCircuitOpen

// NotLeaderError is the service layer's not-leader condition, re-exported
// so embedding hosts can classify it with errors.As (e.g. to retry
// bootstrap work while a cluster's initial election settles) without
// importing pkg/service.
type NotLeaderError = service.NotLeaderError
