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

package lease

import (
	"strings"
	"testing"
)

func TestNotFoundErrorMessage(t *testing.T) {
	err := &NotFoundError{ID: 42}
	got := err.Error()
	if !strings.Contains(got, "lease not found") {
		t.Errorf("NotFoundError.Error() = %q, want it to contain %q", got, "lease not found")
	}
	if !strings.Contains(got, "42") {
		t.Errorf("NotFoundError.Error() = %q, want it to contain %q", got, "42")
	}
}

func TestNotFoundErrorWithLargeID(t *testing.T) {
	err := &NotFoundError{ID: 9999999}
	got := err.Error()
	if !strings.Contains(got, "9999999") {
		t.Errorf("NotFoundError.Error() = %q, want it to contain %q", got, "9999999")
	}
}

func TestNotFoundErrorImplementsError(t *testing.T) {
	err := &NotFoundError{ID: 1}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

func TestExistsErrorMessage(t *testing.T) {
	err := &ExistsError{ID: 100}
	got := err.Error()
	if !strings.Contains(got, "lease already exists") {
		t.Errorf("ExistsError.Error() = %q, want it to contain %q", got, "lease already exists")
	}
	if !strings.Contains(got, "100") {
		t.Errorf("ExistsError.Error() = %q, want it to contain %q", got, "100")
	}
}

func TestExistsErrorWithDifferentID(t *testing.T) {
	err := &ExistsError{ID: 555}
	got := err.Error()
	if !strings.Contains(got, "555") {
		t.Errorf("ExistsError.Error() = %q, want it to contain %q", got, "555")
	}
}

func TestExistsErrorImplementsError(t *testing.T) {
	err := &ExistsError{ID: 1}
	// Verify the concrete type satisfies the error interface.
	var _ error = err
}

func TestInvalidTTLErrorMessage(t *testing.T) {
	err := &InvalidTTLError{TTL: -10}
	got := err.Error()
	if !strings.Contains(got, "invalid lease TTL") {
		t.Errorf("InvalidTTLError.Error() = %q, want it to contain %q", got, "invalid lease TTL")
	}
	if !strings.Contains(got, "-10") {
		t.Errorf("InvalidTTLError.Error() = %q, want it to contain %q", got, "-10")
	}
	if !strings.Contains(got, "must be > 0") {
		t.Errorf("InvalidTTLError.Error() = %q, want it to contain %q", got, "must be > 0")
	}
}

func TestInvalidTTLErrorWithZero(t *testing.T) {
	err := &InvalidTTLError{TTL: 0}
	got := err.Error()
	if !strings.Contains(got, "0") {
		t.Errorf("InvalidTTLError.Error() = %q, want it to contain %q", got, "0")
	}
}

func TestInvalidTTLErrorImplementsError(t *testing.T) {
	err := &InvalidTTLError{TTL: -1}
	var _ error = err // compile-time interface assertion
	_ = err
}

func TestLessorClosedErrorMessage(t *testing.T) {
	err := &LessorClosedError{}
	got := err.Error()
	if got != "lessor is closed" {
		t.Errorf("LessorClosedError.Error() = %q, want %q", got, "lessor is closed")
	}
}

func TestLessorClosedErrorImplementsError(t *testing.T) {
	err := &LessorClosedError{}
	var _ error = err // compile-time interface assertion
	_ = err
}
