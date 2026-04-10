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

package errors

import (
	"errors"
	"fmt"
)

// DragonError represents a structured error in DragonDB with context and metadata.
type DragonError struct {
	// Code is the error code identifying the type of error
	Code ErrorCode

	// Op is the operation that failed (e.g., "storage.Get", "raft.Propose")
	Op string

	// Err is the underlying error
	Err error

	// Context contains additional key-value context about the error
	Context map[string]interface{}

	// Retry indicates whether the operation can be retried
	Retry bool
}

// Error implements the error interface.
func (e *DragonError) Error() string {
	if e.Err != nil {
		if e.Op != "" {
			return fmt.Sprintf("%s: %s: %v", e.Op, e.Code.String(), e.Err)
		}
		return fmt.Sprintf("%s: %v", e.Code.String(), e.Err)
	}

	if e.Op != "" {
		return fmt.Sprintf("%s: %s", e.Op, e.Code.String())
	}
	return e.Code.String()
}

// Unwrap returns the underlying error for errors.Is and errors.As support.
func (e *DragonError) Unwrap() error {
	return e.Err
}

// Is implements error comparison for errors.Is.
func (e *DragonError) Is(target error) bool {
	if target == nil {
		return false
	}

	// Check if target is a DragonError with the same code
	var de *DragonError
	if errors.As(target, &de) {
		return e.Code == de.Code
	}

	// Check underlying error
	if e.Err != nil {
		return errors.Is(e.Err, target)
	}

	return false
}

// WithContext adds context key-value pairs to the error.
func (e *DragonError) WithContext(key string, value interface{}) *DragonError {
	if e.Context == nil {
		e.Context = make(map[string]interface{})
	}
	e.Context[key] = value
	return e
}

// WithRetry sets the retry flag on the error.
func (e *DragonError) WithRetry(retry bool) *DragonError {
	e.Retry = retry
	return e
}

// IsRetryable returns true if the error can be retried.
func (e *DragonError) IsRetryable() bool {
	if e.Retry {
		return true
	}
	return e.Code.IsRetryable()
}

// GetContext retrieves a context value by key.
func (e *DragonError) GetContext(key string) (interface{}, bool) {
	if e.Context == nil {
		return nil, false
	}
	val, ok := e.Context[key]
	return val, ok
}

// IsNotFound returns true if the error is a NotFound error.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrNotFound
	}
	return false
}

// IsCanceled returns true if the error is a Canceled error.
func IsCanceled(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrCanceled
	}
	return false
}

// IsInvalidInput returns true if the error is an InvalidInput error.
func IsInvalidInput(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrInvalidInput
	}
	return false
}

// IsResourceExhausted returns true if the error is a ResourceExhausted error.
func IsResourceExhausted(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrResourceExhausted
	}
	return false
}

// QuotaExceededError represents a quota limit violation.
type QuotaExceededError struct {
	// Resource is the type of resource that exceeded quota (e.g., "vectors", "storage", "batch_size")
	Resource string

	// Namespace is the affected namespace
	Namespace string

	// TenantID is the affected tenant
	TenantID string

	// Limit is the configured limit
	Limit int64

	// Current is the current usage
	Current int64

	// Requested is the requested amount
	Requested int64
}

// Error implements the error interface.
func (e *QuotaExceededError) Error() string {
	if e.TenantID != "" {
		return fmt.Sprintf("quota exceeded for tenant %s namespace %s: %s limit %d, current %d, requested %d",
			e.TenantID, e.Namespace, e.Resource, e.Limit, e.Current, e.Requested)
	}
	return fmt.Sprintf("quota exceeded for namespace %s: %s limit %d, current %d, requested %d",
		e.Namespace, e.Resource, e.Limit, e.Current, e.Requested)
}

// NewQuotaExceededError creates a new QuotaExceededError.
func NewQuotaExceededError(resource, namespace, tenantID string, limit, current, requested int64) *DragonError {
	return &DragonError{
		Code: ErrResourceExhausted,
		Op:   "quota.check",
		Err: &QuotaExceededError{
			Resource:  resource,
			Namespace: namespace,
			TenantID:  tenantID,
			Limit:     limit,
			Current:   current,
			Requested: requested,
		},
		Context: map[string]interface{}{
			"resource":  resource,
			"namespace": namespace,
			"tenant_id": tenantID,
			"limit":     limit,
			"current":   current,
			"requested": requested,
		},
	}
}

// IsQuotaExceeded returns true if the error is a quota exceeded error.
func IsQuotaExceeded(err error) bool {
	if err == nil {
		return false
	}
	var qe *QuotaExceededError
	if errors.As(err, &qe) {
		return true
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrResourceExhausted
	}
	return false
}

// IsSystemKeyspaceViolation returns true if the error is a system keyspace violation.
func IsSystemKeyspaceViolation(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrSystemKeyspaceViolation
	}
	return false
}

// IsCrossTenantAccess returns true if the error is a cross-tenant access violation.
func IsCrossTenantAccess(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrCrossTenantAccess
	}
	return false
}

// IsMissingTenantContext returns true if the error is a missing tenant context error.
func IsMissingTenantContext(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrMissingTenantContext
	}
	return false
}

// NewSystemKeyspaceViolationError creates an error for system keyspace access attempts.
func NewSystemKeyspaceViolationError(op, key string) *DragonError {
	return &DragonError{
		Code: ErrSystemKeyspaceViolation,
		Op:   op,
		Err:  &MessageError{Message: "access to system keyspace is not permitted: " + key},
		Context: map[string]interface{}{
			"key": key,
		},
	}
}

// NewCrossTenantAccessError creates an error for cross-tenant access attempts.
func NewCrossTenantAccessError(op, requestingTenant, targetTenant string) *DragonError {
	return &DragonError{
		Code: ErrCrossTenantAccess,
		Op:   op,
		Err:  &MessageError{Message: "cross-tenant access denied: tenant " + requestingTenant + " cannot access tenant " + targetTenant + " data"},
		Context: map[string]interface{}{
			"requesting_tenant": requestingTenant,
			"target_tenant":     targetTenant,
		},
	}
}

// NewMissingTenantContextError creates an error for missing tenant context.
func NewMissingTenantContextError(op string) *DragonError {
	return &DragonError{
		Code: ErrMissingTenantContext,
		Op:   op,
		Err:  errors.New("tenant context is required but not present"),
	}
}

// IsMissingJustification returns true if the error is a missing justification error.
func IsMissingJustification(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrMissingJustification
	}
	return false
}

// IsMissingUserContext returns true if the error is a missing user context error.
func IsMissingUserContext(err error) bool {
	if err == nil {
		return false
	}
	var de *DragonError
	if errors.As(err, &de) {
		return de.Code == ErrMissingUserContext
	}
	return false
}

// NewMissingJustificationError creates an error for missing justification.
func NewMissingJustificationError(op string) *DragonError {
	return &DragonError{
		Code: ErrMissingJustification,
		Op:   op,
		Err:  errors.New("justification is required for cross-tenant operations"),
	}
}

// NewMissingUserContextError creates an error for missing user context.
func NewMissingUserContextError(op string) *DragonError {
	return &DragonError{
		Code: ErrMissingUserContext,
		Op:   op,
		Err:  errors.New("user context is required but not present"),
	}
}

// MessageError provides a typed wrapper for simple error messages.
type MessageError struct {
	Message string
}

// Error implements the error interface.
func (e *MessageError) Error() string {
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
