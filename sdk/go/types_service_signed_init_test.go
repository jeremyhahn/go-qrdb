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
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// A consumer matches the service's signed-init refusals through the SDK
// aliases alone, without importing pkg/tenant.
func TestServiceTenantSignedInitErrors_MatchThroughAliases(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		target any
	}{
		{"signed init", &tenant.SignedInitError{TenantID: "acme", Reason: tenant.SignedInitUnknown}, new(*ServiceTenantSignedInitError)},
		{"field required", &tenant.SignedInitFieldRequiredError{Field: "signature"}, new(*ServiceTenantSignedInitFieldRequiredError)},
		{"too many", &tenant.TooManySignedInitsError{Limit: 1}, new(*ServiceTenantTooManySignedInitsError)},
		{"unsupported", &tenant.SignedInitUnsupportedError{TenantID: "acme"}, new(*ServiceTenantSignedInitUnsupportedError)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("transport: %w", tc.err)
			if !errors.As(wrapped, tc.target) {
				t.Fatalf("errors.As(%v) through the alias = false", tc.err)
			}
		})
	}
}

func TestServiceTenantSignedInitReasons_AreTheServiceValues(t *testing.T) {
	if ServiceTenantSignedInitUnknown != tenant.SignedInitUnknown || ServiceTenantSignedInitExpired != tenant.SignedInitExpired {
		t.Fatal("the re-exported reasons must be the service's values")
	}
	if ServiceTenantSignedInitTTL != tenant.SignedInitTTL || ServiceTenantSignedInitTTL <= 0 {
		t.Fatalf("ServiceTenantSignedInitTTL = %v", ServiceTenantSignedInitTTL)
	}
	var e error = &ServiceTenantSignedInitError{TenantID: "acme", Reason: ServiceTenantSignedInitExpired}
	if e.Error() == "" {
		t.Fatal("a refusal must describe itself")
	}
}
