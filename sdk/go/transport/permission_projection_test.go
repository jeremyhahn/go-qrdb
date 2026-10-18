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

package transport

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ScopeFromDomain maps every defined scope to its own SDK value. The
// tenant-only case is the one that matters: it used to fall into a default
// branch and cross the boundary as "self".
func TestScopeFromDomain_EveryDefinedScope(t *testing.T) {
	cases := []struct {
		in   permission.Scope
		want PermissionScope
	}{
		{permission.ScopeSelf, PermissionScopeSelf},
		{permission.ScopeCross, PermissionScopeCross},
		{permission.ScopeTenantOnly, PermissionScopeTenantOnly},
		{permission.ScopeSelfStrict, PermissionScopeSelfStrict},
	}
	for _, tc := range cases {
		got, err := ScopeFromDomain(tc.in)
		if err != nil {
			t.Fatalf("ScopeFromDomain(%s): unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ScopeFromDomain(%s) = %q, want %q", tc.in, got, tc.want)
		}
		if string(got) != tc.in.String() {
			t.Errorf("SDK label %q must equal the domain label %q", got, tc.in)
		}
	}
}

// An out-of-range Scope is an error, not PermissionScopeSelf.
func TestScopeFromDomain_UnknownIsAnError(t *testing.T) {
	got, err := ScopeFromDomain(99)
	if err == nil {
		t.Fatalf("expected an error for an undefined scope, got %q", got)
	}
	var invalid *permission.InvalidScopeError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected *permission.InvalidScopeError, got %T: %v", err, err)
	}
	if got != "" {
		t.Errorf("scope must be zero on error, got %q", got)
	}
}

func TestPermissionInfoFromDomain_RegisteredPermission(t *testing.T) {
	got, err := PermissionInfoFromDomain(permission.PermissionsResolve)
	if err != nil {
		t.Fatalf("PermissionInfoFromDomain: %v", err)
	}
	scope, err := permission.LookupScope(permission.PermissionsResolve)
	if err != nil {
		t.Fatalf("LookupScope: %v", err)
	}
	want := PermissionInfo{Name: "permissions:resolve", Resource: "permissions", Action: "resolve", Scope: PermissionScope(scope.String())}
	if got != want {
		t.Fatalf("PermissionInfoFromDomain = %+v, want %+v", got, want)
	}
}

// An unregistered permission has no declared scope: the lookup's error
// propagates rather than defaulting to a scope.
func TestPermissionInfoFromDomain_UnregisteredIsAnError(t *testing.T) {
	got, err := PermissionInfoFromDomain(permission.Permission("nosuch:verb"))
	if err == nil {
		t.Fatalf("expected an error for an unregistered permission, got %+v", got)
	}
	if got != (PermissionInfo{}) {
		t.Errorf("info must be zero on error, got %+v", got)
	}
}
