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

package planner_test

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/planner"
)

// TestNoCatalogError verifies NoCatalogError implements error and formats
// its message correctly.
func TestNoCatalogError(t *testing.T) {
	// Compile-time interface assertion: NoCatalogError implements error.
	var _ error = (*planner.NoCatalogError)(nil)

	err := &planner.NoCatalogError{}
	want := "planner: no index catalog configured"
	if err.Error() != want {
		t.Errorf("NoCatalogError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestNoCatalogErrorIsError(t *testing.T) {
	err := &planner.NoCatalogError{}
	var target *planner.NoCatalogError
	if !errors.As(err, &target) {
		t.Error("errors.As should find NoCatalogError")
	}
}

// TestInvalidPredicateError verifies the message includes field and reason.
func TestInvalidPredicateError(t *testing.T) {
	err := &planner.InvalidPredicateError{
		Field:  "Age",
		Reason: "value must be non-negative",
	}

	want := "planner: invalid predicate on field Age: value must be non-negative"
	if err.Error() != want {
		t.Errorf("InvalidPredicateError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestInvalidPredicateErrorIsError(t *testing.T) {
	var err error = &planner.InvalidPredicateError{Field: "X", Reason: "bad"}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
	var target *planner.InvalidPredicateError
	if !errors.As(err, &target) {
		t.Error("errors.As should find InvalidPredicateError")
	}
	if target.Field != "X" {
		t.Errorf("Field = %q, want X", target.Field)
	}
	if target.Reason != "bad" {
		t.Errorf("Reason = %q, want bad", target.Reason)
	}
}

// TestFieldNotFoundError verifies the message includes field and entity type.
func TestFieldNotFoundError(t *testing.T) {
	err := &planner.FieldNotFoundError{
		Field:      "Email",
		EntityType: "planner_test.testEntity",
	}

	want := "planner: field Email not found on entity type planner_test.testEntity"
	if err.Error() != want {
		t.Errorf("FieldNotFoundError.Error() = %q, want %q", err.Error(), want)
	}
}

func TestFieldNotFoundErrorIsError(t *testing.T) {
	var err error = &planner.FieldNotFoundError{Field: "F", EntityType: "T"}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
	var target *planner.FieldNotFoundError
	if !errors.As(err, &target) {
		t.Error("errors.As should find FieldNotFoundError")
	}
	if target.Field != "F" {
		t.Errorf("Field = %q, want F", target.Field)
	}
	if target.EntityType != "T" {
		t.Errorf("EntityType = %q, want T", target.EntityType)
	}
}

// TestAllErrorsDistinct verifies that each error type is distinct and not
// accidentally aliased to another.
func TestAllErrorsDistinct(t *testing.T) {
	noCatalog := &planner.NoCatalogError{}
	invalidPred := &planner.InvalidPredicateError{Field: "F", Reason: "R"}
	fieldNotFound := &planner.FieldNotFoundError{Field: "F", EntityType: "T"}

	if noCatalog.Error() == invalidPred.Error() {
		t.Error("NoCatalogError and InvalidPredicateError messages should differ")
	}
	if noCatalog.Error() == fieldNotFound.Error() {
		t.Error("NoCatalogError and FieldNotFoundError messages should differ")
	}
	if invalidPred.Error() == fieldNotFound.Error() {
		t.Error("InvalidPredicateError and FieldNotFoundError messages should differ")
	}
}
