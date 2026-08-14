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

package validation

import "fmt"

// Constraint enumerates the specific validation rule that was violated.
type Constraint int

const (
	// ConstraintEmpty indicates the value was empty.
	ConstraintEmpty Constraint = iota
	// ConstraintNullByte indicates the value contains a null byte.
	ConstraintNullByte
	// ConstraintTooLong indicates the value exceeds the maximum length.
	ConstraintTooLong
	// ConstraintAbsolutePath indicates the value is an absolute filesystem path.
	ConstraintAbsolutePath
	// ConstraintPathTraversal indicates the value contains a path traversal sequence.
	ConstraintPathTraversal
	// ConstraintControlChars indicates the value contains control characters.
	ConstraintControlChars
	// ConstraintInvalidChars indicates the value contains disallowed characters.
	ConstraintInvalidChars
	// ConstraintInvalidFormat indicates the value does not match the required format.
	ConstraintInvalidFormat
	// ConstraintInvalidKeyType indicates the key type is not recognized.
	ConstraintInvalidKeyType
	// ConstraintInvalidAlgorithm indicates the algorithm is not recognized.
	ConstraintInvalidAlgorithm
)

// constraintMessages maps each constraint to a human-readable explanation.
var constraintMessages = map[Constraint]string{
	ConstraintEmpty:            "cannot be empty",
	ConstraintNullByte:         "contains null byte",
	ConstraintTooLong:          "exceeds maximum length",
	ConstraintAbsolutePath:     "cannot be an absolute path",
	ConstraintPathTraversal:    "contains path traversal attempt",
	ConstraintControlChars:     "contains control characters",
	ConstraintInvalidChars:     "contains invalid characters",
	ConstraintInvalidFormat:    "invalid format",
	ConstraintInvalidKeyType:   "invalid key type",
	ConstraintInvalidAlgorithm: "invalid algorithm",
}

// String returns the human-readable description for a Constraint.
func (c Constraint) String() string {
	if msg, ok := constraintMessages[c]; ok {
		return msg
	}
	return "unknown constraint"
}

// ErrValidation is the base validation error carrying the field name, the
// violated constraint, and optional detail. Domain packages define their own
// error types by embedding ErrValidation:
//
//	type ErrUserID struct {
//		validation.ErrValidation
//	}
//
// Because the As method below is promoted through embedding, errors.As with a
// **ErrValidation target matches any embedding wrapper, letting callers handle
// all validation failures uniformly while still being able to match the
// concrete domain type.
type ErrValidation struct { //nolint:errname // exported API embedded by domain error types, rename is a breaking change
	// Field is the name of the field that failed validation (e.g. "tenant ID").
	Field string
	// Constraint is the specific rule that was violated.
	Constraint Constraint
	// Detail provides additional context (e.g. max length, allowed characters).
	Detail string
}

// Error returns a human-readable error message.
func (e *ErrValidation) Error() string {
	base := fmt.Sprintf("%s %s", e.Field, e.Constraint)
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", base, e.Detail)
	}
	return base
}

// As supports errors.As by matching **ErrValidation targets. Domain error
// types that embed ErrValidation inherit this method through promotion, so
// errors.As(err, &valErr) succeeds for any embedding wrapper without each
// wrapper reimplementing the pattern.
func (e *ErrValidation) As(target any) bool {
	if t, ok := target.(**ErrValidation); ok {
		*t = e
		return true
	}
	return false
}

// NewErrValidation creates an ErrValidation for the given field and
// constraint with optional detail.
func NewErrValidation(field string, constraint Constraint, detail string) *ErrValidation {
	return &ErrValidation{
		Field:      field,
		Constraint: constraint,
		Detail:     detail,
	}
}
