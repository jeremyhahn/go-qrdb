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

package embedded

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// An embedded caller never re-authenticates: its identity rides in the
// context it was handed. Once its tenant is suspended its next operation is
// refused with the auth chain's *TenantNotActiveError, and served again once
// the tenant is reactivated.
func TestEmbedded_IdentityRefusedOnceTenantSuspended(t *testing.T) {
	tenants, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	if _, err := tenants.CreateTenant(context.Background(), "acme", "acme", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	kv := newMockKVService()
	kv.store["app/k1"] = []byte("v1")
	tr, err := New(kv, &mockAdminService{}, WithAuthMiddleware(authmw.New(grantOnlyAuthz(permission.KVGet),
		authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithTenantStatusReader(tenants))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := ctxFor(&auth.Identity{Subject: "reader", TenantID: "acme"})
	if _, err := tr.Get(ctx, "app/k1"); err != nil {
		t.Fatalf("Get while active: %v", err)
	}

	setEmbeddedTenantStatus(t, tenants, tenant.StatusSuspended)
	_, err = tr.Get(ctx, "app/k1")
	var notActive *authmw.TenantNotActiveError
	if !errors.As(err, &notActive) || notActive.TenantID != "acme" || notActive.Status != string(tenant.StatusSuspended) {
		t.Fatalf("Get while suspended = %T(%v), want *TenantNotActiveError{acme, suspended}", err, err)
	}

	setEmbeddedTenantStatus(t, tenants, tenant.StatusActive)
	if _, err := tr.Get(ctx, "app/k1"); err != nil {
		t.Fatalf("Get after reactivation: %v", err)
	}
}

func setEmbeddedTenantStatus(t *testing.T, tenants tenant.TenantServicer, s tenant.Status) {
	t.Helper()
	if _, err := tenants.UpdateTenant(context.Background(), "acme", tenant.TenantPatch{Status: &s}); err != nil {
		t.Fatalf("UpdateTenant(%s): %v", s, err)
	}
}
