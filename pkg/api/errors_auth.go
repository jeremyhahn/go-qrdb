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

// Package api provides shared types for all DragonDB API transports.
// This file contains error types used by the auth layer.
package api

import (
	"errors"
	"fmt"
	"time"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// =============================================================================
// Auth Layer Error Types
// =============================================================================

// UserNotFoundError indicates a user was not found by ID.
type UserNotFoundError struct {
	UserID string
}

// Error implements the error interface.
func (e *UserNotFoundError) Error() string {
	return "user not found: " + e.UserID
}

// NewUserNotFoundError creates a DragonError for a missing user.
func NewUserNotFoundError(op, userID string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &UserNotFoundError{UserID: userID},
	}
}

// RoleNotFoundError indicates a role was not found by name.
type RoleNotFoundError struct {
	RoleName string
}

// Error implements the error interface.
func (e *RoleNotFoundError) Error() string {
	return "role not found: " + e.RoleName
}

// NewRoleNotFoundError creates a DragonError for a missing role.
func NewRoleNotFoundError(op, roleName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &RoleNotFoundError{RoleName: roleName},
	}
}

// UserAlreadyHasRoleError indicates a user already has the specified role.
type UserAlreadyHasRoleError struct {
	RoleName string
}

// Error implements the error interface.
func (e *UserAlreadyHasRoleError) Error() string {
	return "user already has role: " + e.RoleName
}

// NewUserAlreadyHasRoleError creates a DragonError for a duplicate role assignment.
func NewUserAlreadyHasRoleError(op, roleName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UserAlreadyHasRoleError{RoleName: roleName},
	}
}

// UserDoesNotHaveRoleError indicates a user does not have the specified role.
type UserDoesNotHaveRoleError struct {
	RoleName string
}

// Error implements the error interface.
func (e *UserDoesNotHaveRoleError) Error() string {
	return "user does not have role: " + e.RoleName
}

// NewUserDoesNotHaveRoleError creates a DragonError for revoking a role the user does not have.
func NewUserDoesNotHaveRoleError(op, roleName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UserDoesNotHaveRoleError{RoleName: roleName},
	}
}

// NonceGenerationError indicates a failure generating a cryptographic nonce.
type NonceGenerationError struct {
	Cause error
}

// Error implements the error interface.
func (e *NonceGenerationError) Error() string {
	return "failed to generate nonce: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *NonceGenerationError) Unwrap() error {
	return e.Cause
}

// NewNonceGenerationError creates a DragonError for a nonce generation failure.
func NewNonceGenerationError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &NonceGenerationError{Cause: cause},
	}
}

// TokenSigningError indicates a failure signing a JWT token.
type TokenSigningError struct {
	TokenType string
	Cause     error
}

// Error implements the error interface.
func (e *TokenSigningError) Error() string {
	return "failed to sign " + e.TokenType + " token: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *TokenSigningError) Unwrap() error {
	return e.Cause
}

// NewTokenSigningError creates a DragonError for a token signing failure.
func NewTokenSigningError(op, tokenType string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &TokenSigningError{TokenType: tokenType, Cause: cause},
	}
}

// NOTE: InvalidTokenError, InvalidTokenClaimsError, UnexpectedSigningMethodError,
// and PasswordAuthDisabledError are defined in errors.go and reused here.

// AdminUserDeleteError indicates a failure deleting an existing admin user.
type AdminUserDeleteError struct {
	Cause error
}

// Error implements the error interface.
func (e *AdminUserDeleteError) Error() string {
	return "failed to delete existing admin user: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *AdminUserDeleteError) Unwrap() error {
	return e.Cause
}

// NewAdminUserDeleteError creates a DragonError for a failed admin user deletion.
func NewAdminUserDeleteError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &AdminUserDeleteError{Cause: cause},
	}
}

// AdminUserCreateError indicates a failure creating an admin user.
type AdminUserCreateError struct {
	Cause error
}

