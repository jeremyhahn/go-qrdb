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

//go:build qrdb_vlog

package unix

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

const vlogRotateTenant = "acme"

// vlogClientAs starts a Unix server over svc whose every caller holds
// roleName in vlogRotateTenant, through the real authorization middleware and
// RBAC, and returns a client for it and a context naming database ledger.
func vlogClientAs(t *testing.T, svc service.LogService, roleName string) (pb.QRDBServiceClient, context.Context) {
	t.Helper()
	roles := authtest.BuiltInRoles(t, vlogRotateTenant)
	caller := &auth.Identity{
		Subject:  roleName + "-caller",
		TenantID: vlogRotateTenant,
		Roles:    []string{role.SystemRoleID(vlogRotateTenant, roleName)},
	}
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)))
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithLogService(svc), WithAuthMiddleware(mw), WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(srv.Stop)
	conn := dialUnixTLS(t, socketPath, tlsCfg)
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return pb.NewQRDBServiceClient(conn), metadata.AppendToOutgoingContext(ctx, grpcpkg.MetadataDatabase, "ledger")
}

// TestVlogRotateKey_OverTheSocket rotates a real log's head key over the
// socket: an Admin is refused, an Operator rotates, the checkpoint signed
// after names the new key, and the one signed before still verifies.
func TestVlogRotateKey_OverTheSocket(t *testing.T) {
	svc := servicetest.NewLogService(t)
	key1 := servicetest.SeedSignedLog(t, svc, vlogRotateTenant, "ledger", "audit")
	operator, ctx := vlogClientAs(t, svc, role.RoleNameOperator)
	admin, adminCtx := vlogClientAs(t, svc, role.RoleNameAdmin)
	sign := func() []byte {
		t.Helper()
		resp, err := operator.VlogSignHead(ctx, &pb.VlogSignHeadRequest{Log: "audit"})
		if err != nil {
			t.Fatalf("VlogSignHead: %v", err)
		}
		return resp.GetCheckpoint()
	}
	rotate := &pb.VlogRotateKeyRequest{Log: "audit", SigningKeyType: pb.VlogSignatureType_VLOG_SIGNATURE_TYPE_ED25519}

	before := sign()
	if _, err := admin.VlogRotateKey(adminCtx, rotate); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Admin rotation: err = %v, want PermissionDenied", err)
	}
	resp, err := operator.VlogRotateKey(ctx, rotate)
	if err != nil {
		t.Fatalf("Operator rotation: %v", err)
	}
	k := resp.GetKey()
	key2 := vlog.HeadKey{Version: k.GetVersion(), Ref: k.GetRef(), PublicKey: k.GetPublicKey()}
	if key2.Version != key1.Version+1 {
		t.Fatalf("rotated key version = %d, want %d", key2.Version, key1.Version+1)
	}
	if _, err := operator.VlogAppend(ctx, &pb.VlogAppendRequest{Log: "audit", Entries: [][]byte{[]byte("second")}}); err != nil {
		t.Fatalf("VlogAppend: %v", err)
	}
	after := sign()

	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, after, key2)
	servicetest.RequireHeadSignedBy(t, servicetest.LogOrigin, before, key1)
}
