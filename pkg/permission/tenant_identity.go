package permission

// Tenant-qualified operator permissions (strict-tenant-isolation phase 5).
//
// These let the platform operator -- an identity of the system tenant --
// read and write ANOTHER tenant's identity records: its users, its roles
// and role bindings, and its users' credentials (certificate bindings,
// enabled/suspended state). They are ScopeCross: a tenant-local identity
// never holds them, whatever its roles say.
//
// Holding the permission is not enough. The access is admitted only when
// the target tenant's effective isolation policy is operator-access
// (tenant.EffectiveIsolationPolicy); a strict tenant refuses it. Every
// admitted or refused access is recorded in both audit trails: the
// platform trail and the target tenant's own (see
// middleware.AuthorizeOperatorIdentityAccess).
const (
	// UsersReadTenant reads another tenant's users and their credentials.
	UsersReadTenant Permission = "users:read:tenant"

	// UsersWriteTenant creates, updates, deletes, enables, disables,
	// suspends and unsuspends another tenant's users and adds or removes
	// their certificate bindings.
	UsersWriteTenant Permission = "users:write:tenant"

	// RolesReadTenant reads another tenant's roles and role bindings.
	RolesReadTenant Permission = "roles:read:tenant"

	// RolesWriteTenant creates, updates and deletes another tenant's roles
	// and assigns or revokes its role bindings.
	RolesWriteTenant Permission = "roles:write:tenant"
)

// tenantIdentityPermissions lists the tenant-qualified operator permissions.
// crossScopePermissions includes them, so they are registered and tagged
// ScopeCross with the rest.
func tenantIdentityPermissions() []Permission {
	return []Permission{UsersReadTenant, UsersWriteTenant, RolesReadTenant, RolesWriteTenant}
}

// TenantIdentityPermission returns the tenant-qualified operator permission
// for a read (write == false) or a write of a tenant's users (roles ==
// false) or roles (roles == true).
func TenantIdentityPermission(roles, write bool) Permission {
	switch {
	case roles && write:
		return RolesWriteTenant
	case roles:
		return RolesReadTenant
	case write:
		return UsersWriteTenant
	default:
		return UsersReadTenant
	}
}
