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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// ---------------------------------------------------------------------------
// QUICMethodRegistry semantics
// ---------------------------------------------------------------------------

func TestQUICMethodRegistry_RegisterLookup(t *testing.T) {
	r := NewQUICMethodRegistry()
	r.Register("kv.Put", permission.KVPut)

	got, ok := r.Lookup("kv.Put")
	if !ok || got != permission.KVPut {
		t.Fatalf("Lookup(kv.Put) = (%q, %v), want (%q, true)", got, ok, permission.KVPut)
	}
	if !r.Covered("kv.Put") {
		t.Error("Covered(kv.Put) = false, want true")
	}
	if _, ok := r.Lookup("kv.Unknown"); ok {
		t.Error("Lookup(kv.Unknown) found, want default-deny not-found")
	}
	if r.Covered("kv.Unknown") {
		t.Error("Covered(kv.Unknown) = true, want false")
	}
}

func TestQUICMethodRegistry_NilFailsClosed(t *testing.T) {
	var r *QUICMethodRegistry
	if _, ok := r.Lookup("kv.Put"); ok {
		t.Error("nil registry Lookup found a method; must fail closed")
	}
	if r.Covered("kv.Put") {
		t.Error("nil registry Covered = true; must fail closed")
	}
	if r.Entries() != nil {
		t.Error("nil registry Entries() != nil")
	}
}

func TestQUICMethodRegistry_DuplicatePanics(t *testing.T) {
	r := NewQUICMethodRegistry()
	r.Register("kv.Put", permission.KVPut)
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate method registration")
		}
	}()
	r.Register("kv.Put", permission.KVGet)
}

func TestQUICMethodRegistry_EmptyPermissionPanics(t *testing.T) {
	r := NewQUICMethodRegistry()
	defer func() {
		if recover() == nil {
			t.Error("expected panic on empty permission registration")
		}
	}()
	r.Register("kv.Put", "")
}

func TestQUICMethodRegistry_EntriesIsACopy(t *testing.T) {
	r := NewQUICMethodRegistry()
	r.Register("kv.Put", permission.KVPut)

	entries := r.Entries()
	if len(entries) != 1 || entries["kv.Put"] != permission.KVPut {
		t.Fatalf("Entries() = %v, want {kv.Put: %q}", entries, permission.KVPut)
	}
	// Mutating the snapshot must not affect the registry.
	entries["kv.Put"] = permission.KVGet
	if got, _ := r.Lookup("kv.Put"); got != permission.KVPut {
		t.Errorf("registry mutated through Entries() snapshot: got %q", got)
	}
}

// ---------------------------------------------------------------------------
// MCPToolRegistry semantics
// ---------------------------------------------------------------------------

func TestMCPToolRegistry_RegisterLookup(t *testing.T) {
	r := NewMCPToolRegistry()
	r.Register("qrdb_put", permission.KVPut)

	got, ok := r.Lookup("qrdb_put")
	if !ok || got != permission.KVPut {
		t.Fatalf("Lookup(qrdb_put) = (%q, %v), want (%q, true)", got, ok, permission.KVPut)
	}
	if !r.Covered("qrdb_put") {
		t.Error("Covered(qrdb_put) = false, want true")
	}
	if _, ok := r.Lookup("qrdb_unknown"); ok {
		t.Error("Lookup(qrdb_unknown) found, want default-deny not-found")
	}
}

func TestMCPToolRegistry_NilFailsClosed(t *testing.T) {
	var r *MCPToolRegistry
	if _, ok := r.Lookup("qrdb_put"); ok {
		t.Error("nil registry Lookup found a tool; must fail closed")
	}
	if r.Covered("qrdb_put") {
		t.Error("nil registry Covered = true; must fail closed")
	}
	if r.Entries() != nil {
		t.Error("nil registry Entries() != nil")
	}
}

func TestMCPToolRegistry_DuplicatePanics(t *testing.T) {
	r := NewMCPToolRegistry()
	r.Register("qrdb_put", permission.KVPut)
	defer func() {
		if recover() == nil {
			t.Error("expected panic on duplicate tool registration")
		}
	}()
	r.Register("qrdb_put", permission.KVGet)
}

func TestMCPToolRegistry_EmptyPermissionPanics(t *testing.T) {
	r := NewMCPToolRegistry()
	defer func() {
		if recover() == nil {
			t.Error("expected panic on empty permission registration")
		}
	}()
	r.Register("qrdb_put", "")
}

// TestAuthorizeMCP_NilRegistryDenied verifies the fail-closed contract for an
// unwired tool registry: every tool call is denied.
func TestAuthorizeMCP_NilRegistryDenied(t *testing.T) {
	m := New(&mockAuthorizer{})
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "u1",
		TenantID: "t1",
		Roles:    []string{"admin"},
	})
	if err := m.AuthorizeMCP(ctx, nil, "qrdb_put"); err == nil {
		t.Fatal("expected nil registry to deny, got nil error")
	}
}
