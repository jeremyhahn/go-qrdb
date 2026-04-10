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

package tenant

import (
	"strings"
	"testing"
)

func TestInvalidTenantIDErrorMessage(t *testing.T) {
	err := &InvalidTenantIDError{TenantID: "bad!tenant", Reason: "contains special characters"}
	got := err.Error()
	if !strings.Contains(got, "invalid tenant ID") {
		t.Errorf("Error() = %q, want it to contain %q", got, "invalid tenant ID")
	}
	if !strings.Contains(got, "bad!tenant") {
		t.Errorf("Error() = %q, want it to contain %q", got, "bad!tenant")
	}
	if !strings.Contains(got, "contains special characters") {
		t.Errorf("Error() = %q, want it to contain %q", got, "contains special characters")
	}
}

func TestInvalidTenantIDErrorEmptyTenantID(t *testing.T) {
	err := &InvalidTenantIDError{TenantID: "", Reason: "must not be empty"}
	got := err.Error()
	if !strings.Contains(got, "must not be empty") {
		t.Errorf("Error() = %q, want it to contain %q", got, "must not be empty")
	}
}

func TestInvalidTenantIDErrorImplementsErrorInterface(t *testing.T) {
	err := &InvalidTenantIDError{TenantID: "x", Reason: "test"}
	var _ error = err
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestInvalidTenantIDErrorFormat(t *testing.T) {
	err := &InvalidTenantIDError{TenantID: "abc", Reason: "too short"}
	want := "invalid tenant ID abc: too short"
	got := err.Error()
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
