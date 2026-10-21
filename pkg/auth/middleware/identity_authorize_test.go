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
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
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

// The decision for an identity in a suspended tenant is the refusal its own
// operation would get; the authorizer is never consulted.
func TestEvaluateAuthorization_SubjectInSuspendedTenantIsRefused(t *testing.T) {
	tenants := statusTenants(t)
	g := &grantAuthorizer{grants: map[string]map[string]bool{
		"alice@acme": {string(permission.PermissionsResolve): true, "kv:get": true},
		"bob@acme":   {"kv:get": true},
	}}
	m := New(g, WithTenantStatusReader(tenants))
	ctx := callerCtx(&auth.Identity{Subject: "alice", TenantID: "acme"})
	targets := map[string]*auth.Identity{
		"self":    nil,
		"another": {Subject: "bob", TenantID: "acme"},
	}
	for name, target := range targets {
		if decision, err := m.EvaluateAuthorization(ctx, target, "kv", "get"); err != nil || decision != nil {
			t.Fatalf("%s while active: decision=%v err=%v, want allowed", name, decision, err)
		}
	}
	setStatus(t, tenants, "acme", tenant.StatusSuspended)
	g.evaluated = nil
	decision, err := m.EvaluateAuthorization(ctx, targets["self"], "kv", "get")
	if err != nil {
		t.Fatalf("self: err = %v, want a decision", err)
	}
	wantNotActive(t, "self decision", decision, tenant.StatusSuspended)
	// Asking about another identity is itself an operation of the caller's
	// suspended tenant, so the request is refused rather than answered.
	decision, err = m.EvaluateAuthorization(ctx, targets["another"], "kv", "get")
	if decision != nil {
		t.Fatalf("another: decision = %v, want the request refused", decision)
	}
	wantNotActive(t, "another", err, tenant.StatusSuspended)
	if len(g.evaluated) != 0 {
		t.Fatalf("authorizer consulted for a suspended tenant: %v", g.evaluated)
	}
}

// A status that cannot be read is an error, never a decision: the caller
// retries rather than caching an answer.
func TestEvaluateAuthorization_UnreadableStatusIsAnError(t *testing.T) {
	g := &grantAuthorizer{grants: map[string]map[string]bool{"alice@acme": {"kv:get": true}}}
	m := New(g, WithTenantStatusReader(&countingTenants{err: errStoreDown}))
	decision, err := m.EvaluateAuthorization(callerCtx(&auth.Identity{Subject: "alice", TenantID: "acme"}), nil, "kv", "get")
	var unavailable *StoreUnavailableError
	if !errors.As(err, &unavailable) || decision != nil {
		t.Fatalf("decision=%v err=%T(%v), want *StoreUnavailableError and no decision", decision, err, err)
	}
	if len(g.evaluated) != 0 {
		t.Fatalf("authorizer consulted without a status: %v", g.evaluated)
	}
}

// The system tenant is never gated: its own evaluations are answered while a
// tenant it administers is suspended.
func TestEvaluateAuthorization_SystemTenantNotGated(t *testing.T) {
	tenants := statusTenants(t)
	setStatus(t, tenants, "acme", tenant.StatusSuspended)
	g := &grantAuthorizer{grants: map[string]map[string]bool{"root@" + tenantid.System: {"tenants:read": true}}}
	m := New(g, WithTenantStatusReader(tenants))
	decision, err := m.EvaluateAuthorization(callerCtx(&auth.Identity{Subject: "root", TenantID: tenantid.System}), nil, "tenants", "read")
	if err != nil || decision != nil {
		t.Fatalf("system root while acme is suspended: decision=%v err=%v, want allowed", decision, err)
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
