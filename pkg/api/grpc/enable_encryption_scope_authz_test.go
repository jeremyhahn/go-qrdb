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
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestBarrierEnableEncryption_AuthorizesPerScope drives BarrierEnableEncryption
// through the real authorization interceptor and RBAC over the shared
// per-scope case table (go-qrdb#791): the method gate admits any
// authenticated caller, and the handler authorizes seal:enable_encryption for
// the node's barrier and tenant_barrier:enable_encryption for the named
// tenant's, before the migrator is reached.
func TestBarrierEnableEncryption_AuthorizesPerScope(t *testing.T) {
	for _, c := range authtest.EnableEncryptionScopeCases() {
		t.Run(c.Name, func(t *testing.T) {
			roles := authtest.BuiltInRoles(t)
			mig := &captureGRPCMigrator{ch: make(chan grpcMigrateCall, 1)}
			mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
				authmw.WithAuthenticator(authtest.NewFixedAuthenticator(c.Caller)))
			svc := &qrdbServiceServer{authMW: mw, encMigrator: mig}
			req := &pb.BarrierEnableEncryptionRequest{
				TenantId: c.TenantID, Strategy: "passphrase", Passphrase: []byte("pw"),
			}
			info := &grpc.UnaryServerInfo{FullMethod: pb.QRDBService_BarrierEnableEncryption_FullMethodName}

			_, err := mw.UnaryInterceptor()(context.Background(), req, info, func(ctx context.Context, r any) (any, error) {
				return svc.BarrierEnableEncryption(ctx, r.(*pb.BarrierEnableEncryptionRequest))
			})

			if !c.Admit {
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("err = %v, want PermissionDenied", err)
				}
				if n := len(mig.ch); n != 0 {
					t.Fatalf("a refused call reached the migrator %d time(s)", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("admitted call failed: %v", err)
			}
			want := service.BarrierScope{Kind: service.ScopeSystem}
			if c.TenantID != "" {
				want = service.BarrierScope{Kind: service.ScopeTenant, TenantID: c.TenantID}
			}
			select {
			case call := <-mig.ch:
				if call.scope != want {
					t.Fatalf("migrator scope = %+v, want %+v", call.scope, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("an admitted call did not reach the migrator")
			}
		})
	}
}

// TestBarrierEnableEncryption_RefusesBeforeTheMigratorCheck pins the order:
// an unauthorized caller is refused PermissionDenied even where no migrator
// is wired, so the refusal never leaks whether the node can migrate.
func TestBarrierEnableEncryption_RefusesBeforeTheMigratorCheck(t *testing.T) {
	roles := authtest.BuiltInRoles(t)
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))
	svc := &qrdbServiceServer{authMW: mw}
	admin := authtest.GrantedNonSOIdentity(authtest.EnableEncryptionTenant, "acme-admin", permission.TenantBarrierEnableEncryption)
	ctx := auth.WithIdentity(context.Background(), admin)

	_, err := svc.BarrierEnableEncryption(ctx, &pb.BarrierEnableEncryptionRequest{
		TenantId: authtest.EnableEncryptionOtherTenant, Strategy: "passphrase",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
}
