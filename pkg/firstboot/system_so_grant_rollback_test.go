package firstboot

import (
	"context"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// completeSOEnrollment is what a second enrollment attempt commits once it
// finishes: the SO's key bound and recorded on the account.
func completeSOEnrollment(u *user.User, soKey string) {
	u.CertBindings = append(u.CertBindings, user.CertBinding{Thumbprint: soKey})
	if u.Attributes == nil {
		u.Attributes = map[string]string{}
	}
	u.Attributes[soBoundKeyAttribute] = soKey
}

// Attempt A granted the SO role, attempt B observed the grant and finished
// enrollment; A failing afterwards must not take the role from B's account.
func TestRevokeAdminSORole_KeepsTheGrantOfACompletedEnrollment(t *testing.T) {
	const soRole, soKey = "so", "so-key"
	admin := &user.User{ID: 1, TenantID: "system", Username: "admin", RoleIDs: []string{"admin", soRole}}
	completeSOEnrollment(admin, soKey)
	users, deps := committingFixture(admin, nil)
	snapshot := *admin
	snapshot.RoleIDs = slices.Clone(admin.RoleIDs)
	if err := revokeAdminSORole(context.Background(), deps, &snapshot, soRole, soKey); err != nil {
		t.Fatalf("revokeAdminSORole: %v", err)
	}
	if got := users.byName["admin"].RoleIDs; !slices.Contains(got, soRole) {
		t.Fatalf("roles = %v: the rollback removed the SO role of a completed enrollment", got)
	}
}

// B's completion committing between A's re-read and A's rollback write
// refuses the write, and the re-read keeps the role.
func TestRevokeAdminSORole_KeepsTheGrantWhenEnrollmentCompletesDuringRollback(t *testing.T) {
	const soRole, soKey = "so", "so-key"
	admin := &user.User{ID: 1, TenantID: "system", Username: "admin", RoleIDs: []string{"admin", soRole}}
	users, deps := committingFixture(admin, func(u *user.User) { completeSOEnrollment(u, soKey) })
	snapshot := *admin
	snapshot.RoleIDs = slices.Clone(admin.RoleIDs)
	if err := revokeAdminSORole(context.Background(), deps, &snapshot, soRole, soKey); err != nil {
		t.Fatalf("revokeAdminSORole: %v", err)
	}
	if got := users.byName["admin"].RoleIDs; !slices.Contains(got, soRole) {
		t.Fatalf("roles = %v: the rollback removed the SO role of a completed enrollment", got)
	}
}
