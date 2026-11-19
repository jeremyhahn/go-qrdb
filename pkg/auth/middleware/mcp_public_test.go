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
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ---------------------------------------------------------------------------
// MCPToolRegistry public tools
// ---------------------------------------------------------------------------

func TestMCPToolRegistry_RegisterPublic_UnmappedTool(t *testing.T) {
	r := NewMCPToolRegistry()
	if err := r.RegisterPublic("example_login_begin"); err != nil {
		t.Fatalf("RegisterPublic: %v", err)
	}
	if !r.IsPublic("example_login_begin") {
		t.Fatal("example_login_begin should be public")
	}
	if _, ok := r.Lookup("example_login_begin"); ok {
		t.Fatal("a public tool must not gain a permission mapping")
	}
}

func TestMCPToolRegistry_RegisterPublic_HealthEligible(t *testing.T) {
	r := NewMCPToolRegistry()
	r.Register("qrdb_health", permission.Public)
	if err := r.RegisterPublic("qrdb_health"); err != nil {
		t.Fatalf("identity:public must be eligible to be public: %v", err)
	}
	if !r.IsPublic("qrdb_health") {
		t.Fatal("qrdb_health should be public")
	}
}

// admin:health is no longer the public-eligible exception: health is public
// by carrying identity:public, so a tool carrying admin:health is refused.
func TestMCPToolRegistry_RegisterPublic_AdminHealthRefused(t *testing.T) {
	r := NewMCPToolRegistry()
	r.Register("qrdb_health", permission.AdminHealth)
	var refused *PublicMCPToolRefusedError
	if err := r.RegisterPublic("qrdb_health"); !errors.As(err, &refused) {
		t.Fatalf("RegisterPublic = %v, want *PublicMCPToolRefusedError", err)
	}
}

func TestMCPToolRegistry_RegisterPublic_DataPermissionRefused(t *testing.T) {
	// One verb per data family the public surface must never reach.
	perms := map[string]permission.Permission{
		"kv":     permission.KVPut,
		"kv-get": permission.KVGet,
		"admin":  permission.AdminStatus,
	}
	for name, perm := range perms {
		t.Run(name, func(t *testing.T) {
			r := NewMCPToolRegistry()
			r.Register("tool", perm)
			err := r.RegisterPublic("tool")
			var refused *PublicMCPToolRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("want *PublicMCPToolRefusedError, got %T: %v", err, err)
			}
			if refused.Tool != "tool" || refused.Permission != perm {
				t.Fatalf("refusal = %+v, want tool/%s", refused, perm)
			}
			if r.IsPublic("tool") {
				t.Fatal("a refused tool must not become public")
			}
		})
	}
}

func TestMCPToolRegistry_RegisterPublic_Idempotent(t *testing.T) {
	r := NewMCPToolRegistry()
	for i := 0; i < 2; i++ {
		if err := r.RegisterPublic("example_liveness"); err != nil {
			t.Fatalf("declaration %d: %v", i, err)
		}
	}
	if !r.IsPublic("example_liveness") {
		t.Fatal("example_liveness should be public")
	}
}

func TestMCPToolRegistry_IsPublic(t *testing.T) {
	r := NewMCPToolRegistry()
	if err := r.RegisterPublic("example_readiness"); err != nil {
		t.Fatal(err)
	}
	if r.IsPublic("example_liveness") {
		t.Fatal("an undeclared tool must not be public")
	}
	var nilReg *MCPToolRegistry
	if nilReg.IsPublic("example_readiness") {
		t.Fatal("a nil registry has no public tools")
	}
}

func TestMCPToolRegistry_HasPublic(t *testing.T) {
	var nilReg *MCPToolRegistry
	if nilReg.HasPublic() {
		t.Fatal("a nil registry has no public tools")
	}
	r := NewMCPToolRegistry()
	r.Register("qrdb_put", permission.KVPut)
	if r.HasPublic() {
		t.Fatal("a registry with only mapped tools has no public tools")
	}
	if err := r.RegisterPublic("example_login_finish"); err != nil {
		t.Fatal(err)
	}
	if !r.HasPublic() {
		t.Fatal("HasPublic should be true after a declaration")
	}
}

