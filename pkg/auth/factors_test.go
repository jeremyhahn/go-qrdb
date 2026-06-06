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

package auth

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// AuthFactorType
// ---------------------------------------------------------------------------

func TestAuthFactorType_IsValid_ok(t *testing.T) {
	cases := []AuthFactorType{AuthFactorMTLS, AuthFactorFIDO2, AuthFactorPIV}
	for _, c := range cases {
		if !c.IsValid() {
			t.Fatalf("IsValid(%q) = false, want true", c)
		}
		if c.String() != string(c) {
			t.Fatalf("String(%q) = %q, want %q", c, c.String(), string(c))
		}
	}
}

func TestAuthFactorType_IsValid_unknown(t *testing.T) {
	cases := []AuthFactorType{"", "totp", "password", "MTLS", "FIDO2"}
	for _, c := range cases {
		if c.IsValid() {
			t.Fatalf("IsValid(%q) = true, want false", c)
		}
	}
}

// ---------------------------------------------------------------------------
// AuthFactor.IsActive / Clone
// ---------------------------------------------------------------------------

func TestAuthFactor_IsActive_active(t *testing.T) {
	f := &AuthFactor{ID: "x"}
	if !f.IsActive() {
		t.Fatal("IsActive: nil RevokedAt should be active")
	}
}

func TestAuthFactor_IsActive_revoked(t *testing.T) {
	now := time.Now().UTC()
	f := &AuthFactor{ID: "x", RevokedAt: &now}
	if f.IsActive() {
		t.Fatal("IsActive: non-nil RevokedAt should be inactive")
	}
}

func TestAuthFactor_IsActive_nil(t *testing.T) {
	var f *AuthFactor
	if f.IsActive() {
		t.Fatal("IsActive: nil receiver should return false")
	}
}

func TestAuthFactor_Clone_deepCopy(t *testing.T) {
	now := time.Now().UTC()
	src := &AuthFactor{
		ID:           "abc",
		UserID:       42,
		Type:         AuthFactorMTLS,
		Credential:   []byte{1, 2, 3},
		Metadata:     map[string]string{"k": "v"},
		RegisteredAt: now,
		LastUsedAt:   &now,
		RevokedAt:    &now,
	}
	dst := src.Clone()
	if dst == src {
		t.Fatal("Clone returned same pointer")
	}
	dst.Credential[0] = 9
	if src.Credential[0] == 9 {
		t.Fatal("Clone did not deep-copy Credential")
	}
	dst.Metadata["k"] = "vv"
	if src.Metadata["k"] == "vv" {
		t.Fatal("Clone did not deep-copy Metadata")
	}
}

func TestAuthFactor_Clone_nil(t *testing.T) {
	var src *AuthFactor
	if src.Clone() != nil {
		t.Fatal("Clone(nil) should return nil")
	}
}

// ---------------------------------------------------------------------------
// Errors: type assertions / unwrap chains
// ---------------------------------------------------------------------------

func TestFactorNotFoundError_unwrap(t *testing.T) {
	err := &FactorNotFoundError{FactorID: "abc"}
	if !errors.Is(err, ErrFactorNotFound) {
		t.Fatal("FactorNotFoundError should wrap ErrFactorNotFound")
	}
	if err.Error() == "" {
		t.Fatal("FactorNotFoundError.Error returned empty")
	}
}

func TestFactorRevokedError_unwrap(t *testing.T) {
	err := &FactorRevokedError{FactorID: "abc"}
	if !errors.Is(err, ErrFactorRevoked) {
		t.Fatal("FactorRevokedError should wrap ErrFactorRevoked")
	}
}

func TestLastFactorError_unwrap(t *testing.T) {
	err := &LastFactorError{UserID: 1, FactorID: "x"}
	if !errors.Is(err, ErrLastFactor) {
		t.Fatal("LastFactorError should wrap ErrLastFactor")
	}
}

func TestUserHasNoFactorsError_unwrap(t *testing.T) {
	err := &UserHasNoFactorsError{UserID: 1}
	if !errors.Is(err, ErrUserHasNoFactors) {
		t.Fatal("UserHasNoFactorsError should wrap ErrUserHasNoFactors")
	}
}

func TestFactorTypeUnsupportedError_unwrap(t *testing.T) {
	err := &FactorTypeUnsupportedError{Type: "bogus"}
	if !errors.Is(err, ErrFactorTypeUnsupported) {
		t.Fatal("FactorTypeUnsupportedError should wrap ErrFactorTypeUnsupported")
	}
}

func TestFactorAlreadyExistsError_unwrap(t *testing.T) {
	err := &FactorAlreadyExistsError{Type: AuthFactorMTLS, FactorID: "x"}
	if !errors.Is(err, ErrFactorAlreadyExists) {
		t.Fatal("FactorAlreadyExistsError should wrap ErrFactorAlreadyExists")
	}
}

func TestInvalidFactorError_unwrap(t *testing.T) {
	err := &InvalidFactorError{Reason: "missing"}
	if !errors.Is(err, ErrInvalidFactor) {
		t.Fatal("InvalidFactorError should wrap ErrInvalidFactor")
	}
}

// ---------------------------------------------------------------------------
// CredentialKey helpers
// ---------------------------------------------------------------------------

func TestCredentialKey_deterministic(t *testing.T) {
	a := CredentialKey([]byte("hello"))
	b := CredentialKey([]byte("hello"))
	if string(a) != string(b) {
		t.Fatal("CredentialKey is not deterministic")
	}
	if len(a) != 32 {
		t.Fatalf("CredentialKey length = %d, want 32", len(a))
	}
}

func TestCredentialKey_differentInputs(t *testing.T) {
	a := CredentialKey([]byte("alpha"))
	b := CredentialKey([]byte("beta"))
	if string(a) == string(b) {
		t.Fatal("CredentialKey collision on distinct inputs")
	}
}

func TestCredentialKeyHex_format(t *testing.T) {
	h := CredentialKeyHex([]byte("hello"))
	if len(h) != 64 {
		t.Fatalf("CredentialKeyHex length = %d, want 64", len(h))
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("CredentialKeyHex contains non-hex char %q", c)
		}
	}
}
