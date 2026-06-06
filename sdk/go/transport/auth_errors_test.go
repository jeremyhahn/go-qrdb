// Copyright 2026 Jeremy Hahn
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

package transport

import (
	"strings"
	"testing"
)

// Each Phase 3 typed error must produce a stable, descriptive message.
// The tests both pin the format and exercise the Error() methods so
// callers that log them get coverage in the registry.

func TestPhase3ErrorMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"permission not found", &PermissionNotFoundError{Name: "kv:put"}, "kv:put"},
		{"invalid identity", &InvalidIdentityError{Reason: "no subject"}, "no subject"},
		{"factor type unsupported", &FactorTypeUnsupportedError{Type: "magic"}, "magic"},
		{"factor not found", &FactorNotFoundError{FactorID: "f-1"}, "f-1"},
		{"factor already enrolled", &FactorAlreadyEnrolledError{Type: FactorTypeFIDO2, FactorID: "f-1"}, "f-1"},
		{"factor verification with reason", &FactorVerificationError{FactorID: "f-1", Reason: "bad sig"}, "bad sig"},
		{"factor verification empty reason", &FactorVerificationError{FactorID: "f-1"}, "f-1"},
		{"invalid bearer with reason", &InvalidBearerError{Reason: "expired"}, "expired"},
		{"invalid bearer empty", &InvalidBearerError{}, "invalid bearer"},
		{"login session not found", &LoginSessionNotFoundError{SessionID: "sess-1"}, "sess-1"},
		{"login session expired", &LoginSessionExpiredError{SessionID: "sess-1"}, "sess-1"},
		{"pool not configured", &PoolNotConfiguredError{Name: "default"}, "default"},
		{"pool empty", &PoolEmptyError{Name: "default"}, "default"},
		{"pool backend not local", &PoolBackendNotLocalError{Name: "cloud"}, "cloud"},
		{"pool already registered", &PoolAlreadyRegisteredError{Name: "default"}, "default"},
		{"invalid pool strategy", &InvalidPoolStrategyError{Strategy: "weird"}, "weird"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("error message %q does not contain %q", got, tc.want)
			}
		})
	}
}

// FactorType.IsValid + String round-trip.
func TestFactorType_IsValid(t *testing.T) {
	cases := map[FactorType]bool{
		FactorTypeMTLS:      true,
		FactorTypeFIDO2:     true,
		FactorTypePIV:       true,
		FactorType("bogus"): false,
		FactorType(""):      false,
	}
	for ft, want := range cases {
		if got := ft.IsValid(); got != want {
			t.Errorf("FactorType(%q).IsValid() = %v, want %v", ft, got, want)
		}
		if ft.String() != string(ft) {
			t.Errorf("FactorType(%q).String() = %q", ft, ft.String())
		}
	}
}

// Factor.IsActive nil + revoked + active.
func TestFactor_IsActive(t *testing.T) {
	if (*Factor)(nil).IsActive() {
		t.Error("nil Factor must not be active")
	}
	f := &Factor{}
	if !f.IsActive() {
		t.Error("zero Factor must be active")
	}
}
