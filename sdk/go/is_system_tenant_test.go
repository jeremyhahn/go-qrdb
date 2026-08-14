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
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// TestSDK_IsSystemTenant proves the string-level system-tenant predicate is
// wired through the SDK boundary. go-xkms and go-quicbaas may only import
// sdk/go — a predicate that exists in pkg/tenant but is not re-exported
// here is invisible to them, which is exactly the failure mode this test
// guards against.
func TestSDK_IsSystemTenant(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"empty resolves to system", "", true},
		{"literal system id matches", sdk.SystemTenantID, true},
		{"ordinary tenant id does not match", "acme-corp", false},
		{"lookalike does not match", "System", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sdk.IsSystemTenant(tc.id); got != tc.want {
				t.Fatalf("sdk.IsSystemTenant(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

// TestSDK_IdentityIsSystemTenant proves the identity-level system-tenant
// predicate is wired through the SDK boundary alongside the string-level
// one.
func TestSDK_IdentityIsSystemTenant(t *testing.T) {
	tests := []struct {
		name     string
		identity *sdk.Identity
		want     bool
	}{
		{"nil identity is system-scoped", nil, true},
		{"empty TenantID resolves to system", &sdk.Identity{Subject: "s", TenantID: ""}, true},
		{"system TenantID matches", &sdk.Identity{Subject: "s", TenantID: sdk.SystemTenantID}, true},
		{"ordinary tenant does not match", &sdk.Identity{Subject: "s", TenantID: "acme-corp"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sdk.IdentityIsSystemTenant(tc.identity); got != tc.want {
				t.Fatalf("sdk.IdentityIsSystemTenant(%+v) = %v, want %v", tc.identity, got, tc.want)
			}
		})
	}
}
