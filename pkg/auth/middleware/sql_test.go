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
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Authenticate tests
// ---------------------------------------------------------------------------

func TestAuthenticateSQL_Success(t *testing.T) {
	id := &auth.Identity{Subject: "alice", TenantID: "t1"}
	authn := &mockAuthenticator{name: "test-authn", identity: id}
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
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username:  "charlie",
			TenantID:  "t1",
			RoleIDs:   []string{"admin", "operator"},
			Lifecycle: user.LifecycleActive,
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
	authn := &mockAuthenticator{name: "test-authn", identity: id}
	authz := &mockAuthorizer{}
	users := &mockUserServicer{
		user: &user.User{
			Username:  "disabled-user",
			TenantID:  "t1",
			Lifecycle: user.LifecycleDisabled,
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
	authn := &mockAuthenticator{name: "test-authn", identity: id}
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