// Error implements the error interface.
func (e *AdminUserCreateError) Error() string {
	return "failed to create admin user: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *AdminUserCreateError) Unwrap() error {
	return e.Cause
}

// NewAdminUserCreateError creates a DragonError for a failed admin user creation.
func NewAdminUserCreateError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &AdminUserCreateError{Cause: cause},
	}
}

// BootstrapMarkerError indicates a failure setting the bootstrap complete marker.
type BootstrapMarkerError struct {
	Cause error
}

// Error implements the error interface.
func (e *BootstrapMarkerError) Error() string {
	return "failed to mark bootstrap complete: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *BootstrapMarkerError) Unwrap() error {
	return e.Cause
}

// NewBootstrapMarkerError creates a DragonError for a failed bootstrap marker write.
func NewBootstrapMarkerError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &BootstrapMarkerError{Cause: cause},
	}
}

// AdminUserNotFoundError indicates the admin user record was not found in storage.
type AdminUserNotFoundError struct{}

// Error implements the error interface.
func (e *AdminUserNotFoundError) Error() string {
	return "admin user not found"
}

// NewAdminUserNotFoundError creates a DragonError for a missing admin user.
func NewAdminUserNotFoundError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &AdminUserNotFoundError{},
	}
}

// PasswordAuthDeprecatedError indicates password-based authentication is deprecated.
// This is used by deprecated functions that return ErrUnimplemented, distinct from
// PasswordAuthDisabledError in errors.go which returns ErrUnauthorized.
type PasswordAuthDeprecatedError struct{}

// Error implements the error interface.
func (e *PasswordAuthDeprecatedError) Error() string {
	return "password-based authentication is disabled; use IAM (WebAuthn, OIDC, OAuth) instead"
}

// NewPasswordAuthDeprecatedError creates a DragonError for a deprecated password auth function.
func NewPasswordAuthDeprecatedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnimplemented,
		Op:   op,
		Err:  &PasswordAuthDeprecatedError{},
	}
}

// =============================================================================
// Password Validation Error Types
// =============================================================================

// PasswordTooShortError indicates a password does not meet the minimum length requirement.
type PasswordTooShortError struct {
	MinLength int
	GotLength int
}

// Error implements the error interface.
func (e *PasswordTooShortError) Error() string {
	return fmt.Sprintf("password must be at least %d characters, got %d", e.MinLength, e.GotLength)
}

// NewPasswordTooShortError creates a DragonError for a password that is too short.
func NewPasswordTooShortError(op string, minLength, gotLength int) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &PasswordTooShortError{MinLength: minLength, GotLength: gotLength},
	}
}

// PasswordHashError indicates a failure hashing a password with bcrypt.
type PasswordHashError struct {
	Cause error
}

// Error implements the error interface.
func (e *PasswordHashError) Error() string {
	return "failed to hash password: " + e.Cause.Error()
}

// Unwrap returns the underlying cause for errors.Is and errors.As support.
func (e *PasswordHashError) Unwrap() error {
	return e.Cause
}

// NewPasswordHashError creates a DragonError for a password hashing failure.
func NewPasswordHashError(op string, cause error) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInternal,
		Op:   op,
		Err:  &PasswordHashError{Cause: cause},
	}
}

// =============================================================================
// Nil Input Error Types
// =============================================================================

// NilLoggerError indicates a nil logger was provided.
type NilLoggerError struct{}

// Error implements the error interface.
func (e *NilLoggerError) Error() string {
	return "logger cannot be nil"
}

// NewNilLoggerError creates a DragonError for a nil logger.
func NewNilLoggerError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NilLoggerError{},
	}
}

// NilStorageError indicates a nil storage was provided where one is required.
type NilStorageError struct{}

// Error implements the error interface.
func (e *NilStorageError) Error() string {
	return "storage cannot be nil for persistent auth"
}

