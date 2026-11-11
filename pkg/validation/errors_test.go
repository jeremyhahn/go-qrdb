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

import (
	"errors"
	"testing"
)

// domainFieldError is a test double simulating a downstream domain error type
// that embeds ErrValidation, mirroring how consuming packages are expected
// to use the framework.
type domainFieldError struct {
	ErrValidation
}

// TestConstraintString verifies the Constraint.String() output for every
// defined constraint.
func TestConstraintString(t *testing.T) {
	tests := []struct {
		constraint Constraint
		want       string
	}{
		{ConstraintEmpty, "cannot be empty"},
		{ConstraintNullByte, "contains null byte"},
		{ConstraintTooLong, "exceeds maximum length"},
		{ConstraintAbsolutePath, "cannot be an absolute path"},
		{ConstraintPathTraversal, "contains path traversal attempt"},
		{ConstraintControlChars, "contains control characters"},
		{ConstraintInvalidChars, "contains invalid characters"},
		{ConstraintInvalidFormat, "invalid format"},
		{ConstraintInvalidKeyType, "invalid key type"},
		{ConstraintInvalidAlgorithm, "invalid algorithm"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.constraint.String(); got != tt.want {
				t.Errorf("Constraint(%d).String() = %q, want %q", tt.constraint, got, tt.want)
			}
		})
	}
}

// TestErrValidationError verifies the message format with and without detail.
func TestErrValidationError(t *testing.T) {
	tests := []struct {
		name string
		err  *ErrValidation
		want string
	}{
		{
			name: "without detail",
			err:  &ErrValidation{Field: "tenant ID", Constraint: ConstraintEmpty},
			want: "tenant ID cannot be empty",
		},
		{
			name: "with detail",
			err:  &ErrValidation{Field: "tenant ID", Constraint: ConstraintTooLong, Detail: "max 255 characters"},
			want: "tenant ID exceeds maximum length: max 255 characters",
		},
		{
			name: "unknown constraint",
			err:  &ErrValidation{Field: "value", Constraint: Constraint(999)},
			want: "value unknown constraint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestErrValidationAs verifies the As method matches **ErrValidation targets
// and rejects unrelated targets.
func TestErrValidationAs(t *testing.T) {
	src := NewErrValidation("field", ConstraintInvalidChars, "allowed: a-z")

	t.Run("matches ErrValidation target", func(t *testing.T) {
		var target *ErrValidation
		if !src.As(&target) {
			t.Fatal("As(**ErrValidation) = false, want true")
		}
		if target != src {
			t.Errorf("As set target to %p, want %p", target, src)
		}
	})

	t.Run("rejects unrelated target", func(t *testing.T) {
		var other *domainFieldError
		if src.As(&other) {
			t.Error("As(**domainFieldError) = true, want false")
		}
		if other != nil {
			t.Error("As mutated an unrelated target")
		}
	})
}

// TestErrValidationAsPromotion verifies that a domain error type embedding
// ErrValidation matches **ErrValidation targets through the promoted As
// method — the core contract that lets callers handle all validation
// failures uniformly.
func TestErrValidationAsPromotion(t *testing.T) {
	var err error = &domainFieldError{
		ErrValidation: ErrValidation{
			Field:      "user ID",
			Constraint: ConstraintNullByte,
		},
	}

	var valErr *ErrValidation
	if !errors.As(err, &valErr) {
		t.Fatalf("errors.As(%T, **ErrValidation) = false, want true", err)
	}
	if valErr.Field != "user ID" {
		t.Errorf("field = %q, want %q", valErr.Field, "user ID")
	}
	if valErr.Constraint != ConstraintNullByte {
		t.Errorf("constraint = %v, want ConstraintNullByte", valErr.Constraint)
	}

	// The concrete domain type must still be matchable directly.
	var domainErr *domainFieldError
	if !errors.As(err, &domainErr) {
		t.Fatalf("errors.As(%T, **domainFieldError) = false, want true", err)
	}
}

// TestNewErrValidation verifies the constructor populates all fields.
func TestNewErrValidation(t *testing.T) {
	err := NewErrValidation("backend name", ConstraintInvalidChars, "allowed: a-z, 0-9, -")
	if err.Field != "backend name" {
		t.Errorf("field = %q, want %q", err.Field, "backend name")
	}
	if err.Constraint != ConstraintInvalidChars {
		t.Errorf("constraint = %v, want ConstraintInvalidChars", err.Constraint)
	}
	if err.Detail != "allowed: a-z, 0-9, -" {
		t.Errorf("detail = %q, want %q", err.Detail, "allowed: a-z, 0-9, -")
	}
}
