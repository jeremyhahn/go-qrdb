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

package tenant

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

const testClusterTrustDomain = "cluster.example.com"

func TestValidateTenantIDTrustDomain_AcceptsATrustDomainName(t *testing.T) {
	for _, id := range []string{"acme", "acme-corp", "tenant_1", "acme.example.com", "a"} {
		if err := ValidateTenantIDTrustDomain(id, testClusterTrustDomain); err != nil {
			t.Errorf("ValidateTenantIDTrustDomain(%q) = %v, want nil", id, err)
		}
	}
}

// SPIFFE-ID §2.1 admits only lowercase letters, digits, '.', '-' and '_' in a
// trust domain, so any other ID would not survive a round trip through the
// SAN its certificates carry.
func TestValidateTenantIDTrustDomain_RefusesAnIDThatDoesNotRoundTrip(t *testing.T) {
	tests := []struct {
		id   string
		want error
	}{
		{"", spiffe.ErrTrustDomainEmpty},
		{"Acme", spiffe.ErrTrustDomainInvalid},
		{"acme corp", spiffe.ErrTrustDomainInvalid},
		{"acme/other", spiffe.ErrTrustDomainInvalid},
		{"acme:8443", spiffe.ErrTrustDomainInvalid},
		{"user@acme", spiffe.ErrTrustDomainInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			err := ValidateTenantIDTrustDomain(tt.id, testClusterTrustDomain)
			var tdErr *TenantTrustDomainError
			if !errors.As(err, &tdErr) || tdErr.TenantID != tt.id {
				t.Fatalf("ValidateTenantIDTrustDomain(%q) = %v, want *TenantTrustDomainError", tt.id, err)
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateTenantIDTrustDomain(%q) = %v, want it to wrap %v", tt.id, err, tt.want)
			}
		})
	}
}

// The cluster's own identities live under the cluster trust domain; a tenant
// of the same name would share their SPIFFE namespace.
func TestValidateTenantIDTrustDomain_RefusesTheClusterTrustDomain(t *testing.T) {
	err := ValidateTenantIDTrustDomain(testClusterTrustDomain, testClusterTrustDomain)
	var tdErr *TenantTrustDomainError
	if !errors.As(err, &tdErr) || tdErr.TenantID != testClusterTrustDomain {
		t.Fatalf("ValidateTenantIDTrustDomain = %v, want *TenantTrustDomainError", err)
	}
	if !errors.Is(err, ErrTenantIDIsClusterTrustDomain) {
		t.Fatalf("ValidateTenantIDTrustDomain = %v, want it to wrap ErrTenantIDIsClusterTrustDomain", err)
	}
}

func TestValidateTenantIDTrustDomain_OnlyTheExactClusterTrustDomainIsRefused(t *testing.T) {
	for _, id := range []string{"tenant.cluster.example.com", "cluster.example"} {
		if err := ValidateTenantIDTrustDomain(id, testClusterTrustDomain); err != nil {
			t.Errorf("ValidateTenantIDTrustDomain(%q) = %v, want nil", id, err)
		}
	}
}

func TestTenantTrustDomainError_ErrorNamesTheIDAndTheCause(t *testing.T) {
	err := &TenantTrustDomainError{TenantID: "Acme", Err: spiffe.ErrTrustDomainInvalid}
	msg := err.Error()
	if !strings.HasPrefix(msg, "tenant: ") || !strings.Contains(msg, `"Acme"`) || !strings.Contains(msg, spiffe.ErrTrustDomainInvalid.Error()) {
		t.Fatalf("Error() = %q, want the tenant prefix, the quoted ID and the cause", msg)
	}
}

func TestTenantTrustDomainError_UnwrapReturnsTheCause(t *testing.T) {
	err := &TenantTrustDomainError{TenantID: testClusterTrustDomain, Err: ErrTenantIDIsClusterTrustDomain}
	if !errors.Is(err.Unwrap(), ErrTenantIDIsClusterTrustDomain) {
		t.Fatalf("Unwrap() = %v, want ErrTenantIDIsClusterTrustDomain", err.Unwrap())
	}
	if errors.Is(err, spiffe.ErrTrustDomainInvalid) {
		t.Fatal("errors.Is matched a cause the error does not carry")
	}
}
