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

package sdk

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
)

// hardwarelessFactor stands in for an out-of-repo Factor implementation
// (go-xkms ships fido2, mtls, piv and pkcs11 ones). qrdb ships none, so this
// is the only way to exercise the seam from the consumer's side: it constructs
// the error through the SDK surface exactly as a real factor would, using no
// pkg/ import of its own.
type hardwarelessFactor struct{ sentinel bool }

func (f *hardwarelessFactor) Verify(_ context.Context, _ *AuthFactorChallenge, _ *AuthFactorResponse) error {
	if f.sentinel {
		return ErrAuthFactorHardwareUnavailable
	}
	return &AuthFactorHardwareUnavailableError{Factor: "pkcs11", Reason: "no reader attached"}
}

// TestAuthFactorHardwareUnavailable_ConstructibleThroughTheSDK pins the fix for
// go-qrdb#243: both transports map a hardware-unavailable condition to 503, but
// the type and its sentinel were the only members of the Factor seam's error
// contract with no sdk/go surface, so no out-of-repo factor could construct the
// error the handler was waiting for. The arm was unreachable in deployment, not
// merely unproduced in this repo.
//
// The assertion is that a value built through the SDK alias is matchable as the
// pkg/authfactors type, because that is what the transports' errors.As targets
// are written against. An alias that resolved to a different type would compile
// and still leave the handler dead.
func TestAuthFactorHardwareUnavailable_ConstructibleThroughTheSDK(t *testing.T) {
	t.Run("struct form", func(t *testing.T) {
		err := (&hardwarelessFactor{}).Verify(context.Background(), nil, nil)

		var target *authfactors.HardwareUnavailableError
		if !errors.As(err, &target) {
			t.Fatalf("an error built through the SDK alias must match the type the "+
				"transports assert on, got %T: %v", err, err)
		}
		if target.Factor != "pkcs11" || target.Reason != "no reader attached" {
			t.Errorf("fields did not survive the alias: got Factor=%q Reason=%q",
				target.Factor, target.Reason)
		}
	})

	t.Run("sentinel form", func(t *testing.T) {
		err := (&hardwarelessFactor{sentinel: true}).Verify(context.Background(), nil, nil)

		if !errors.Is(err, authfactors.ErrHardwareUnavailable) {
			t.Fatalf("the SDK sentinel must be the one the transports match, got %v", err)
		}
	})
}

// TestAuthFactorSeamErrorsAreAllExported guards the gap that #243 found: three
// of the four errors on the Factor seam had an SDK surface and one did not, and
// nothing failed when the fourth was missing. This enumerates the seam so a
// future addition that forgets its alias is a compile error here rather than a
// silently unreachable handler arm in both transports.
func TestAuthFactorSeamErrorsAreAllExported(t *testing.T) {
	var (
		_ *AuthFactorEnrollmentInvalidError   = (*authfactors.EnrollmentInvalidError)(nil)
		_ *AuthFactorChallengeExpiredError    = (*authfactors.ChallengeExpiredError)(nil)
		_ *AuthFactorVerificationFailedError  = (*authfactors.VerificationFailedError)(nil)
		_ *AuthFactorHardwareUnavailableError = (*authfactors.HardwareUnavailableError)(nil)
	)
	if ErrAuthFactorHardwareUnavailable == nil {
		t.Fatal("ErrAuthFactorHardwareUnavailable must alias the pkg sentinel")
	}
}
