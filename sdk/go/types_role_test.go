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

// fakeRoleStore is a minimal RoleNameResolver double, used only to pin the
// type alias at compile time.
type fakeRoleStore struct{}

func (fakeRoleStore) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
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
}

func TestRoleNameResolver_IsNameResolver(t *testing.T) {
	// A RoleNameResolver is what the SDK asks consumers for.
	var _ RoleNameResolver = fakeRoleStore{}
}
