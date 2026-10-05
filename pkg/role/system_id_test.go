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

package role

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ---------------------------------------------------------------------------
// Derivation: RFC 9562 §5.8 UUIDv8 over SHA-256
// ---------------------------------------------------------------------------

func TestSystemRoleID_IsUUIDv8(t *testing.T) {
	for _, name := range []string{RoleNameSO, RoleNameAdmin, RoleNameOperator, RoleNameUser, RoleNameAuditor, RoleNameOwner} {
		got := SystemRoleID(tenantid.System, name)

		parsed, err := uuid.Parse(got)
		if err != nil {
			t.Fatalf("%s: not a parseable UUID: %v", name, err)
		}
		if parsed.Version() != 8 {
			t.Errorf("%s: version = %d, want 8 (RFC 9562 §5.8)", name, parsed.Version())
		}
		if parsed.Variant() != uuid.RFC4122 {
			t.Errorf("%s: variant = %v, want RFC4122", name, parsed.Variant())
		}
		if len(got) != 36 {
			t.Errorf("%s: length = %d, want 36 (shape must be unchanged)", name, len(got))
		}
	}
}

func TestSystemRoleID_DeterministicAndDistinct(t *testing.T) {
	first := SystemRoleID("acme", RoleNameAdmin)
	if second := SystemRoleID("acme", RoleNameAdmin); first != second {
		t.Errorf("not deterministic: %s != %s", first, second)
	}
	if other := SystemRoleID("acme", RoleNameOperator); first == other {
		t.Errorf("admin and operator collide on %s", first)
	}
	if otherTenant := SystemRoleID("acme2", RoleNameAdmin); first == otherTenant {
		t.Errorf("tenants acme and acme2 collide on %s", first)
	}
}

func TestSystemRoleID_EmptyTenantDefaultsToSystem(t *testing.T) {
	if got, want := SystemRoleID("", RoleNameAdmin), SystemRoleID(tenantid.System, RoleNameAdmin); got != want {
		t.Errorf("empty tenant = %s, want %s", got, want)
	}
}

func TestSystemRoleID_LengthPrefixIsInjective(t *testing.T) {
	// Without length framing, ("a/b", "c") and ("a", "b/c") would hash the
	// same bytes and two distinct roles would share one identifier.
	if a, b := SystemRoleID("a/b", "c"), SystemRoleID("a", "b/c"); a == b {
		t.Errorf(`SystemRoleID("a/b","c") and SystemRoleID("a","b/c") both = %s`, a)
	}
}

// ---------------------------------------------------------------------------
// Resolver contract violations
// ---------------------------------------------------------------------------

func TestInvalidResolverResultError_Message(t *testing.T) {
	err := &InvalidResolverResultError{Name: RoleNameSO, TenantID: "acme"}
	msg := err.Error()
	if !strings.Contains(msg, RoleNameSO) || !strings.Contains(msg, "acme") {
		t.Errorf("Error() = %q, want it to name both the role and the tenant", msg)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// nilNameResolver is a NameResolver that breaks its contract by reporting
// neither a role nor an error.
type nilNameResolver struct{}

func (nilNameResolver) GetRoleByName(_ context.Context, _, _ string) (*Role, error) {
	return nil, nil
}

// errNameResolver is a NameResolver whose every lookup fails.
type errNameResolver struct{ err error }

func (e errNameResolver) GetRoleByName(_ context.Context, _, _ string) (*Role, error) {
	return nil, e.err
}
