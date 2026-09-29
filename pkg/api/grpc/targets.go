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

package grpc

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// The request shapes a target is read from. Every generated request message
// exposes its fields through these getters, so one extractor serves every
// method whose request carries the field.
type (
	userIDRequest   interface{ GetId() uint64 }
	userRefRequest  interface{ GetUserId() uint64 }
	stringIDRequest interface{ GetId() string }
	tenantRequest   interface{ GetTenantId() string }
)

// targetUserID targets the user named by the request's numeric id.
func targetUserID(req any) authmw.Target {
	if r, ok := req.(userIDRequest); ok {
		return authmw.UserTarget(r.GetId())
	}
	return authmw.SelfTarget()
}

// targetUserRef targets the user named by the request's user_id.
func targetUserRef(req any) authmw.Target {
	if r, ok := req.(userRefRequest); ok {
		return authmw.UserTarget(r.GetUserId())
	}
	return authmw.SelfTarget()
}

// targetRoleID targets the role named by the request's id.
func targetRoleID(req any) authmw.Target {
	if r, ok := req.(stringIDRequest); ok {
		return authmw.RoleTarget(r.GetId())
	}
	return authmw.SelfTarget()
}

// targetTenantField targets the tenant named by the request's tenant_id.
func targetTenantField(req any) authmw.Target {
	if r, ok := req.(tenantRequest); ok {
		return authmw.TenantTarget(r.GetTenantId())
	}
	return authmw.SelfTarget()
}

// IdentityTargets is how each gRPC method that acts on a named user, role or
// tenant names it. The gRPC and unix servers register it on their
// AuthMiddleware (RegisterGRPCTargets) so the route-level permission check
// is made against that tenant, the same resolution every other transport
// uses (see authmw.Target).
func IdentityTargets() map[string]authmw.GRPCTargetFunc {
	return map[string]authmw.GRPCTargetFunc{
		pb.QRDBService_CreateUser_FullMethodName:        targetTenantField,
		pb.QRDBService_GetUser_FullMethodName:           targetUserID,
		pb.QRDBService_GetUserByUsername_FullMethodName: targetTenantField,
		pb.QRDBService_GetUserByEmail_FullMethodName:    targetTenantField,
		pb.QRDBService_UpdateUser_FullMethodName:        targetUserID,
		pb.QRDBService_DeleteUser_FullMethodName:        targetUserID,
		pb.QRDBService_ListUsers_FullMethodName:         targetTenantField,
		pb.QRDBService_EnableUser_FullMethodName:        targetUserID,
		pb.QRDBService_DisableUser_FullMethodName:       targetUserID,
		pb.QRDBService_AddCertBinding_FullMethodName:    targetUserRef,
		pb.QRDBService_RemoveCertBinding_FullMethodName: targetUserRef,

		pb.QRDBService_CreateRole_FullMethodName:            targetTenantField,
		pb.QRDBService_GetRole_FullMethodName:               targetRoleID,
		pb.QRDBService_GetRoleByName_FullMethodName:         targetTenantField,
		pb.QRDBService_UpdateRole_FullMethodName:            targetRoleID,
		pb.QRDBService_DeleteRole_FullMethodName:            targetRoleID,
		pb.QRDBService_ListRoles_FullMethodName:             targetTenantField,
		pb.QRDBService_AssignRole_FullMethodName:            targetUserRef,
		pb.QRDBService_UnassignRole_FullMethodName:          targetUserRef,
		pb.QRDBService_ReconcileBuiltInRoles_FullMethodName: targetTenantField,

		pb.TenantBarrierService_Seal_FullMethodName:            targetTenantField,
		pb.TenantBarrierService_OfficerRegister_FullMethodName: targetTenantField,
		pb.TenantBarrierService_OfficerList_FullMethodName:     targetTenantField,
		pb.TenantBarrierService_RekeyStart_FullMethodName:      targetTenantField,
		pb.TenantBarrierService_OfficerRevoke_FullMethodName:   targetTenantField,
	}
}
