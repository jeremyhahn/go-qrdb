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

//go:build qrdb_etcd

package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestEtcdRequestAuthorizer_SystemTenantIsRefused: a database's etcd
// listener serves its owning tenant and no one else. A system-tenant
// identity -- Admin, Security Officer or a read-only Auditor -- is refused
// exactly as another tenant's is: there is no cross-tenant database access.
func TestEtcdRequestAuthorizer_SystemTenantIsRefused(t *testing.T) {
	f := newEtcdScopeFixture(t, false)
	callers := map[string]*auth.Identity{
		"system admin":   authtest.AdminIdentity(tenantid.System, "root"),
		"system so":      authtest.SOIdentity(tenantid.System, "so"),
		"system auditor": {Subject: "ro", TenantID: tenantid.System, Roles: []string{role.SystemRoleID(tenantid.System, role.RoleNameAuditor)}},
		"globex admin":   authtest.AdminIdentity("globex", "g"),
	}
	for name, id := range callers {
		mw := authmw.New(allowAll{}, authmw.WithAuthenticator(authtest.NewFixedAuthenticator(id)))
		a := &etcdRequestAuthorizer{mw: mw, mgr: f.mgr, owner: "acme", db: "app"}
		if _, err := a.AuthorizeRequest(context.Background(), permission.KVGet); status.Code(err) != codes.PermissionDenied {
			t.Errorf("%s on acme's etcd listener = %v, want PermissionDenied", name, err)
		}
	}
}
