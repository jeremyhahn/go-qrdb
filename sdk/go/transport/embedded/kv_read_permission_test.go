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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// KV read permission is decided by the transport's authorize step, before the
// KV service is reached. A caller holding kv:put and kv:list but neither
// kv:get, kv:range nor mvcc:range must receive the typed permission denial
// from every value-returning read -- never the empty success a reached service
// would answer for a key it holds.

// grantOnlyAuthorizer grants exactly the permissions it holds.
type grantOnlyAuthorizer map[string]bool

func (a grantOnlyAuthorizer) Authorize(_ context.Context, _ *auth.Identity, resource, action string) error {
	if a[resource+":"+action] {
		return nil
	}
	return auth.ErrUnauthorized
}

func grantOnlyAuthz(perms ...permission.Permission) grantOnlyAuthorizer {
	a := grantOnlyAuthorizer{}
	for _, p := range perms {
		a[permission.Resource(p)+":"+permission.Action(p)] = true
	}
	return a
}

// seededKVTransport returns a transport over a KV service holding app/k1 and
// app/k2, authorizing through authz.
func seededKVTransport(t *testing.T, authz auth.Authorizer) *Transport {
	t.Helper()
	kv := newMockKVService()
	kv.store["app/k1"] = []byte("v1")
	kv.store["app/k2"] = []byte("v2")
	tr, err := New(kv, &mockAdminService{}, WithAuthMiddleware(authmw.New(authz, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func TestKVReads_WithoutGetOrRangeAreDenied(t *testing.T) {
	tr := seededKVTransport(t, grantOnlyAuthz(permission.KVPut, permission.KVList))
	ctx := ctxFor(&auth.Identity{Subject: "writer", TenantID: "acme"})

	// Control: the caller is authenticated and holds kv:list, so the store is
	// visibly non-empty to it.
	keys, err := tr.List(ctx, "app/")
	if err != nil || len(keys) != 2 {
		t.Fatalf("List(app/) = %v, %v; want the two seeded keys", keys, err)
	}

	cases := map[string]func() (any, error){
		"get single key": func() (any, error) { return tr.Get(ctx, "app/k1") },
		"prefix scan":    func() (any, error) { return tr.Scan(ctx, "app/") },
		"full scan":      func() (any, error) { return tr.Scan(ctx, "") },
		"range scan": func() (any, error) {
			return tr.Range(ctx, []byte("app/a"), []byte("app/z"), transport.RangeOptions{})
		},
	}
	for name, read := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := read()
			if err == nil {
				t.Fatal("succeeded for a caller holding neither kv:get nor kv:range")
			}
			if !errors.Is(err, auth.ErrUnauthorized) {
				t.Fatalf("err = %v (%T); want the typed permission denial", err, err)
			}
		})
	}
}

// TestKVReads_GrantedAreServed is the control: the same reads with kv:get and
// kv:range return the seeded values.
func TestKVReads_GrantedAreServed(t *testing.T) {
	tr := seededKVTransport(t, grantOnlyAuthz(permission.KVGet, permission.KVRange))
	ctx := ctxFor(&auth.Identity{Subject: "reader", TenantID: "acme"})

	if v, err := tr.Get(ctx, "app/k1"); err != nil || string(v) != "v1" {
		t.Fatalf("Get(app/k1) = %q, %v; want v1", v, err)
	}
	if m, err := tr.Scan(ctx, "app/"); err != nil || len(m) != 2 {
		t.Fatalf("Scan(app/) = %v, %v; want both seeded pairs", m, err)
	}
}
