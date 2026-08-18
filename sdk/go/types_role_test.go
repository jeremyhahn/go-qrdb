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

package sdk

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// legacyAdminRoleIDAcme is the identifier the pre-FIPS UUIDv5 (SHA-1)
// derivation produced for tenant "acme", role "admin". Frozen literal, not
// recomputed: SHA-1 panics under GODEBUG=fips140=only.
const legacyAdminRoleIDAcme = "a2dcd47f-c404-52c6-a1c2-b2ee93eb2da6"

// legacyRoleStore answers as a cluster seeded before the migration would.
type legacyRoleStore struct{}

func (legacyRoleStore) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if name != role.RoleNameAdmin {
		return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
	}
	return &role.Role{ID: legacyAdminRoleIDAcme, Name: name, TenantID: tenantID, System: true}, nil
}

func TestSystemRoleID_DerivesUUIDv8(t *testing.T) {
	got := SystemRoleID("acme", role.RoleNameAdmin)

	parsed, err := uuid.Parse(got)
	if err != nil {
		t.Fatalf("%q is not a parseable UUID: %v", got, err)
	}
	if parsed.Version() != 8 {
		t.Errorf("version = %d, want 8 (RFC 9562 §5.8)", parsed.Version())
	}
	if len(got) != 36 {
		t.Errorf("length = %d, want 36 (shape must be unchanged for consumers)", len(got))
	}
	if got == legacyAdminRoleIDAcme {
		t.Errorf("still deriving the legacy UUIDv5 identifier %s", legacyAdminRoleIDAcme)
	}
}

func TestResolveSystemRoleID_ExposesTheLegacyIdentifierToConsumers(t *testing.T) {
	// The seam a consumer needs so a grant binds to the role that exists on
	// an already-provisioned cluster rather than to a derived identifier.
	got, err := ResolveSystemRoleID(context.Background(), legacyRoleStore{}, "acme", role.RoleNameAdmin)
	if err != nil {
		t.Fatalf("ResolveSystemRoleID: %v", err)
	}
	if got != legacyAdminRoleIDAcme {
		t.Errorf("ResolveSystemRoleID = %s, want the persisted %s", got, legacyAdminRoleIDAcme)
	}

	// With no store to consult, the derived identifier is all there is.
	derived, err := ResolveSystemRoleID(context.Background(), nil, "acme", role.RoleNameAdmin)
	if err != nil {
		t.Fatalf("ResolveSystemRoleID (nil resolver): %v", err)
	}
	if want := SystemRoleID("acme", role.RoleNameAdmin); derived != want {
		t.Errorf("ResolveSystemRoleID = %s, want %s", derived, want)
	}
}

func TestSystemRoleIDs_ExposesBothFormsToConsumers(t *testing.T) {
	ids, err := SystemRoleIDs(context.Background(), legacyRoleStore{}, "acme", role.RoleNameAdmin)
	if err != nil {
		t.Fatalf("SystemRoleIDs: %v", err)
	}
	canonical := SystemRoleID("acme", role.RoleNameAdmin)
	if len(ids) != 2 || ids[0] != canonical || ids[1] != legacyAdminRoleIDAcme {
		t.Fatalf("SystemRoleIDs = %v, want [%s %s]", ids, canonical, legacyAdminRoleIDAcme)
	}

	// A RoleNameResolver is what the SDK asks consumers for.
	var _ RoleNameResolver = legacyRoleStore{}
}