func TestMCPToolRegistry_Register_PublicDataToolPanics(t *testing.T) {
	r := NewMCPToolRegistry()
	if err := r.RegisterPublic("tool"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("registering a data permission on a public tool must panic")
		}
	}()
	r.Register("tool", permission.KVGet)
}

func TestMCPToolRegistry_Register_PublicHealthAllowed(t *testing.T) {
	r := NewMCPToolRegistry()
	if err := r.RegisterPublic("qrdb_health"); err != nil {
		t.Fatal(err)
	}
	r.Register("qrdb_health", permission.Public)
	if perm, ok := r.Lookup("qrdb_health"); !ok || perm != permission.Public {
		t.Fatalf("Lookup = %q, %v", perm, ok)
	}
}

func TestMCPToolRegistry_Covered_IncludesPublic(t *testing.T) {
	r := NewMCPToolRegistry()
	r.Register("qrdb_put", permission.KVPut)
	if err := r.RegisterPublic("example_liveness"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"qrdb_put", "example_liveness"} {
		if !r.Covered(name) {
			t.Errorf("%s should be covered", name)
		}
	}
	if r.Covered("unknown") {
		t.Error("an unregistered tool must not be covered")
	}
}

func TestPublicMCPToolRefusedError_Message(t *testing.T) {
	err := &PublicMCPToolRefusedError{Tool: "qrdb_put", Permission: permission.KVPut}
	msg := err.Error()
	for _, want := range []string{"qrdb_put", string(permission.KVPut), "cannot be public"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

// ---------------------------------------------------------------------------
// AuthorizeMCP / MCPToolVisible on public tools
// ---------------------------------------------------------------------------

func publicTestRegistry(t *testing.T) *MCPToolRegistry {
	t.Helper()
	r := testMCPToolRegistry()
	if err := r.RegisterPublic("example_login_begin"); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAuthorizeMCP_PublicTool_NoIdentity(t *testing.T) {
	denyErr := &auth.AuthError{Code: "unauthorized", Message: "permission denied"}
	m := newTestMiddleware(&mockAuthorizer{err: denyErr})
	if err := m.AuthorizeMCP(context.Background(), publicTestRegistry(t), "example_login_begin"); err != nil {
		t.Fatalf("a public tool must be admitted without an identity: %v", err)
	}
}

func TestAuthorizeMCP_PublicRegistry_DataToolStillDenied(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	if err := m.AuthorizeMCP(context.Background(), publicTestRegistry(t), "qrdb_put"); err == nil {
		t.Fatal("a non-public tool must still require an identity")
	}
}

func TestMCPToolVisible_PublicTool(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{err: &auth.AuthError{Code: "unauthorized"}})
	if !m.MCPToolVisible(context.Background(), publicTestRegistry(t), "example_login_begin") {
		t.Fatal("a public tool must be visible to an anonymous caller")
	}
}

func TestMCPToolVisible_AnonymousNonPublic(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	if m.MCPToolVisible(context.Background(), publicTestRegistry(t), "qrdb_get") {
		t.Fatal("a non-public tool must be hidden from an anonymous caller")
	}
}

func TestMCPToolVisible_IdentityAllowed(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "u1", TenantID: "t1"})
	if !m.MCPToolVisible(ctx, publicTestRegistry(t), "qrdb_get") {
		t.Fatal("a granted tool must be visible")
	}
}

func TestMCPToolVisible_IdentityDenied(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{err: &auth.AuthError{Code: "unauthorized"}})
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "u1", TenantID: "t1"})
	if m.MCPToolVisible(ctx, publicTestRegistry(t), "qrdb_get") {
		t.Fatal("a denied tool must be hidden")
	}
}

func TestMCPToolVisible_UnmappedHidden(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{})
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "u1", TenantID: "t1"})
	if m.MCPToolVisible(ctx, publicTestRegistry(t), "unknown_tool") {
		t.Fatal("an unmapped tool must be hidden (default-deny)")
	}
}
