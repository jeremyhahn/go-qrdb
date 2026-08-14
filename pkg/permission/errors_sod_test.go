// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package permission

import (
	"strings"
	"testing"
)

// The separation-of-duties errors are panic values. Their message is the ONLY
// diagnosis a contributor gets — there is no stack of wrapped context, no log
// line, and no way to recover and inspect fields in the init() path where they
// fire. These tests pin the parts of each message a reader needs to act on: the
// offending permission, the role, and (for the fail-safe error) the call that
// resolves it.

// TestSoDViolationError_Message verifies the FIPS violation message names the
// standard, the role, and the permission.
func TestSoDViolationError_Message(t *testing.T) {
	err := &SoDViolationError{Role: RoleSO, Permission: KeysSign}
	got := err.Error()
	for _, want := range []string{"AS10.03", RoleSO, string(KeysSign)} {
		if !strings.Contains(got, want) {
			t.Errorf("SoDViolationError.Error() = %q, missing %q", got, want)
		}
	}
}

// TestUndeclaredDutyError_MessageNamesRemediation verifies the fail-safe error
// tells the author exactly which call to add. Without this the panic reads as an
// unexplained refusal, and a contributor's likely next move — deleting the guard
// — is the worst possible outcome.
func TestUndeclaredDutyError_MessageNamesRemediation(t *testing.T) {
	err := &UndeclaredDutyError{Role: RoleSO, Permission: "attestation:attest"}
	got := err.Error()
	for _, want := range []string{
		"AS10.03", RoleSO, "attestation:attest", "undeclared",
		"RegisterDuty(DutyGovernance,", "RegisterDuty(DutyCryptoUse,",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("UndeclaredDutyError.Error() = %q, missing %q", got, want)
		}
	}
}

// TestWildcardGrantError_Message verifies the wildcard refusal names the
// wildcard and points at the alternative (concrete permissions).
func TestWildcardGrantError_Message(t *testing.T) {
	err := &WildcardGrantError{Role: RoleSO, Permission: "keys:*"}
	got := err.Error()
	for _, want := range []string{RoleSO, "keys:*", "wildcard", "concrete"} {
		if !strings.Contains(got, want) {
			t.Errorf("WildcardGrantError.Error() = %q, missing %q", got, want)
		}
	}
}

// TestInvalidDutyError_Message verifies the message reports the rejected value
// by label and names the declarable alternatives.
func TestInvalidDutyError_Message(t *testing.T) {
	tests := []struct {
		duty      Duty
		wantLabel string
	}{
		{DutyUndeclared, "undeclared"},
		{Duty(99), "unknown"},
	}
	for _, tc := range tests {
		got := (&InvalidDutyError{Duty: tc.duty}).Error()
		for _, want := range []string{tc.wantLabel, "DutyGovernance", "DutyCryptoUse"} {
			if !strings.Contains(got, want) {
				t.Errorf("InvalidDutyError{%s}.Error() = %q, missing %q", tc.duty, got, want)
			}
		}
	}
}

// TestDutyConflictError_Message verifies the immutability refusal reports both
// the existing and the requested classification, so the reader can tell which
// declaration is the mistake.
func TestDutyConflictError_Message(t *testing.T) {
	err := &DutyConflictError{
		Permission: KeysVerify,
		Existing:   DutyCryptoUse,
		Requested:  DutyGovernance,
	}
	got := err.Error()
	for _, want := range []string{string(KeysVerify), "crypto-use", "governance"} {
		if !strings.Contains(got, want) {
			t.Errorf("DutyConflictError.Error() = %q, missing %q", got, want)
		}
	}
}
