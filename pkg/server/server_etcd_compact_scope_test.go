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

// An etcd listener admits a system-tenant caller to another tenant's
// database -- the system tenant is the store-global administrator, and the
// RPC's permission is checked for the owning tenant, where ScopeSelf lifts
// it. Compaction is the exception: mvcc:compact is ScopeSelfStrict, so the
// system tenant cannot compact a database another tenant owns through that
// tenant's listener, while the owner's own identity can.
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

	if err := authorize(operator, permission.MVCCCompact); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("the system tenant compacting acme's database through acme's listener = %v, want PermissionDenied", err)
	}
	// The control: the same identity is still admitted to a ScopeSelf verb on
	// that listener, so the refusal above is the compaction scope and not a
	// fixture that refuses everything.
	if err := authorize(operator, permission.KVRange); err != nil {
		t.Fatalf("the system tenant's kv:range on acme's listener = %v, want admitted", err)
	}
	if err := authorize(owner, permission.MVCCCompact); err != nil {
		t.Fatalf("acme compacting its own database = %v, want admitted", err)
	}
}
