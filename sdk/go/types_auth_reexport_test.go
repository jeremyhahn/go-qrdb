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

package sdk_test

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// Compile-time: the permitted-method surface crosses the SDK boundary as
// aliases of the canonical declarations, not as copies. A copy of the error
// type would compile here but fail these assignments.
var (
	_ func(*sdk.User, string) error       = sdk.CheckAuthMethod
	_ *authmw.AuthMethodNotPermittedError = (*sdk.AuthMethodNotPermittedError)(nil)
	_ error                               = (*sdk.AuthMethodNotPermittedError)(nil)
)

// TestSDK_AuthMethodTokensAreTheCanonicalOnes pins the tokens to the values
// the server compares against, and proves each is already in the normalized
// form the store writes — a token that normalization rewrote would never match
// a stored policy entry spelled with it.
func TestSDK_AuthMethodTokensAreTheCanonicalOnes(t *testing.T) {
	cases := []struct {
		sdkValue, pkgValue, want string
	}{
		{sdk.MethodMTLS, auth.MethodMTLS, "mtls"},
		{sdk.MethodPIV, auth.MethodPIV, "piv"},
		{sdk.MethodWebAuthn, auth.MethodWebAuthn, "webauthn"},
	}
	seen := make(map[string]bool, len(cases))
	for _, tc := range cases {
		if tc.sdkValue != tc.pkgValue || tc.sdkValue != tc.want {
			t.Errorf("sdk=%q pkg=%q, want %q", tc.sdkValue, tc.pkgValue, tc.want)
		}
		if seen[tc.sdkValue] {
			t.Errorf("token %q names two methods", tc.sdkValue)
		}
		seen[tc.sdkValue] = true

		normalized, err := sdk.NormalizeAuthMethods([]string{tc.sdkValue})
		if err != nil || len(normalized) != 1 || normalized[0] != tc.sdkValue {
			t.Errorf("NormalizeAuthMethods(%q) = %v, %v; want it unchanged", tc.sdkValue, normalized, err)
		}
	}
}

// TestSDK_CheckAuthMethod_EnforcesThePolicy is the consumer-side behaviour: a
// PIV ceremony layer enforces an account's policy through the SDK alone, and
// tells a refusal apart with errors.As on the re-exported type.
func TestSDK_CheckAuthMethod_EnforcesThePolicy(t *testing.T) {
	u := &sdk.User{Username: "erin", TenantID: "t1", AuthMethods: []string{sdk.MethodPIV}}

	if err := sdk.CheckAuthMethod(u, sdk.MethodPIV); err != nil {
		t.Fatalf("PIV refused against a PIV-only policy: %v", err)
	}

	err := sdk.CheckAuthMethod(u, sdk.MethodMTLS)
	var refused *sdk.AuthMethodNotPermittedError
	if !errors.As(err, &refused) {
		t.Fatalf("mTLS against a PIV-only policy: got %T (%v), want *AuthMethodNotPermittedError", err, err)
	}
	if refused.Method != sdk.MethodMTLS || refused.Username != "erin" {
		t.Errorf("refusal = %+v, want method mtls for erin", refused)
	}

	if err := sdk.CheckAuthMethod(&sdk.User{Username: "erin"}, sdk.MethodMTLS); err != nil {
		t.Errorf("an empty policy states no restriction, got %v", err)
	}
}
