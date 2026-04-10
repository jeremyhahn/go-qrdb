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
	"time"
)

// ParameterNotFoundError indicates the parameter was not found.
type ParameterNotFoundError struct {
	Path string
}

// Error implements error interface.
func (e *ParameterNotFoundError) Error() string {
	return fmt.Sprintf("parameter not found: %s", e.Path)
}

// ParameterExistsError indicates the parameter already exists.
type ParameterExistsError struct {
	Path string
}

// Error implements error interface.
func (e *ParameterExistsError) Error() string {
	return fmt.Sprintf("parameter already exists: %s", e.Path)
}

// InvalidPathError indicates an invalid parameter path.
type InvalidPathError struct {
	Path   string
	Reason string
}

// Error implements error interface.
func (e *InvalidPathError) Error() string {
	return fmt.Sprintf("invalid parameter path '%s': %s", e.Path, e.Reason)
}

// ParameterExpiredError indicates the parameter has expired.
type ParameterExpiredError struct {
	Path      string
	ExpiredAt time.Time
}

// Error implements error interface.
func (e *ParameterExpiredError) Error() string {
	return fmt.Sprintf("parameter '%s' expired at %s", e.Path, e.ExpiredAt.Format(time.RFC3339))
}

// ParameterEncryptionError indicates encryption/decryption failed.
type ParameterEncryptionError struct {
	Op  string
	Err error
}

// Error implements error interface.
func (e *ParameterEncryptionError) Error() string {
	return fmt.Sprintf("%s: encryption failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *ParameterEncryptionError) Unwrap() error {
	return e.Err
}

// ParameterValueTooLargeError indicates the parameter value exceeds size limits.
type ParameterValueTooLargeError struct {
	Path    string
	Size    int
	MaxSize int
}

// Error implements error interface.
func (e *ParameterValueTooLargeError) Error() string {
	return fmt.Sprintf("parameter '%s' value size %d exceeds maximum %d", e.Path, e.Size, e.MaxSize)
}

// ParameterPathTooLongError indicates the parameter path exceeds length limits.
type ParameterPathTooLongError struct {
	Path      string
	Length    int
	MaxLength int
}

// Error implements error interface.
func (e *ParameterPathTooLongError) Error() string {
	return fmt.Sprintf("parameter path length %d exceeds maximum %d", e.Length, e.MaxLength)
}

// ParameterTagLimitError indicates too many tags on a parameter.
type ParameterTagLimitError struct {
	Path     string
	Count    int
	MaxCount int
}

// Error implements error interface.
func (e *ParameterTagLimitError) Error() string {
	return fmt.Sprintf("parameter '%s' has %d tags, exceeds maximum %d", e.Path, e.Count, e.MaxCount)
}

// NewParameterNotFoundError creates a new ParameterNotFoundError wrapped in DragonError.
func NewParameterNotFoundError(path string) *DragonError {
	return &DragonError{
		Code: ErrNotFound,
		Op:   "parameterstore",
		Err:  &ParameterNotFoundError{Path: path},
	}
}

// NewParameterExistsError creates a new ParameterExistsError wrapped in DragonError.
func NewParameterExistsError(path string) *DragonError {
	return &DragonError{
		Code: ErrAlreadyExists,
		Op:   "parameterstore",
		Err:  &ParameterExistsError{Path: path},
	}
}

// NewInvalidPathError creates a new InvalidPathError wrapped in DragonError.
func NewInvalidPathError(path, reason string) *DragonError {
	return &DragonError{
		Code: ErrInvalidInput,
		Op:   "parameterstore",
		Err:  &InvalidPathError{Path: path, Reason: reason},
	}
}

// NewParameterExpiredError creates a new ParameterExpiredError wrapped in DragonError.
func NewParameterExpiredError(path string, expiredAt time.Time) *DragonError {
	return &DragonError{
		Code: ErrNotFound,
		Op:   "parameterstore",
		Err:  &ParameterExpiredError{Path: path, ExpiredAt: expiredAt},
	}
}

// NewParameterEncryptionError creates a new ParameterEncryptionError wrapped in DragonError.
func NewParameterEncryptionError(op string, err error) *DragonError {
	return &DragonError{
		Code: ErrEncryption,
		Op:   op,
		Err:  &ParameterEncryptionError{Op: op, Err: err},
	}
}

// NewParameterDecryptionError creates a new decryption error wrapped in DragonError.
func NewParameterDecryptionError(op string, err error) *DragonError {
	return &DragonError{
		Code: ErrDecryption,
		Op:   op,
		Err:  &ParameterEncryptionError{Op: op, Err: err},
	}
}

// NewParameterValueTooLargeError creates a new ParameterValueTooLargeError wrapped in DragonError.
func NewParameterValueTooLargeError(path string, size, maxSize int) *DragonError {
	return &DragonError{
		Code: ErrInvalidInput,
		Op:   "parameterstore",
		Err:  &ParameterValueTooLargeError{Path: path, Size: size, MaxSize: maxSize},
	}
}

// NewParameterPathTooLongError creates a new ParameterPathTooLongError wrapped in DragonError.
func NewParameterPathTooLongError(path string, length, maxLength int) *DragonError {
	return &DragonError{
		Code: ErrInvalidInput,
		Op:   "parameterstore",
		Err:  &ParameterPathTooLongError{Path: path, Length: length, MaxLength: maxLength},
	}
}

// NewParameterTagLimitError creates a new ParameterTagLimitError wrapped in DragonError.
func NewParameterTagLimitError(path string, count, maxCount int) *DragonError {
	return &DragonError{
		Code: ErrInvalidInput,
		Op:   "parameterstore",
		Err:  &ParameterTagLimitError{Path: path, Count: count, MaxCount: maxCount},
	}
}

// IsParameterNotFound returns true if the error is a ParameterNotFoundError.
func IsParameterNotFound(err error) bool {
	var pnf *ParameterNotFoundError
	return errors.As(err, &pnf)
}

// IsParameterExists returns true if the error is a ParameterExistsError.
func IsParameterExists(err error) bool {
	var pe *ParameterExistsError
	return errors.As(err, &pe)
}

// IsParameterExpired returns true if the error is a ParameterExpiredError.
func IsParameterExpired(err error) bool {
	var pe *ParameterExpiredError
	return errors.As(err, &pe)
}

// IsInvalidPath returns true if the error is an InvalidPathError.
func IsInvalidPath(err error) bool {
	var ip *InvalidPathError
	return errors.As(err, &ip)
}

// IsParameterEncryptionError returns true if the error is a ParameterEncryptionError.
func IsParameterEncryptionError(err error) bool {
	var pe *ParameterEncryptionError
	return errors.As(err, &pe)
}
