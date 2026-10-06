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

package service

import (
	"context"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// certTargetGuard refuses certificate issuance and enrolment (SignCertificate,
// EnrollCertificate) for an account holding tenantID's Security Officer or
// Owner role, with exactly one exception: an Owner target is permitted when
// the CALLER authenticated on ctx itself holds tenantID's Owner role. There
// are no other exceptions -- a tenant Admin, who can otherwise act on every
// account in its own tenant (go-qrdb owner ruling: "the tenant admin can do
// everything in its tenant except SO/custodian custody"), stops here.
//
// A key custodian is never a target this guard needs to recognize: a
// custodian authenticates by holder certificate (pkg/holderprofile), never by
// user account, so GetUserByUsername can never resolve one to begin with --
// see pkg/role.BuiltInRoles ("there is no custodian role").
func certTargetGuard(ctx context.Context, tenantID string, target *user.User) error {
	so := role.SystemRoleID(tenantID, role.RoleNameSO)
	owner := role.SystemRoleID(tenantID, role.RoleNameOwner)
	isSO := holdsRoleID(target.RoleIDs, role.RoleNameSO, so)
	isOwner := holdsRoleID(target.RoleIDs, role.RoleNameOwner, owner)
	if !isSO && !isOwner {
		return nil
	}
	if isOwner && !isSO && callerHoldsOwner(ctx, tenantID) {
		return nil
	}
	return &CertTargetRefusedError{TenantID: tenantID, Username: target.Username}
}

// callerHoldsOwner reports whether the identity authenticated on ctx holds
// tenantID's Owner role. Every certificate enrolment verb is tenant-self
// (ScopeSelfStrict), so a genuine caller's identity.TenantID already equals
// tenantID; the comparison is kept explicit rather than assumed, so a nil
// identity -- no authentication middleware configured, or a direct service
// call made with no identity on its context at all -- never qualifies.
func callerHoldsOwner(ctx context.Context, tenantID string) bool {
	id := auth.IdentityFromContext(ctx)
	if id == nil || id.TenantID != tenantID {
		return false
	}
	return holdsRoleID(id.Roles, role.RoleNameOwner, role.SystemRoleID(tenantID, role.RoleNameOwner))
}

// holdsRoleID reports whether roleIDs contains roleName or its canonical,
// deterministic system-role identifier. System roles are undeletable and
// name-immutable (pkg/role.SystemRoleID), so no store lookup is needed to
// resolve the name to an identifier.
func holdsRoleID(roleIDs []string, roleName, canonicalID string) bool {
	return slices.Contains(roleIDs, roleName) || slices.Contains(roleIDs, canonicalID)
}

// CertTargetGuard is the middleware.CertTargetGuard this package's certificate
// target rule satisfies: no Security Officer or (for a non-Owner caller)
// Owner target, exactly as SignCertificate and EnrollCertificate already
// enforce internally via certTargetGuard. Defined here, rather than having
// pkg/auth/middleware import this package, to keep the dependency direction
// one-way (middleware declares the interface it needs; this package, and
// node/daemon composition, provide it).
type CertTargetGuard interface {
	AuthorizeCertTarget(ctx context.Context, tenantID string, target *user.User) error
}

// certTargetGuardAdapter adapts the package-private certTargetGuard function
// to the CertTargetGuard interface, so users:add_cert_binding (via
// AuthMiddleware.AuthorizeCertBindingTarget) is held to the SAME rule
// certs:issue and certs:enroll already are, with no duplicated logic.
type certTargetGuardAdapter struct{}

// AuthorizeCertTarget implements CertTargetGuard.
func (certTargetGuardAdapter) AuthorizeCertTarget(ctx context.Context, tenantID string, target *user.User) error {
	return certTargetGuard(ctx, tenantID, target)
}

// NewCertTargetGuard returns the CertTargetGuard certs:issue, certs:enroll
// and (through the auth middleware) users:add_cert_binding apply to every
// target account: a caller may bind a key to a target only when the target
// holds no ceremony-only role (Security Officer), or, for an Owner target,
// when the CALLER itself holds the tenant's Owner role.
func NewCertTargetGuard() CertTargetGuard { return certTargetGuardAdapter{} }
