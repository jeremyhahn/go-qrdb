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
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Compaction through a database's etcd listener is the owning tenant's
// alone: mvcc:compact is ScopeSelfStrict, and the listener admits no other
// tenant -- the system tenant included, which has no cross-tenant database
// access on any surface (docs/databases). The owner's identity is admitted to
// compact and to a ScopeSelf verb; the system tenant to neither.
func TestEtcdRequestAuthorizer_CompactIsTheOwningTenantsAlone(t *testing.T) {
	f := newEtcdScopeFixture(t, false)
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	grant := []permission.Permission{permission.MVCCCompact, permission.KVRange}
	operator := authtest.Grant(t, roles, &auth.Identity{Subject: "root", TenantID: tenantid.System}, grant...)
	owner := authtest.Grant(t, roles, &auth.Identity{Subject: "alice", TenantID: "acme"}, grant...)

	authorize := func(id *auth.Identity, perm permission.Permission) error {
		t.Helper()
		a := &etcdRequestAuthorizer{
			mw:    authmw.New(rbac.New(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(id))),
			reg:   f.srv.buildServiceRegistry(),
			mgr:   f.mgr,
			owner: "acme",
			db:    "app",
		}
		_, err := a.AuthorizeRequest(context.Background(), perm)
		return err
	}

	for _, perm := range []permission.Permission{permission.MVCCCompact, permission.KVRange} {
		if err := authorize(operator, perm); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("the system tenant's %s through acme's listener = %v, want PermissionDenied", perm, err)
		}
	}
	// The control: the owner is admitted to both, so the refusals above are
	// the tenancy rule and not a fixture that refuses everything.
	for _, perm := range []permission.Permission{permission.MVCCCompact, permission.KVRange} {
		if err := authorize(owner, perm); err != nil {
			t.Fatalf("acme's %s on its own database = %v, want admitted", perm, err)
		}
	}
}
