package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestIdentityHandlers_OperatorAccessRefusalIsReturned: every identity-record
// handler that names a tenant runs the operator-access gate before its
// service call, and returns the gate's refusal as is. A failing isolation
// policy read answers codes.Unavailable on every one of them.
func TestIdentityHandlers_OperatorAccessRefusalIsReturned(t *testing.T) {
	sink := &unavailablePolicySink{}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	mw := authmw.New(rbac.New(roles),
		authmw.WithRoleResolver(roles),
		authmw.WithAuditLogger(sink), authmw.WithOperatorAccessTenantTrail(sink),
		authmw.WithIsolationPolicyReader(unavailablePolicyReader{}))
	srv := &qrdbServiceServer{userSvc: &mockUserService{}, roleSvc: roles, authMW: mw}
	ctx := auth.WithIdentity(tenant.WithTenantID(context.Background(), tenantid.System),
		authtest.AdminIdentity(tenantid.System, "operator"))
	const tid = "acme"

	cases := []struct {
		name string
		call func() error
	}{
		{"GetUser", func() error { _, err := srv.GetUser(ctx, &pb.GetUserRequest{TenantId: tid}); return err }},
		{"GetUserByUsername", func() error {
			_, err := srv.GetUserByUsername(ctx, &pb.GetUserByUsernameRequest{TenantId: tid})
			return err
		}},
		{"GetUserByEmail", func() error {
			_, err := srv.GetUserByEmail(ctx, &pb.GetUserByEmailRequest{TenantId: tid})
			return err
		}},
		{"GetUserByCertThumbprint", func() error {
			_, err := srv.GetUserByCertThumbprint(ctx, &pb.GetUserByCertThumbprintRequest{TenantId: tid})
			return err
		}},
		{"AddCertBinding", func() error {
			_, err := srv.AddCertBinding(ctx, &pb.AddCertBindingRequest{TenantId: tid})
			return err
		}},
		{"RemoveCertBinding", func() error {
			_, err := srv.RemoveCertBinding(ctx, &pb.RemoveCertBindingRequest{TenantId: tid})
			return err
		}},
		{"UpdateUser", func() error { _, err := srv.UpdateUser(ctx, &pb.UpdateUserProtoRequest{TenantId: tid}); return err }},
		{"DeleteUser", func() error { _, err := srv.DeleteUser(ctx, &pb.DeleteUserRequest{TenantId: tid}); return err }},
		{"EnableUser", func() error { _, err := srv.EnableUser(ctx, &pb.EnableUserRequest{TenantId: tid}); return err }},
		{"DisableUser", func() error { _, err := srv.DisableUser(ctx, &pb.DisableUserRequest{TenantId: tid}); return err }},
		{"SuspendUser", func() error { _, err := srv.SuspendUser(ctx, &pb.SuspendUserRequest{TenantId: tid}); return err }},
		{"UnsuspendUser", func() error {
			_, err := srv.UnsuspendUser(ctx, &pb.UnsuspendUserRequest{TenantId: tid})
			return err
		}},
		{"CreateRole", func() error { _, err := srv.CreateRole(ctx, &pb.CreateRoleRequest{TenantId: tid}); return err }},
		{"GetRole", func() error { _, err := srv.GetRole(ctx, &pb.GetRoleRequest{TenantId: tid}); return err }},
		{"GetRoleByName", func() error {
			_, err := srv.GetRoleByName(ctx, &pb.GetRoleByNameRequest{TenantId: tid})
			return err
		}},
		{"UpdateRole", func() error { _, err := srv.UpdateRole(ctx, &pb.UpdateRoleProtoRequest{TenantId: tid}); return err }},
		{"DeleteRole", func() error { _, err := srv.DeleteRole(ctx, &pb.DeleteRoleRequest{TenantId: tid}); return err }},
		{"ListRoles", func() error { _, err := srv.ListRoles(ctx, &pb.ListRolesRequest{TenantId: tid}); return err }},
		{"AssignRole", func() error { _, err := srv.AssignRole(ctx, &pb.AssignRoleRequest{TenantId: tid}); return err }},
		{"UnassignRole", func() error {
			_, err := srv.UnassignRole(ctx, &pb.UnassignRoleRequest{TenantId: tid})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := status.Code(tc.call()); got != codes.Unavailable {
				t.Fatalf("%s with a failing policy read: code = %v, want Unavailable", tc.name, got)
			}
		})
	}
}

