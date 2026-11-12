package permission

import "testing"

func TestTenantIdentityPermissions_RegisteredCrossScope(t *testing.T) {
	want := map[Permission]string{
		UsersReadTenant:  "users:read:tenant",
		UsersWriteTenant: "users:write:tenant",
		RolesReadTenant:  "roles:read:tenant",
		RolesWriteTenant: "roles:write:tenant",
	}
	for p, name := range want {
		if p.String() != name {
			t.Errorf("%q: wire name %q, want %q", p, p.String(), name)
		}
		if !IsRegistered(p) {
			t.Errorf("%q is not registered", p)
		}
		if !IsCrossScope(p) {
			t.Errorf("%q is not ScopeCross: a tenant-local identity must never hold it", p)
		}
	}
}

func TestTenantIdentityPermission_Selects(t *testing.T) {
	cases := []struct {
		roles, write bool
		want         Permission
	}{
		{false, false, UsersReadTenant},
		{false, true, UsersWriteTenant},
		{true, false, RolesReadTenant},
		{true, true, RolesWriteTenant},
	}
	for _, c := range cases {
		if got := TenantIdentityPermission(c.roles, c.write); got != c.want {
			t.Errorf("TenantIdentityPermission(%v, %v) = %q, want %q", c.roles, c.write, got, c.want)
		}
	}
}
