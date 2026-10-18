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

package quic

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandleBarrierEnableEncryption_AuthorizesPerScope runs
// barrier.EnableEncryption the way dispatch does -- the method gate, then the
// handler -- under real RBAC for every caller in
// authtest.EnableEncryptionScopeCases (go-qrdb#791). The method gate admits
// any authenticated caller; the handler decides by the barrier named. A
// refused call is an authorization failure that never reaches the migrator;
// an admitted one reaches it with the scope the request named.
func TestHandleBarrierEnableEncryption_AuthorizesPerScope(t *testing.T) {
	for _, c := range authtest.EnableEncryptionScopeCases() {
		t.Run(c.Name, func(t *testing.T) {
			roles := authtest.BuiltInRoles(t)
			mig := &captureQUICMigrator{ch: make(chan quicMigrateCall, 1)}
			srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithEncryptionMigrator(mig),
				WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))))
			ctx := auth.WithIdentity(context.Background(), c.Caller)
			payload, err := proto.Marshal(&pb.BarrierEnableEncryptionRequest{
				TenantId: c.TenantID, Strategy: "passphrase", Passphrase: []byte("pw"),
			})
			if err != nil {
				t.Fatal(err)
			}

			if err := srv.authorize(ctx, "barrier.EnableEncryption"); err != nil {
				t.Fatalf("method gate refused an authenticated caller: %v", err)
			}
			_, err = srv.handleBarrierEnableEncryption(ctx, payload)

			if !c.Admit {
				if authmw.ClassifyAuthError(err) != authmw.AuthFailureUnauthorized {
					t.Fatalf("err = %v, want an authorization refusal", err)
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
