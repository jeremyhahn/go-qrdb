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
