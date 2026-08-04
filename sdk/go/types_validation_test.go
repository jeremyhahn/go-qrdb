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
	"errors"
	"testing"
)

// TestValidationAliases verifies the SDK aliases are wired to the canonical
// pkg/validation implementations.
func TestValidationAliases(t *testing.T) {
	err := NewValidationError("tenant ID", ValidationConstraintEmpty, "")
	if err.Error() != "tenant ID cannot be empty" {
		t.Errorf("Error() = %q, want %q", err.Error(), "tenant ID cannot be empty")
	}

	var valErr *ValidationError
	if !errors.As(error(err), &valErr) {
		t.Fatal("errors.As(*ValidationError) = false, want true")
	}
	if valErr.Constraint != ValidationConstraintEmpty {
		t.Errorf("constraint = %v, want ValidationConstraintEmpty", valErr.Constraint)
	}
}

// TestValidationSanitizeForLogAlias verifies the SanitizeForLog re-export
// strips control characters through the alias.
func TestValidationSanitizeForLogAlias(t *testing.T) {
	if got := SanitizeForLog("key\nINFO: fake"); got != "keyINFO: fake" {
		t.Errorf("SanitizeForLog = %q, want %q", got, "keyINFO: fake")
	}
	if got := SanitizeForLog("clean"); got != "clean" {
		t.Errorf("SanitizeForLog = %q, want %q", got, "clean")
	}
}
