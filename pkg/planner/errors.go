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

package planner

// NoCatalogError is returned when the planner is invoked without an
// index catalog configured.
type NoCatalogError struct{}

// Error implements the error interface.
func (e *NoCatalogError) Error() string {
	return "planner: no index catalog configured"
}

// InvalidPredicateError is returned when a predicate is malformed or
// contains an invalid combination of fields and operator.
type InvalidPredicateError struct {
	// Field is the predicate field that caused the error.
	Field string

	// Reason describes why the predicate is invalid.
	Reason string
}

// Error implements the error interface.
func (e *InvalidPredicateError) Error() string {
	return "planner: invalid predicate on field " + e.Field + ": " + e.Reason
}

// FieldNotFoundError is returned when a predicate references a field that
// does not exist on the entity type being evaluated.
type FieldNotFoundError struct {
	// Field is the name of the missing field.
	Field string

	// EntityType is the string representation of the entity's reflect.Type.
	EntityType string
}

// Error implements the error interface.
func (e *FieldNotFoundError) Error() string {
	return "planner: field " + e.Field + " not found on entity type " + e.EntityType
}
