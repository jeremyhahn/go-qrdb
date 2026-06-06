// Copyright 2026 Jeremy Hahn
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

package unix

import (
	"context"
	"errors"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// protoToUser
// ---------------------------------------------------------------------------

func TestUnixProtoToUser_Nil(t *testing.T) {
	if got := protoToUser(nil); got != nil {
		t.Fatalf("protoToUser(nil) = %v, want nil", got)
	}
}

func TestUnixProtoToUser_Valid(t *testing.T) {
	in := &pb.UserResponse{
		Id:        1,
		TenantId:  "tenant-a",
		Username:  "alice",
		RoleIds:   []string{"role1", "role2"},
		Enabled:   true,
		System:    false,
		CreatedAt: "2026-01-01T00:00:00Z",
		UpdatedAt: "2026-02-01T00:00:00Z",
	}
	out := protoToUser(in)
	if out == nil {
		t.Fatal("protoToUser returned nil")
	}
	if out.ID != 1 || out.TenantID != "tenant-a" || out.Username != "alice" {
		t.Errorf("identity mismatch: %+v", out)
	}
	if len(out.RoleIDs) != 2 || out.RoleIDs[0] != "role1" || out.RoleIDs[1] != "role2" {
		t.Errorf("role mismatch: %v", out.RoleIDs)
	}
	if !out.Enabled || out.System {
		t.Errorf("flag mismatch: enabled=%v system=%v", out.Enabled, out.System)
	}
	if out.CreatedAt != "2026-01-01T00:00:00Z" || out.UpdatedAt != "2026-02-01T00:00:00Z" {
		t.Errorf("timestamps mismatch: %+v", out)
	}
}

// ---------------------------------------------------------------------------
// protoToRole
// ---------------------------------------------------------------------------

func TestUnixProtoToRole_Nil(t *testing.T) {
	if got := protoToRole(nil); got != nil {
		t.Fatalf("protoToRole(nil) = %v, want nil", got)
	}
}

func TestUnixProtoToRole_Valid(t *testing.T) {
	in := &pb.RoleResponse{
		Id:          "r1",
		TenantId:    "t1",
		Name:        "admin",
		Permissions: []string{"kv.read", "kv.write"},
		System:      true,
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-02-01T00:00:00Z",
	}
	out := protoToRole(in)
	if out == nil {
		t.Fatal("protoToRole returned nil")
	}
	if out.ID != "r1" || out.TenantID != "t1" || out.Name != "admin" {
		t.Errorf("identity mismatch: %+v", out)
	}
	if len(out.Permissions) != 2 {
		t.Errorf("permissions mismatch: %v", out.Permissions)
	}
	if !out.System {
		t.Errorf("System = false, want true")
	}
}

// ---------------------------------------------------------------------------
// protoToCertRecord
// ---------------------------------------------------------------------------

func TestUnixProtoToCertRecord_Nil(t *testing.T) {
	if got := protoToCertRecord(nil); got != nil {
		t.Fatalf("protoToCertRecord(nil) = %v, want nil", got)
	}
}

func TestUnixProtoToCertRecord_Valid(t *testing.T) {
	in := &pb.CertRecord{
		Serial:    "abc123",
		Username:  "bob",
		TenantId:  "t1",
		IssuedAt:  "2026-01-01T00:00:00Z",
		ExpiresAt: "2027-01-01T00:00:00Z",
		Revoked:   true,
	}
	out := protoToCertRecord(in)
	if out == nil {
		t.Fatal("protoToCertRecord returned nil")
	}
	if out.Serial != "abc123" || out.Username != "bob" || out.TenantID != "t1" {
		t.Errorf("identity mismatch: %+v", out)
	}
	if !out.Revoked {
		t.Errorf("Revoked = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Closed-state tests for User / Role / Cert methods.
// These guard the closed-check shortcuts in every transport method.
// ---------------------------------------------------------------------------

func TestUnixUser_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.CreateUser(ctx, &transport.CreateUserRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateUser: %v", err)
	}
	if _, err := tr.GetUser(ctx, 1); !errors.As(err, &ce) {
		t.Errorf("GetUser: %v", err)
	}
	if _, err := tr.GetUserByUsername(ctx, "t", "u"); !errors.As(err, &ce) {
		t.Errorf("GetUserByUsername: %v", err)
	}
	if _, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateUser: %v", err)
	}
	if err := tr.DeleteUser(ctx, 1); !errors.As(err, &ce) {
		t.Errorf("DeleteUser: %v", err)
	}
	if _, err := tr.ListUsers(ctx, &transport.ListUsersRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListUsers: %v", err)
	}
	if err := tr.EnableUser(ctx, 1); !errors.As(err, &ce) {
		t.Errorf("EnableUser: %v", err)
	}
	if err := tr.DisableUser(ctx, 1); !errors.As(err, &ce) {
		t.Errorf("DisableUser: %v", err)
	}
}

func TestUnixRole_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.CreateRole(ctx, &transport.CreateRoleRequest{}); !errors.As(err, &ce) {
		t.Errorf("CreateRole: %v", err)
	}
	if _, err := tr.GetRole(ctx, "r"); !errors.As(err, &ce) {
		t.Errorf("GetRole: %v", err)
	}
	if _, err := tr.UpdateRole(ctx, &transport.UpdateRoleRequest{}); !errors.As(err, &ce) {
		t.Errorf("UpdateRole: %v", err)
	}
	if err := tr.DeleteRole(ctx, "r"); !errors.As(err, &ce) {
		t.Errorf("DeleteRole: %v", err)
	}
	if _, err := tr.ListRoles(ctx, &transport.ListRolesRequest{}); !errors.As(err, &ce) {
		t.Errorf("ListRoles: %v", err)
	}
	if err := tr.AssignRole(ctx, 1, "r"); !errors.As(err, &ce) {
		t.Errorf("AssignRole: %v", err)
	}
	if err := tr.UnassignRole(ctx, 1, "r"); !errors.As(err, &ce) {
		t.Errorf("UnassignRole: %v", err)
	}
	if _, err := tr.ListPermissions(ctx); !errors.As(err, &ce) {
		t.Errorf("ListPermissions: %v", err)
	}
}

func TestUnixCert_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if _, err := tr.GenerateCert(ctx, &transport.GenerateCertRequest{}); !errors.As(err, &ce) {
		t.Errorf("GenerateCert: %v", err)
	}
	if _, err := tr.ExportCert(ctx, &transport.ExportCertRequest{}); !errors.As(err, &ce) {
		t.Errorf("ExportCert: %v", err)
	}
	if err := tr.RevokeCert(ctx, "s"); !errors.As(err, &ce) {
		t.Errorf("RevokeCert: %v", err)
	}
	if _, err := tr.ListCerts(ctx, "t"); !errors.As(err, &ce) {
		t.Errorf("ListCerts: %v", err)
	}
}
