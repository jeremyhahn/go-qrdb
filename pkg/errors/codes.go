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

// Package errors provides structured error handling for QRDB.
// It defines error codes, types, and helper functions for consistent error management.
package errors

// ErrorCode represents a specific error condition in QRDB.
type ErrorCode int

const (
	// ErrUnknown represents an unknown or unspecified error.
	ErrUnknown ErrorCode = iota

	// ErrNotFound indicates that the requested resource was not found.
	ErrNotFound

	// ErrConflict indicates a conflict with existing state (e.g., duplicate key).
	ErrConflict

	// ErrTimeout indicates that an operation timed out.
	ErrTimeout

	// ErrInternal represents an internal server error.
	ErrInternal

	// ErrInvalidInput indicates invalid input parameters.
	ErrInvalidInput

	// ErrUnauthorized indicates authentication is required.
	ErrUnauthorized

	// ErrForbidden indicates the operation is not permitted.
	ErrForbidden

	// ErrUnavailable indicates the service is unavailable.
	ErrUnavailable

	// ErrStorage indicates a storage layer error.
	ErrStorage

	// ErrRaft indicates a Raft consensus error.
	ErrRaft

	// ErrEncryption indicates an encryption/decryption error.
	ErrEncryption

	// ErrDecryption indicates a decryption error.
	ErrDecryption

	// ErrValidation indicates a validation error.
	ErrValidation

	// ErrConfiguration indicates a configuration error.
	ErrConfiguration

	// ErrCanceled indicates the operation was canceled.
	ErrCanceled

	// ErrDeadlineExceeded indicates the deadline was exceeded.
	ErrDeadlineExceeded

	// ErrAlreadyExists indicates the resource already exists.
	ErrAlreadyExists

	// ErrResourceExhausted indicates resources are exhausted.
	ErrResourceExhausted

	// ErrPreconditionFailed indicates a precondition check failed.
	ErrPreconditionFailed

	// ErrAborted indicates the operation was aborted.
	ErrAborted

	// ErrOutOfRange indicates a value is out of range.
	ErrOutOfRange

	// ErrUnimplemented indicates the operation is not implemented.
	ErrUnimplemented

	// ErrDataLoss indicates unrecoverable data loss or corruption.
	ErrDataLoss

	// ErrSnapshot indicates a snapshot operation error.
	ErrSnapshot

	// ErrReplication indicates a replication error.
	ErrReplication

	// ErrLeaderNotAvailable indicates no Raft leader is available.
	ErrLeaderNotAvailable

	// ErrClusterNotReady indicates the cluster is not ready.
	ErrClusterNotReady

	// ErrQuorumLost indicates the cluster has lost quorum.
	ErrQuorumLost

	// ErrSplitBrain indicates a split-brain scenario has been detected.
	ErrSplitBrain

	// ErrRateLimited indicates the operation was rate limited.
	ErrRateLimited

	// ErrNotImplemented indicates functionality is not yet implemented.
	ErrNotImplemented

	// ErrNotAllowed indicates the operation is not allowed.
	ErrNotAllowed

	// ErrSystemKeyspaceViolation indicates an attempt to access the reserved system keyspace.
	ErrSystemKeyspaceViolation

	// ErrCrossTenantAccess indicates an attempt to access another tenant's data.
	ErrCrossTenantAccess

	// ErrMissingTenantContext indicates tenant context is required but not present.
	ErrMissingTenantContext

	// ErrMissingJustification indicates a justification is required but not provided.
	ErrMissingJustification

	// ErrMissingUserContext indicates user context is required but not present.
	ErrMissingUserContext

	// ErrCircuitOpen indicates the circuit breaker is open and rejecting requests.
	ErrCircuitOpen
)

// String returns the string representation of the error code.
func (c ErrorCode) String() string {
	switch c {
	case ErrUnknown:
		return "unknown"
	case ErrNotFound:
		return "not_found"
	case ErrConflict:
		return "conflict"
	case ErrTimeout:
		return "timeout"
	case ErrInternal:
		return "internal"
	case ErrInvalidInput:
		return "invalid_input"
	case ErrUnauthorized:
		return "unauthorized"
	case ErrForbidden:
		return "forbidden"
	case ErrUnavailable:
		return "unavailable"
	case ErrStorage:
		return "storage"
	case ErrRaft:
		return "raft"
	case ErrEncryption:
		return "encryption"
	case ErrDecryption:
		return "decryption"
	case ErrValidation:
		return "validation"
	case ErrConfiguration:
		return "configuration"
	case ErrCanceled:
		return "canceled"
	case ErrDeadlineExceeded:
		return "deadline_exceeded"
	case ErrAlreadyExists:
		return "already_exists"
	case ErrResourceExhausted:
		return "resource_exhausted"
	case ErrPreconditionFailed:
		return "precondition_failed"
	case ErrAborted:
		return "aborted"
	case ErrOutOfRange:
		return "out_of_range"
	case ErrUnimplemented:
		return "unimplemented"
	case ErrDataLoss:
		return "data_loss"
	case ErrSnapshot:
		return "snapshot"
	case ErrReplication:
		return "replication"
	case ErrLeaderNotAvailable:
		return "leader_not_available"
	case ErrClusterNotReady:
		return "cluster_not_ready"
	case ErrQuorumLost:
		return "quorum_lost"
	case ErrSplitBrain:
		return "split_brain"
	case ErrRateLimited:
		return "rate_limited"
	case ErrNotImplemented:
		return "not_implemented"
	case ErrNotAllowed:
		return "not_allowed"
	case ErrSystemKeyspaceViolation:
		return "system_keyspace_violation"
	case ErrCrossTenantAccess:
		return "cross_tenant_access"
	case ErrMissingTenantContext:
		return "missing_tenant_context"
	case ErrMissingJustification:
		return "missing_justification"
	case ErrMissingUserContext:
		return "missing_user_context"
	case ErrCircuitOpen:
		return "circuit_open"
	default:
		return "unknown"
	}
}

// HTTPStatus returns the HTTP status code for this error code.
func (c ErrorCode) HTTPStatus() int {
	switch c {
	case ErrNotFound:
		return 404
	case ErrConflict, ErrAlreadyExists:
		return 409
	case ErrTimeout, ErrDeadlineExceeded:
		return 408
	case ErrInvalidInput, ErrValidation, ErrOutOfRange:
		return 400
	case ErrUnauthorized:
		return 401
	case ErrForbidden, ErrNotAllowed, ErrSystemKeyspaceViolation, ErrCrossTenantAccess:
		return 403
	case ErrMissingTenantContext, ErrMissingJustification, ErrMissingUserContext:
		return 400
	case ErrUnavailable, ErrLeaderNotAvailable, ErrClusterNotReady, ErrQuorumLost, ErrStorage, ErrRaft:
		return 503
	case ErrUnimplemented, ErrNotImplemented:
		return 501
	case ErrResourceExhausted, ErrRateLimited:
		return 429
	case ErrPreconditionFailed:
		return 412
	case ErrSplitBrain:
		return 500
	case ErrCircuitOpen:
		return 503
	default:
		return 500
	}
}

// IsRetryable returns true if the error is potentially retryable.
func (c ErrorCode) IsRetryable() bool {
	switch c {
	case ErrTimeout, ErrUnavailable, ErrLeaderNotAvailable, ErrClusterNotReady,
		ErrDeadlineExceeded, ErrResourceExhausted, ErrAborted, ErrRateLimited,
		ErrCircuitOpen:
		return true
	default:
		return false
	}
}
