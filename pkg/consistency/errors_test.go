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

package consistency

import (
	"strings"
	"testing"
)

func TestUnknownLevelErrorMessage(t *testing.T) {
	err := &UnknownLevelError{Level: "foobar"}
	got := err.Error()
	if !strings.Contains(got, "foobar") {
		t.Errorf("UnknownLevelError.Error() = %q, want it to contain %q", got, "foobar")
	}
	if !strings.Contains(got, "unknown consistency level") {
		t.Errorf("UnknownLevelError.Error() = %q, want it to contain %q", got, "unknown consistency level")
	}
}

func TestUnknownLevelErrorContainsValidOptions(t *testing.T) {
	err := &UnknownLevelError{Level: "invalid"}
	got := err.Error()
	if !strings.Contains(got, "strong") {
		t.Errorf("UnknownLevelError.Error() = %q, want it to contain %q", got, "strong")
	}
	if !strings.Contains(got, "linearizable") {
		t.Errorf("UnknownLevelError.Error() = %q, want it to contain %q", got, "linearizable")
	}
	if !strings.Contains(got, "eventual") {
		t.Errorf("UnknownLevelError.Error() = %q, want it to contain %q", got, "eventual")
	}
}

func TestUnknownLevelErrorImplementsError(t *testing.T) {
	var err error = &UnknownLevelError{Level: "test"}
	if err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestInvalidLevelErrorMessage(t *testing.T) {
	err := &InvalidLevelError{Level: Level(42)}
	got := err.Error()
	if !strings.Contains(got, "invalid consistency level") {
		t.Errorf("InvalidLevelError.Error() = %q, want it to contain %q", got, "invalid consistency level")
	}
	if !strings.Contains(got, "42") {
		t.Errorf("InvalidLevelError.Error() = %q, want it to contain %q", got, "42")
	}
}

func TestInvalidLevelErrorWithNegativeLevel(t *testing.T) {
	err := &InvalidLevelError{Level: Level(-5)}
	got := err.Error()
	if !strings.Contains(got, "-5") {
		t.Errorf("InvalidLevelError.Error() = %q, want it to contain %q", got, "-5")
	}
}

func TestInvalidLevelErrorImplementsError(t *testing.T) {
	var err error = &InvalidLevelError{Level: Level(10)}
	if err == nil {
		t.Fatal("expected non-nil error")
	}
}

func TestInvalidTimeoutErrorMessage(t *testing.T) {
	err := &InvalidTimeoutError{Timeout: -500}
	got := err.Error()
	if !strings.Contains(got, "timeout must be non-negative") {
		t.Errorf("InvalidTimeoutError.Error() = %q, want it to contain %q", got, "timeout must be non-negative")
	}
	if !strings.Contains(got, "-500") {
		t.Errorf("InvalidTimeoutError.Error() = %q, want it to contain %q", got, "-500")
	}
}

func TestInvalidTimeoutErrorWithLargeNegative(t *testing.T) {
	err := &InvalidTimeoutError{Timeout: -99999}
	got := err.Error()
	if !strings.Contains(got, "-99999") {
		t.Errorf("InvalidTimeoutError.Error() = %q, want it to contain %q", got, "-99999")
	}
}

func TestInvalidTimeoutErrorImplementsError(t *testing.T) {
	var err error = &InvalidTimeoutError{Timeout: -1}
	if err == nil {
		t.Fatal("expected non-nil error")
	}
}
