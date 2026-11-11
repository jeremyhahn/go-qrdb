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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// acmeAdminReadOnly is the ACME-administration / CA-hierarchy read surface —
// the slice every role above User is expected to hold.
var acmeAdminReadOnly = []permission.Permission{
	permission.ACMEAccountsRead, permission.ACMEAccountsList,
	permission.ACMEOrdersRead, permission.ACMEOrdersList,
	permission.ACMEEABRead, permission.ACMEEABList,
	permission.CARead, permission.CAList,
}

// acmeAdminMutating is the ACME-administration / CA-hierarchy write surface —
// the slice reserved for Admin and SO.
var acmeAdminMutating = []permission.Permission{
	permission.ACMEAccountsDisable,
	permission.ACMEEABCreate, permission.ACMEEABDelete,
	permission.CACreate, permission.CAUpdate,
}

// TestBuiltInRoles_ACMEAdminGrantsValidate verifies every ACME-administration
// grant on the built-in roles passes validatePermissions — the same gate
// CreateRole and UpdateRole apply — so the seeded roles are legal.
func TestBuiltInRoles_ACMEAdminGrantsValidate(t *testing.T) {
	all := append(append([]permission.Permission{}, acmeAdminReadOnly...), acmeAdminMutating...)
	if err := validatePermissions(all); err != nil {
		t.Fatalf("validatePermissions: unexpected error %v", err)
	}
}

// TestBuiltInRoles_ACMEAdminRejectsUnregistered is the error-path counterpart:
// a plausible-looking but unregistered near-duplicate of the new vocabulary is
// rejected, so a role can never be seeded with a permission that resolves to
// nothing.
func TestBuiltInRoles_ACMEAdminRejectsUnregistered(t *testing.T) {
	for _, bogus := range []permission.Permission{
		"acme_account:read", // singular resource
		"acme_eab:endorse",  // unregistered action
		"ca:delete",         // unregistered action
		"tenant-ca:create",  // the deleted xkms-local vocabulary
		"certificates:read", // the deleted xkms-local vocabulary
	} {
		if err := validatePermissions([]permission.Permission{bogus}); err == nil {
			t.Errorf("validatePermissions(%q): expected error, got nil", bogus)
		}
	}
}
