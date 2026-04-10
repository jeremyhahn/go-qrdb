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
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// LookupSQL tests
// ---------------------------------------------------------------------------

func TestLookupSQL_Found(t *testing.T) {
	tests := []struct {
		key  string
		want permission.Permission
	}{
		{"SELECT kv", permission.KVRange},
		{"INSERT kv", permission.KVPut},
		{"UPDATE kv", permission.KVPut},
		{"DELETE kv", permission.KVDelete},
		{"SELECT status", permission.AdminStatus},
		{"SELECT peers", permission.AdminPeers},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, ok := LookupSQL(tt.key)
			if !ok {
				t.Fatalf("expected mapping for %q", tt.key)
			}
			if got != tt.want {
				t.Errorf("LookupSQL(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestLookupSQL_NotFound(t *testing.T) {
	keys := []string{
		"SHOW tables",
		"SET variable",
		"USE database",
		"BEGIN",
		"COMMIT",
		"ROLLBACK",
		"SELECT unknown",
		"DROP kv",
		"",
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			_, ok := LookupSQL(key)
			if ok {
				t.Errorf("expected no mapping for %q", key)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SQL permissions map integrity tests
// ---------------------------------------------------------------------------

func TestSQLPermissions_AllPermissionsRegistered(t *testing.T) {
	for key, perm := range sqlPermissions {
		if !permission.IsRegistered(perm) {
			t.Errorf("SQL %q maps to unregistered permission %q", key, perm)
		}
	}
}

func TestSQLPermissions_AllKeysUseVerbTableFormat(t *testing.T) {
	for key := range sqlPermissions {
		hasSpace := false
		for i := range key {
			if key[i] == ' ' {
				hasSpace = true
				break
			}
		}
		if !hasSpace {
			t.Errorf("SQL key %q does not use 'VERB table' format", key)
		}
	}
}

func TestSQLPermissions_HaveGRPCCounterparts(t *testing.T) {
	grpcPerms := make(map[permission.Permission]struct{}, len(grpcPermissions))
	for _, perm := range grpcPermissions {
		grpcPerms[perm] = struct{}{}
	}

	for key, perm := range sqlPermissions {
		if _, ok := grpcPerms[perm]; !ok {
			t.Errorf("SQL key %q maps to %q which has no gRPC counterpart", key, perm)
		}
	}
}

// ---------------------------------------------------------------------------
// Authenticate tests
// ---------------------------------------------------------------------------

func TestAuthenticateSQL_Success(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	ctx, identity, err := m.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if identity == nil {
		t.Fatal("expected non-nil identity")
	}
	if identity.Subject != "alice" {
		t.Errorf("expected subject 'alice', got %q", identity.Subject)
	}
	if identity.TenantID != "t1" {
		t.Errorf("expected tenant 't1', got %q", identity.TenantID)
	}

	// Verify identity is stored in context.
	ctxID := auth.IdentityFromContext(ctx)
	if ctxID == nil {
		t.Fatal("expected identity in context")
	}
	if ctxID.Subject != "alice" {
		t.Errorf("expected context subject 'alice', got %q", ctxID.Subject)
	}
}

func TestAuthenticateSQL_Unauthenticated(t *testing.T) {
	authn := &mockAuthenticator{name: "mtls", err: auth.ErrUnauthenticated}
	authz := &mockAuthorizer{}

	m := New(authz, WithAuthenticator(authn))

	_, _, err := m.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticateSQL_NoAuthenticators(t *testing.T) {
	authz := &mockAuthorizer{}
	m := New(authz)

	_, _, err := m.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, auth.ErrUnauthenticated) {
		t.Errorf("expected ErrUnauthenticated, got %v", err)
	}
}

func TestAuthenticateSQL_UserEnrichment(t *testing.T) {
	id := &auth.Identity{Subject: "charlie", TenantID: "t1", Roles: []string{"base"}}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username: "charlie",
			TenantID: "t1",
			RoleIDs:  []string{"admin", "operator"},
			Enabled:  true,
		},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	_, identity, err := m.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(identity.Roles) != 2 {
		t.Fatalf("expected 2 roles, got %d", len(identity.Roles))
	}
	if identity.Roles[0] != "admin" || identity.Roles[1] != "operator" {
		t.Errorf("expected roles [admin operator], got %v", identity.Roles)
	}
}

func TestAuthenticateSQL_UserDisabled(t *testing.T) {
	id := &auth.Identity{Subject: "disabled-user", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username: "disabled-user",
			TenantID: "t1",
			Enabled:  false,
		},
	}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	_, _, err := m.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error for disabled user")
	}

	var ude *UserDisabledError
	if !errors.As(err, &ude) {
		t.Fatalf("expected UserDisabledError, got %T: %v", err, err)
	}
}

func TestAuthenticateSQL_StoreUnavailable(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "mtls", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{err: errors.New("raft not ready")}

	m := New(authz, WithAuthenticator(authn), WithUserService(users))

	_, _, err := m.Authenticate(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}

	var sue *StoreUnavailableError
	if !errors.As(err, &sue) {
		t.Fatalf("expected StoreUnavailableError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// AuthorizeSQL tests
// ---------------------------------------------------------------------------

func TestAuthorizeSQL_Authorized(t *testing.T) {
	authz := &mockAuthorizer{}
	m := New(authz)

	identity := &auth.Identity{Subject: "alice", TenantID: "t1", Roles: []string{"admin"}}

	err := m.AuthorizeSQL(context.Background(), identity, "SELECT kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authz.calls.Load() != 1 {
		t.Errorf("expected 1 authorize call, got %d", authz.calls.Load())
	}
}

func TestAuthorizeSQL_Unauthorized(t *testing.T) {
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}
	m := New(authz)

	identity := &auth.Identity{Subject: "bob", TenantID: "t1"}

	err := m.AuthorizeSQL(context.Background(), identity, "DELETE kv")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestAuthorizeSQL_NoMapping_Public(t *testing.T) {
	authz := &mockAuthorizer{}
	m := New(authz)

	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

	// Unmapped operations are public and should not invoke the authorizer.
	publicOps := []string{"SHOW tables", "SET variable", "USE database", "BEGIN", "COMMIT"}
	for _, op := range publicOps {
		t.Run(op, func(t *testing.T) {
			err := m.AuthorizeSQL(context.Background(), identity, op)
			if err != nil {
				t.Fatalf("unexpected error for public operation %q: %v", op, err)
			}
		})
	}

	if authz.calls.Load() != 0 {
		t.Errorf("expected 0 authorize calls for public operations, got %d", authz.calls.Load())
	}
}

func TestAuthorizeSQL_AllMappedOperations(t *testing.T) {
	tests := []struct {
		key  string
		want permission.Permission
	}{
		{"SELECT kv", permission.KVRange},
		{"INSERT kv", permission.KVPut},
		{"UPDATE kv", permission.KVPut},
		{"DELETE kv", permission.KVDelete},
		{"SELECT status", permission.AdminStatus},
		{"SELECT peers", permission.AdminPeers},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			authz := &mockAuthorizer{}
			al := &mockAuditLogger{}
			m := New(authz, WithAuditLogger(al))

			identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

			err := m.AuthorizeSQL(context.Background(), identity, tt.key)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if authz.calls.Load() != 1 {
				t.Errorf("expected 1 authorize call, got %d", authz.calls.Load())
			}
			if al.calls.Load() != 1 {
				t.Errorf("expected 1 audit call, got %d", al.calls.Load())
			}
		})
	}
}

func TestAuthorizeSQL_WithAuditLogging(t *testing.T) {
	authz := &mockAuthorizer{}
	al := &mockAuditLogger{}
	m := New(authz, WithAuditLogger(al))

	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

	err := m.AuthorizeSQL(context.Background(), identity, "INSERT kv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if al.calls.Load() != 1 {
		t.Fatalf("expected 1 audit call, got %d", al.calls.Load())
	}
	entry := al.entries[0]
	if entry.Subject != "alice" {
		t.Errorf("expected audit subject 'alice', got %q", entry.Subject)
	}
	if entry.Resource != string(permission.KVPut) {
		t.Errorf("expected audit resource %q, got %q", permission.KVPut, entry.Resource)
	}
}

func TestAuthorizeSQL_DeniedWithAudit(t *testing.T) {
	authz := &mockAuthorizer{err: auth.ErrUnauthorized}
	al := &mockAuditLogger{}
	m := New(authz, WithAuditLogger(al))

	identity := &auth.Identity{Subject: "bob", TenantID: "t2"}

	err := m.AuthorizeSQL(context.Background(), identity, "SELECT status")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}

	if al.calls.Load() != 1 {
		t.Fatalf("expected 1 audit call, got %d", al.calls.Load())
	}
}

func TestAuthorizeSQL_EmptyKey(t *testing.T) {
	authz := &mockAuthorizer{}
	m := New(authz)

	identity := &auth.Identity{Subject: "alice", TenantID: "t1"}

	// Empty key should be treated as unmapped (public).
	err := m.AuthorizeSQL(context.Background(), identity, "")
	if err != nil {
		t.Fatalf("unexpected error for empty key: %v", err)
	}
	if authz.calls.Load() != 0 {
		t.Errorf("expected 0 authorize calls, got %d", authz.calls.Load())
	}
}
