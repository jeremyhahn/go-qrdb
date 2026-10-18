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

package embedded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestBarrierEnableEncryption_AuthorizesPerScope drives BarrierEnableEncryption
// behind real RBAC through the shared per-scope case table (go-qrdb#791): the
// handler authorizes seal:enable_encryption for the node's barrier and
// tenant_barrier:enable_encryption for the named tenant's, before the
// migrator is reached.
func TestBarrierEnableEncryption_AuthorizesPerScope(t *testing.T) {
	for _, c := range authtest.EnableEncryptionScopeCases() {
		t.Run(c.Name, func(t *testing.T) {
			roles := authtest.BuiltInRoles(t)
			mig := &stubEmbeddedMigrator{ch: make(chan embeddedMigrateCall, 1)}
			tr, err := New(newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(mig),
				WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer tr.Close()
			ctx := auth.WithIdentity(context.Background(), c.Caller)

			_, err = tr.BarrierEnableEncryption(ctx, &transport.BarrierEnableEncryptionRequest{
				TenantID: c.TenantID, Strategy: "passphrase", Passphrase: []byte("pw"),
			})

			if !c.Admit {
				if !errors.Is(err, auth.ErrUnauthorized) {
					t.Fatalf("err = %v, want %v", err, auth.ErrUnauthorized)
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
// an unauthorized caller is refused even where no migrator is wired, so the
// refusal never leaks whether the node can migrate.
func TestBarrierEnableEncryption_RefusesBeforeTheMigratorCheck(t *testing.T) {
	roles := authtest.BuiltInRoles(t)
	tr, err := New(newMockKVService(), &mockAdminService{},
		WithAuthMiddleware(authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tr.Close()
	admin := authtest.GrantedNonSOIdentity(authtest.EnableEncryptionTenant, "acme-admin", permission.TenantBarrierEnableEncryption)
	ctx := auth.WithIdentity(context.Background(), admin)

	_, err = tr.BarrierEnableEncryption(ctx, &transport.BarrierEnableEncryptionRequest{
		TenantID: authtest.EnableEncryptionOtherTenant, Strategy: "passphrase",
	})
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v, want %v", err, auth.ErrUnauthorized)
	}
	if errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatal("the refusal reports the missing migrator")
	}
}