// NewNilStorageError creates a DragonError for a nil storage.
func NewNilStorageError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &NilStorageError{},
	}
}

// UnknownAuthManagerTypeError indicates an unrecognized auth manager type.
type UnknownAuthManagerTypeError struct {
	TypeName string
}

// Error implements the error interface.
func (e *UnknownAuthManagerTypeError) Error() string {
	return "unknown auth manager type: " + e.TypeName
}

// NewUnknownAuthManagerTypeError creates a DragonError for an unknown auth manager type.
func NewUnknownAuthManagerTypeError(op, typeName string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &UnknownAuthManagerTypeError{TypeName: typeName},
	}
}

// StorageClosedAuthError indicates a storage backend used by auth is closed.
type StorageClosedAuthError struct{}

// Error implements the error interface.
func (e *StorageClosedAuthError) Error() string {
	return "storage closed"
}

// NewStorageClosedAuthError creates a DragonError for a closed auth storage backend.
func NewStorageClosedAuthError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnavailable,
		Op:   op,
		Err:  &StorageClosedAuthError{},
	}
}

// KeyNotFoundError indicates a storage key was not found.
type KeyNotFoundError struct{}

// Error implements the error interface.
func (e *KeyNotFoundError) Error() string {
	return "key not found"
}

// NewKeyNotFoundError creates a DragonError for a missing storage key.
func NewKeyNotFoundError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrNotFound,
		Op:   op,
		Err:  &KeyNotFoundError{},
	}
}

// SnapshotNotSupportedError indicates snapshots are not supported by this storage backend.
type SnapshotNotSupportedError struct{}

// Error implements the error interface.
func (e *SnapshotNotSupportedError) Error() string {
	return "snapshots not implemented in mock"
}

// NewSnapshotNotSupportedError creates a DragonError for unsupported snapshots.
func NewSnapshotNotSupportedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrUnimplemented,
		Op:   op,
		Err:  &SnapshotNotSupportedError{},
	}
}

// BatchFinalizedError indicates a batch operation has already been committed or rolled back.
type BatchFinalizedError struct{}

// Error implements the error interface.
func (e *BatchFinalizedError) Error() string {
	return "batch already finalized"
}

// NewBatchFinalizedError creates a DragonError for a finalized batch.
func NewBatchFinalizedError(op string) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrInvalidInput,
		Op:   op,
		Err:  &BatchFinalizedError{},
	}
}

// AdminUserExistsError indicates the admin user already exists.
type AdminUserExistsError struct {
	Username string
}

// Error implements the error interface.
func (e *AdminUserExistsError) Error() string {
	return fmt.Sprintf("Admin user '%s' already exists", e.Username)
}

// NewAdminUserExistsError creates an AdminUserExistsError.
func NewAdminUserExistsError(username string) *AdminUserExistsError {
	return &AdminUserExistsError{Username: username}
}

// =============================================================================
// Account Lockout Error Types
// =============================================================================

// AccountLockedError indicates a user account is temporarily locked due to
// exceeding the maximum number of failed login attempts.
type AccountLockedError struct {
	Username   string
	RetryAfter time.Duration
}

// Error implements the error interface.
func (e *AccountLockedError) Error() string {
	return "account locked for user " + e.Username + ": too many failed login attempts, retry after " + e.RetryAfter.String()
}

// NewAccountLockedError creates a DragonError for a locked account.
func NewAccountLockedError(op, username string, retryAfter time.Duration) *dberrors.DragonError {
	return &dberrors.DragonError{
		Code: dberrors.ErrRateLimited,
		Op:   op,
		Err:  &AccountLockedError{Username: username, RetryAfter: retryAfter},
	}
}

// IsAccountLocked returns true if the error indicates an account lockout.
func IsAccountLocked(err error) bool {
	if err == nil {
		return false
	}
	var ale *AccountLockedError
	return errors.As(err, &ale)
}
