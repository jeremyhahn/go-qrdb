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
	"slices"
	"testing"
)

func TestNewServicePrincipal_CarriesExactlyItsGrants(t *testing.T) {
	perms := []string{"kv:read", "kv:write"}
	id := NewServicePrincipal("xkms", "system", perms)

	if id.Subject != "qrdb-service:xkms" || id.TenantID != "system" {
		t.Fatalf("identity = %+v, want subject qrdb-service:xkms in tenant system", id)
	}
	if !slices.Equal(id.ScopePermissions, perms) {
		t.Fatalf("ScopePermissions = %v, want %v", id.ScopePermissions, perms)
	}
	if len(id.Roles) != 0 || len(id.RoleBindings) != 0 {
		t.Fatalf("service principal carries roles %v / bindings %v, want none", id.Roles, id.RoleBindings)
	}
	if id.Metadata[ServicePrincipalMetadataKey] != "xkms" {
		t.Fatalf("Metadata = %v, want the principal's name under %q", id.Metadata, ServicePrincipalMetadataKey)
	}

	// The caller's slice is copied: a later write to it grants nothing.
	perms[0] = "admin:status"
	if id.ScopePermissions[0] != "kv:read" {
		t.Fatalf("ScopePermissions aliases the caller's slice: %v", id.ScopePermissions)
	}
}

func TestIsServicePrincipal(t *testing.T) {
	cases := map[string]struct {
		id   *Identity
		want bool
	}{
		"nil":                 {nil, false},
		"constructed":         {NewServicePrincipal("xkms", "system", nil), true},
		"reserved subject":    {&Identity{Subject: ServicePrincipalSubjectPrefix + "x"}, true},
		"marker, any value":   {&Identity{Subject: "alice", Metadata: map[string]string{ServicePrincipalMetadataKey: ""}}, true},
		"ordinary":            {&Identity{Subject: "alice", TenantID: "acme"}, false},
		"prefix without ':'":  {&Identity{Subject: "qrdb-service"}, false},
		"namespace mid-name":  {&Identity{Subject: "x-qrdb-service:y"}, false},
		"other metadata only": {&Identity{Subject: "alice", Metadata: map[string]string{"auth_source": "on_behalf"}}, false},
	}
	for name, tc := range cases {
		if got := IsServicePrincipal(tc.id); got != tc.want {
			t.Errorf("%s: IsServicePrincipal = %v, want %v", name, got, tc.want)
		}
	}
}
