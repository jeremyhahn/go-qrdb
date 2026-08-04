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
	"github.com/jeremyhahn/go-qrdb/pkg/validation"
)

// ---------------------------------------------------------------------------
// Validation constraint vocabulary
// ---------------------------------------------------------------------------

// ValidationConstraint enumerates the specific validation rule that was
// violated. (Aliases pkg/validation.Constraint; prefixed to avoid an overly
// generic name in the flat SDK namespace.)
type ValidationConstraint = validation.Constraint

// Validation constraint values re-exported from pkg/validation.
const (
	// ValidationConstraintEmpty indicates the value was empty.
	ValidationConstraintEmpty = validation.ConstraintEmpty
	// ValidationConstraintNullByte indicates the value contains a null byte.
	ValidationConstraintNullByte = validation.ConstraintNullByte
	// ValidationConstraintTooLong indicates the value exceeds the maximum length.
	ValidationConstraintTooLong = validation.ConstraintTooLong
	// ValidationConstraintAbsolutePath indicates the value is an absolute filesystem path.
	ValidationConstraintAbsolutePath = validation.ConstraintAbsolutePath
	// ValidationConstraintPathTraversal indicates the value contains a path traversal sequence.
	ValidationConstraintPathTraversal = validation.ConstraintPathTraversal
	// ValidationConstraintControlChars indicates the value contains control characters.
	ValidationConstraintControlChars = validation.ConstraintControlChars
	// ValidationConstraintInvalidChars indicates the value contains disallowed characters.
	ValidationConstraintInvalidChars = validation.ConstraintInvalidChars
	// ValidationConstraintInvalidFormat indicates the value does not match the required format.
	ValidationConstraintInvalidFormat = validation.ConstraintInvalidFormat
	// ValidationConstraintInvalidKeyType indicates the key type is not recognized.
	ValidationConstraintInvalidKeyType = validation.ConstraintInvalidKeyType
	// ValidationConstraintInvalidAlgorithm indicates the algorithm is not recognized.
	ValidationConstraintInvalidAlgorithm = validation.ConstraintInvalidAlgorithm
)

// ---------------------------------------------------------------------------
// Validation error type
// ---------------------------------------------------------------------------

// ValidationError is the base validation error carrying the field name, the
// violated constraint, and optional detail. Domain error types embed it so
// errors.As with a **ValidationError target matches any embedding wrapper.
// (Aliases pkg/validation.ErrValidation; named ValidationError to avoid
// colliding with the database error sentinel ErrValidation re-exported in
// types_errors.go, which is a distinct classification value.)
type ValidationError = validation.ErrValidation

// NewValidationError creates a ValidationError for the given field and
// constraint with optional detail.
var NewValidationError = validation.NewErrValidation

// ---------------------------------------------------------------------------
// Sanitization helpers
// ---------------------------------------------------------------------------

// SanitizeForLog sanitizes a string for safe logging (prevents log injection).
var SanitizeForLog = validation.SanitizeForLog
