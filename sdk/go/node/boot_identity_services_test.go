//go:build !integration

package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// A strict clustered node restarting while its peers are down must not
// block on the startup holder check: the check reads the user and role
// stores locally, as cmd/qrdbd's boot stores do, and with no conflicting
// holder passes. Two of three replicas stop, so the survivor's runtime
// services, which read linearizably, cannot answer; the boot services still
// do.
func TestBootIdentityServices_StrictHolderCheckNeedsNoQuorum(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	nodes, err := bootFormationCluster(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	for i, n := range nodes[1:] {
		if err := n.Close(); err != nil {
			t.Fatalf("close replica %d: %v", replicas[i+1].id, err)
		}
	}
	survivor := nodes[0]
	svcNode := survivor.Services()
	if svcNode == nil {
		t.Fatal("survivor has no services")
	}

	// The runtime services cannot answer without a quorum: this is the
	// read the strict startup check used to block on.
	short, cancelShort := context.WithTimeout(context.Background(), 2*time.Second)
	linErr := firstboot.CheckStrictSeparationHolders(short, svcNode.User, svcNode.Role)
	cancelShort()
	if linErr == nil {
		t.Fatal("runtime holder check answered without a quorum; the survivor still has one")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	users, roles, err := survivor.bootIdentityServices(svcNode)
	if err != nil {
		t.Fatalf("bootIdentityServices: %v", err)
	}
	if err := firstboot.CheckStrictSeparationHolders(ctx, users, roles); err != nil {
		t.Fatalf("strict startup holder check without a quorum: %v", err)
	}
}

// A clustered node's boot stores must read the system identities where the
// runtime IAM persists them, under tenant.IdentityNamespace(tenantid.System):
// a persisted account holding both the system SO and Admin roles must make
// the strict startup holder check refuse, not pass over an empty raw keyspace
// (SPEC row 124).
func TestBootIdentityServices_StrictHolderCheckSeesPersistedConflict(t *testing.T) {
	ca, replicas := newFormationReplicas(t, 3)
	nodes, err := bootFormationCluster(t, ca, replicas, func(self uint64) string {
		return peerIdentitySpecs(t, self, replicas)
	})
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	n := nodes[0]
	svcNode := n.Services()
	if svcNode == nil {
		t.Fatal("node has no services")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sysCtx := tenant.WithTenantID(ctx, tenantid.System)

	var roleIDs []string
	for _, name := range []string{role.RoleNameSO, role.RoleNameAdmin} {
		r, err := svcNode.Role.GetRoleByName(sysCtx, tenantid.System, name)
		if err != nil {
			var notFound *role.RoleNotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("GetRoleByName(%s): %v", name, err)
			}
			var perms []permission.Permission
			for _, b := range role.BuiltInRoles(tenantid.System) {
				if b.Name == name {
					perms = b.Permissions
				}
			}
			r, err = svcNode.Role.CreateRole(sysCtx, &role.CreateRoleRequest{
				ID: role.SystemRoleID(tenantid.System, name), Name: name,
				TenantID: tenantid.System, Permissions: perms,
			})
			if err != nil {
				t.Fatalf("CreateRole(%s): %v", name, err)
			}
		}
		roleIDs = append(roleIDs, r.ID)
	}
	combined, err := svcNode.User.CreateUser(sysCtx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "combined-so-admin",
		TenantID:             tenantid.System,
		RoleIDs:              roleIDs,
	})
	if err != nil {
		t.Fatalf("CreateUser(combined SO/Admin): %v", err)
	}

	users, roles, err := n.bootIdentityServices(svcNode)
	if err != nil {
		t.Fatalf("bootIdentityServices: %v", err)
	}
	err = firstboot.CheckStrictSeparationHolders(ctx, users, roles)
	var conflict *firstboot.StrictSeparationSOAdminError
	if !errors.As(err, &conflict) {
		t.Fatalf("strict startup holder check over the boot stores: %v, want *firstboot.StrictSeparationSOAdminError", err)
	}
	if conflict.UserID != combined.ID {
		t.Fatalf("conflict = %+v, want user %d", conflict, combined.ID)
	}
}
