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

package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// grantAuthorizer permits subject@tenant exactly the "resource:action" pairs
// listed for it, in its own tenant only -- the confinement the RBAC
// authorizer applies to a tenant-scoped grant -- and records every identity
// it evaluated.
type grantAuthorizer struct {
	grants    map[string]map[string]bool
	evaluated []*auth.Identity
}

var _ auth.Authorizer = (*grantAuthorizer)(nil)

func (g *grantAuthorizer) Authorize(ctx context.Context, id *auth.Identity, resource, action string) error {
	g.evaluated = append(g.evaluated, id)
	if target := tenant.FromContext(ctx); target != "" && target != id.TenantID {
		return auth.ErrUnauthorized
	}
	if g.grants[id.Subject+"@"+id.TenantID][resource+":"+action] {
		return nil
	}
	return auth.ErrUnauthorized
}

// callerCtx authenticates id and targets its own tenant, the context every
// transport hands a handler.
func callerCtx(id *auth.Identity) context.Context {
	return tenant.WithTenantID(auth.WithIdentity(context.Background(), id), id.TenantID)
}

func newEvalMiddleware(grants map[string]map[string]bool) (*AuthMiddleware, *grantAuthorizer) {
	g := &grantAuthorizer{grants: grants}
	return New(g), g
}

func TestEvaluateAuthorization_SelfNeedsOnlyAuthentication(t *testing.T) {
	m, g := newEvalMiddleware(map[string]map[string]bool{"alice@t1": {"kv:get": true}})
	caller := &auth.Identity{Subject: "alice", TenantID: "t1", Roles: []string{"reader"}}
	ctx := callerCtx(caller)

	decision, err := m.EvaluateAuthorization(ctx, &auth.Identity{Subject: "alice", TenantID: "t1", Roles: []string{"admin"}}, "kv", "get")
	if err != nil || decision != nil {
		t.Fatalf("self allowed: decision=%v err=%v", decision, err)
	}
	if got := g.evaluated[len(g.evaluated)-1]; got != caller {
		t.Fatalf("self evaluated %+v, want the authenticated caller, not the supplied roles", got)
	}
	decision, err = m.EvaluateAuthorization(ctx, nil, "kv", "put")
	if err != nil || !errors.Is(decision, auth.ErrUnauthorized) {
		t.Fatalf("self denied: decision=%v err=%v", decision, err)
	}
}

func TestEvaluateAuthorization_RefusesAnUnauthenticatedCaller(t *testing.T) {
	m, _ := newEvalMiddleware(nil)
	if _, err := m.EvaluateAuthorization(context.Background(), nil, "kv", "get"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	var nilMW *AuthMiddleware
	ctx := callerCtx(&auth.Identity{Subject: "alice", TenantID: "t1"})
	if _, err := nilMW.EvaluateAuthorization(ctx, nil, "kv", "get"); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("nil middleware err = %v, want ErrUnauthenticated", err)
	}
}

func TestEvaluateAuthorization_AnotherIdentityNeedsPermissionsResolve(t *testing.T) {
	m, g := newEvalMiddleware(map[string]map[string]bool{"bob@t1": {"kv:get": true}})
	ctx := callerCtx(&auth.Identity{Subject: "alice", TenantID: "t1"})
	_, err := m.EvaluateAuthorization(ctx, &auth.Identity{Subject: "bob", TenantID: "t1"}, "kv", "get")
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("err = %v, want the caller refused", err)
	}
	for _, id := range g.evaluated {
		if id.Subject == "bob" {
			t.Fatal("the target was evaluated although the caller may not ask")
		}
	}
}

func TestEvaluateAuthorization_AnotherIdentityWithPermissionsResolve(t *testing.T) {
	resolve := string(permission.PermissionsResolve)
	m, _ := newEvalMiddleware(map[string]map[string]bool{
		"alice@t1": {resolve: true},
		"bob@t1":   {"kv:get": true},
	})
	ctx := callerCtx(&auth.Identity{Subject: "alice", TenantID: "t1"})
	bob := &auth.Identity{Subject: "bob", TenantID: "t1"}
	if decision, err := m.EvaluateAuthorization(ctx, bob, "kv", "get"); err != nil || decision != nil {
		t.Fatalf("bob kv:get: decision=%v err=%v", decision, err)
	}
	if decision, err := m.EvaluateAuthorization(ctx, bob, "kv", "put"); err != nil || !errors.Is(decision, auth.ErrUnauthorized) {
		t.Fatalf("bob kv:put: decision=%v err=%v", decision, err)
	}
}

func TestEvaluateAuthorization_NeverAnswersForAnotherTenant(t *testing.T) {
	resolve := string(permission.PermissionsResolve)
	m, g := newEvalMiddleware(map[string]map[string]bool{
		"alice@t1": {resolve: true},
		"carol@t2": {"kv:get": true},
	})
	ctx := callerCtx(&auth.Identity{Subject: "alice", TenantID: "t1"})
	if _, err := m.EvaluateAuthorization(ctx, &auth.Identity{Subject: "carol", TenantID: "t2"}, "kv", "get"); err == nil {
		t.Fatal("a t1 caller got an answer about a t2 identity")
	}
	for _, id := range g.evaluated {
		if id.Subject == "carol" {
			t.Fatal("the other tenant's identity was evaluated")
		}
	}
}

func TestEvaluateAuthorization_RejectsAMalformedTarget(t *testing.T) {
	m, _ := newEvalMiddleware(nil)
	ctx := callerCtx(&auth.Identity{Subject: "alice", TenantID: "t1"})
	var invalid *InvalidAuthorizeTargetError
	if _, err := m.EvaluateAuthorization(ctx, &auth.Identity{Subject: "bob"}, "kv", "get"); !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want InvalidAuthorizeTargetError", err)
	}
	if invalid.Error() == "" {
		t.Fatal("empty error string")
	}
}
