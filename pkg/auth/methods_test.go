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

package auth

import (
	"slices"
	"testing"
)

func TestBuiltinAuthMethods(t *testing.T) {
	if got, want := BuiltinAuthMethods(), []string{MethodMTLS, MethodPIV, MethodWebAuthn}; !slices.Equal(got, want) {
		t.Fatalf("BuiltinAuthMethods() = %v, want %v", got, want)
	}
	// The result is a copy: a caller editing it cannot widen the set.
	got := BuiltinAuthMethods()
	got[0] = "password"
	if IsBuiltinAuthMethod("password") {
		t.Fatal("editing BuiltinAuthMethods()'s result widened the built-in set")
	}
}

func TestIsBuiltinAuthMethod(t *testing.T) {
	for _, known := range []string{MethodMTLS, MethodPIV, MethodWebAuthn} {
		if !IsBuiltinAuthMethod(known) {
			t.Errorf("IsBuiltinAuthMethod(%q) = false", known)
		}
	}
	// Exact comparison: canonicalization is the caller's step, and "fido2" is
	// a factor type, not a method.
	for _, unknown := range []string{"", "MTLS", " piv", "fido2", "totp", "oauth"} {
		if IsBuiltinAuthMethod(unknown) {
			t.Errorf("IsBuiltinAuthMethod(%q) = true", unknown)
		}
	}
}

func TestCanonicalAuthMethod_FoldsWithoutValidating(t *testing.T) {
	for in, want := range map[string]string{" MTLS ": "mtls", "WebAuthn": "webauthn", "Legacy-OTP": "legacy-otp", "  ": ""} {
		if got := CanonicalAuthMethod(in); got != want {
			t.Errorf("CanonicalAuthMethod(%q) = %q, want %q", in, got, want)
		}
	}
}
