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

package bootstrap

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestValidatePendingAdmin_BoundCredentialIsRefused pins the last two
// refusals: a record that is otherwise the pending admin but already holds a
// bound certificate or an enrolled factor is not an invitation target.
func TestValidatePendingAdmin_BoundCredentialIsRefused(t *testing.T) {
	const tenantID, username, adminRoleID = "acme", "admin", "role-admin"
	pending := func() *user.User {
		return &user.User{ID: 7, TenantID: tenantID, Username: username, RoleIDs: []string{adminRoleID}, Lifecycle: user.LifecyclePending}
	}
	if err := validatePendingAdmin(pending(), tenantID, username, adminRoleID); err != nil {
		t.Fatalf("control: a clean pending admin must validate, got %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*user.User)
	}{
		{name: "bound certificate", mutate: func(u *user.User) { u.CertBindings = []user.CertBinding{{}} }},
		{name: "enrolled factor", mutate: func(u *user.User) { u.Factors = []user.FactorEnrollment{{}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := pending()
			tc.mutate(u)
			var pe *PendingAdminUnresolvableError
			if err := validatePendingAdmin(u, tenantID, username, adminRoleID); !errors.As(err, &pe) {
				t.Fatalf("err = %v, want *PendingAdminUnresolvableError", err)
			}
		})
	}
}
