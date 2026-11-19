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

package quota

import (
	"strings"
	"testing"
)

// --- ExceededError ---

func TestExceededErrorMessageContainsTenantID(t *testing.T) {
	err := &ExceededError{
		TenantID: "acme",
		Resource: ResourceKeys,
		Current:  100,
		Limit:    100,
	}
	got := err.Error()
	if !strings.Contains(got, "acme") {
		t.Errorf("ExceededError.Error() = %q, want it to contain tenant ID %q", got, "acme")
	}
}

func TestExceededErrorMessageContainsResourceAndCounts(t *testing.T) {
	err := &ExceededError{
		TenantID: "tenant1",
		Resource: ResourceDatabases,
		Current:  5,
		Limit:    5,
	}
	got := err.Error()
	if !strings.Contains(got, "databases") {
		t.Errorf("ExceededError.Error() = %q, want it to contain %q", got, "databases")
	}
	if !strings.Contains(got, "5") {
		t.Errorf("ExceededError.Error() = %q, want it to contain limit/current %q", got, "5")
	}
	if !strings.Contains(got, "quota") {
		t.Errorf("ExceededError.Error() = %q, want it to contain %q", got, "quota")
	}
}

func TestExceededErrorMessageContainsExceededPrefix(t *testing.T) {
	err := &ExceededError{
		TenantID: "t",
		Resource: ResourceUsers,
		Current:  10,
		Limit:    10,
	}
	got := err.Error()
	if !strings.Contains(got, "exceeded") {
		t.Errorf("ExceededError.Error() = %q, want it to contain %q", got, "exceeded")
	}
}

// --- InvalidResourceError ---

func TestInvalidResourceErrorMessageContainsResourceID(t *testing.T) {
	err := &InvalidResourceError{Resource: Resource(99)}
	got := err.Error()
	if !strings.Contains(got, "99") {
		t.Errorf("InvalidResourceError.Error() = %q, want it to contain %q", got, "99")
	}
}

func TestInvalidResourceErrorMessageContainsUnknown(t *testing.T) {
	err := &InvalidResourceError{Resource: Resource(42)}
	got := err.Error()
	if !strings.Contains(got, "unknown") {
		t.Errorf("InvalidResourceError.Error() = %q, want it to contain %q", got, "unknown")
	}
	if !strings.Contains(got, "quota") {
		t.Errorf("InvalidResourceError.Error() = %q, want it to contain %q", got, "quota")
	}
}

// --- UnregisteredCounterError ---

func TestUnregisteredCounterErrorMessageContainsResource(t *testing.T) {
	err := &UnregisteredCounterError{Resource: ResourceBackups}
	got := err.Error()
	if !strings.Contains(got, "backups") {
		t.Errorf("UnregisteredCounterError.Error() = %q, want it to contain %q", got, "backups")
	}
}

func TestUnregisteredCounterErrorMessageContainsPrefix(t *testing.T) {
	err := &UnregisteredCounterError{Resource: ResourceRoles}
	got := err.Error()
	if !strings.Contains(got, "quota") {
		t.Errorf("UnregisteredCounterError.Error() = %q, want it to contain %q", got, "quota")
	}
	if !strings.Contains(got, "counter") {
		t.Errorf("UnregisteredCounterError.Error() = %q, want it to contain %q", got, "counter")
	}
}

// --- ErrNilChecker sentinel ---

func TestErrNilCheckerMessageContainsQuota(t *testing.T) {
	got := ErrNilChecker.Error()
	if !strings.Contains(got, "quota") {
		t.Errorf("ErrNilChecker.Error() = %q, want it to contain %q", got, "quota")
	}
}
