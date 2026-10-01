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
