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
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ValidateAssignable resolves roleID and confirms it may be granted within
// tenantID: either the role was created in tenantID, or it is one of the
// system tenant's built-in roles, which every tenant's ReconcileBuiltInRoles
// seeds under its OWN identifier but which the system tenant's own copy is
// treated as valid everywhere (the same "system tenant is structurally
// privileged" rule permission.ScopeSelf already applies to the identity
// checking this).
//
// A role that does not exist is a *ReferenceError wrapping the lookup's
// *RoleNotFoundError; any other lookup failure is returned verbatim. A role
// that exists but belongs to a tenant that is neither tenantID nor the system
// tenant is a *NotAssignableError.
func ValidateAssignable(ctx context.Context, svc RoleServicer, roleID, tenantID string) error {
	r, err := svc.GetRole(ctx, roleID)
	var notFound *RoleNotFoundError
	if errors.As(err, &notFound) {
		return &ReferenceError{RoleID: roleID, Err: err}
	}
	if err != nil {
		return err
	}
	if r == nil {
		// A nil role with a nil error breaks the RoleServicer contract; the
		// reference is unresolved, so it is refused rather than dereferenced.
		return &ReferenceError{RoleID: roleID, Err: &RoleNotFoundError{ID: roleID}}
	}
	if r.TenantID == tenantID {
		return nil
	}
	if r.System && r.TenantID == tenant.SystemTenantID {
		return nil
	}
	return &NotAssignableError{RoleID: roleID, TenantID: tenantID}
}