// TestGetUserByEmail_ServiceErrorIsMapped: with no tenant named the gate is
// not consulted and the user service's error is mapped; with no user service
// the handler refuses as not configured.
func TestGetUserByEmail_ServiceErrorIsMapped(t *testing.T) {
	none := &qrdbServiceServer{}
	if _, err := none.GetUserByEmail(context.Background(), &pb.GetUserByEmailRequest{}); err == nil {
		t.Fatal("GetUserByEmail with no user service: want an error")
	}
	srv := &qrdbServiceServer{userSvc: &mockUserService{getByEmailErr: errors.New("store down")}}
	if _, err := srv.GetUserByEmail(context.Background(), &pb.GetUserByEmailRequest{Email: "a@b"}); err == nil {
		t.Fatal("GetUserByEmail with a failing user service: want an error")
	}
}

// TestGroupHandlers_NoGroupServiceIsRefused: every group handler refuses as
// not configured when no group store is wired, before touching the request.
func TestGroupHandlers_NoGroupServiceIsRefused(t *testing.T) {
	srv := &qrdbServiceServer{}
	ctx := context.Background()
	cases := []struct {
		name string
		call func() error
	}{
		{"CreateGroup", func() error { _, err := srv.CreateGroup(ctx, &pb.CreateGroupRequest{}); return err }},
		{"GetGroup", func() error { _, err := srv.GetGroup(ctx, &pb.GetGroupRequest{}); return err }},
		{"GetGroupByName", func() error { _, err := srv.GetGroupByName(ctx, &pb.GetGroupByNameRequest{}); return err }},
		{"UpdateGroup", func() error { _, err := srv.UpdateGroup(ctx, &pb.UpdateGroupRequest{}); return err }},
		{"DeleteGroup", func() error { _, err := srv.DeleteGroup(ctx, &pb.DeleteGroupRequest{}); return err }},
		{"ListGroups", func() error { _, err := srv.ListGroups(ctx, &pb.ListGroupsRequest{}); return err }},
		{"AddGroupMember", func() error { _, err := srv.AddGroupMember(ctx, &pb.GroupMemberRequest{}); return err }},
		{"RemoveGroupMember", func() error { _, err := srv.RemoveGroupMember(ctx, &pb.GroupMemberRequest{}); return err }},
		{"ListGroupMembers", func() error {
			_, err := srv.ListGroupMembers(ctx, &pb.ListGroupMembersRequest{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatalf("%s with no group service: want an error", tc.name)
			}
		})
	}
	if groupToProto(nil) != nil {
		t.Fatal("groupToProto(nil) = non-nil, want nil")
	}
}

// TestConverters_NilAndSetInputs: an absent entitlement set or barrier
// status converts to its empty wire form; a set entitlement is carried.
func TestConverters_NilAndSetInputs(t *testing.T) {
	if entitlementsToProto(nil) != nil {
		t.Fatal("entitlementsToProto(nil) = non-nil, want nil")
	}
	if got := entitlementsToProto(&quota.Entitlements{AllowDedicatedRaft: true}); got == nil || !got.GetAllowDedicatedRaft() {
		t.Fatalf("entitlementsToProto: got %v, want AllowDedicatedRaft", got)
	}
	if got := databaseBarrierStatusToProto(nil); got == nil || got.GetSealed() {
		t.Fatalf("databaseBarrierStatusToProto(nil) = %v, want the empty response", got)
	}
}
