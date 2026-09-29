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

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// ValidateAssignable resolves roleID and confirms it may be granted within
// tenantID: the role must belong to tenantID itself. That holds for the
// built-in roles too -- every tenant's ReconcileBuiltInRoles seeds its own
// copy under its own identifier, and the role store resolves an id only in the
// tenant that owns it, so another tenant's role (the system tenant's built-ins
// included) is one the authorizer cannot resolve for a principal of tenantID.
// Granting it would leave every later decision for that principal failing as
// unavailable, not merely deny the one grant.
//
// A role that does not exist is a *ReferenceError wrapping the lookup's
// *RoleNotFoundError; any other lookup failure is returned verbatim. A role
// that exists in another tenant is a *NotAssignableError.
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
	if tenantid.Normalize(r.TenantID) == tenantid.Normalize(tenantID) {
		return nil
	}
	return &NotAssignableError{RoleID: roleID, TenantID: tenantID}
}
