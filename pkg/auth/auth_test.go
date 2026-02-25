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

package auth

import (
	"context"
	"testing"
)

func TestIdentity_IsCrossTenant(t *testing.T) {
	t.Run("empty_tenant_is_cross_tenant", func(t *testing.T) {
		id := &Identity{Subject: "admin", TenantID: ""}
		if !id.IsCrossTenant() {
			t.Fatal("expected IsCrossTenant to return true for empty TenantID")
		}
	})

	t.Run("non_empty_tenant_is_not_cross_tenant", func(t *testing.T) {
		id := &Identity{Subject: "user1", TenantID: "tenant-abc"}
		if id.IsCrossTenant() {
			t.Fatal("expected IsCrossTenant to return false for non-empty TenantID")
		}
	})
}

func TestIdentity_HasRole(t *testing.T) {
	t.Run("found_role", func(t *testing.T) {
		id := &Identity{
			Subject: "user1",
			Roles:   []string{"admin", "reader", "writer"},
		}
		if !id.HasRole("reader") {
			t.Fatal("expected HasRole to return true for existing role")
		}
	})

	t.Run("missing_role", func(t *testing.T) {
		id := &Identity{
			Subject: "user1",
			Roles:   []string{"admin", "reader"},
		}
		if id.HasRole("superadmin") {
			t.Fatal("expected HasRole to return false for non-existent role")
		}
	})

	t.Run("empty_roles", func(t *testing.T) {
		id := &Identity{Subject: "user1"}
		if id.HasRole("admin") {
			t.Fatal("expected HasRole to return false for nil roles slice")
		}
	})
}

func TestNoopAuthorizer_Authorize(t *testing.T) {
	authz := &NoopAuthorizer{}
	id := &Identity{Subject: "anyone"}

	err := authz.Authorize(context.Background(), id, "keys", "write")
	if err != nil {
		t.Fatalf("expected NoopAuthorizer to permit all, got error: %v", err)
	}
}

func TestNoopAuthorizer_Authorize_NilIdentity(t *testing.T) {
	authz := &NoopAuthorizer{}

	err := authz.Authorize(context.Background(), nil, "anything", "delete")
	if err != nil {
		t.Fatalf("expected NoopAuthorizer to permit nil identity, got error: %v", err)
	}
}

func TestWithIdentity_IdentityFromContext(t *testing.T) {
	id := &Identity{
		Subject:  "test-user",
		TenantID: "tenant-1",
		Roles:    []string{"admin"},
		Metadata: map[string]string{"org": "acme"},
	}

	ctx := WithIdentity(context.Background(), id)
	got := IdentityFromContext(ctx)

	if got == nil {
		t.Fatal("expected non-nil identity from context")
	}
	if got.Subject != "test-user" {
		t.Fatalf("expected Subject 'test-user', got %q", got.Subject)
	}
	if got.TenantID != "tenant-1" {
		t.Fatalf("expected TenantID 'tenant-1', got %q", got.TenantID)
	}
	if !got.HasRole("admin") {
		t.Fatal("expected identity to have 'admin' role")
	}
	if got.Metadata["org"] != "acme" {
		t.Fatalf("expected metadata org 'acme', got %q", got.Metadata["org"])
	}
}

func TestIdentityFromContext_Empty(t *testing.T) {
	got := IdentityFromContext(context.Background())
	if got != nil {
		t.Fatalf("expected nil identity from empty context, got %+v", got)
	}
}

func TestAuthError_Error(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		expected := "qrdb/auth: unauthenticated: no valid credentials provided"
		if ErrUnauthenticated.Error() != expected {
			t.Fatalf("expected %q, got %q", expected, ErrUnauthenticated.Error())
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		expected := "qrdb/auth: unauthorized: insufficient permissions"
		if ErrUnauthorized.Error() != expected {
			t.Fatalf("expected %q, got %q", expected, ErrUnauthorized.Error())
		}
	})

	t.Run("no_peer_cert", func(t *testing.T) {
		expected := "qrdb/auth: no_peer_cert: no client certificate in TLS connection"
		if ErrNoPeerCertificate.Error() != expected {
			t.Fatalf("expected %q, got %q", expected, ErrNoPeerCertificate.Error())
		}
	})

	t.Run("invalid_cert", func(t *testing.T) {
		expected := "qrdb/auth: invalid_cert: client certificate is invalid or unverified"
		if ErrInvalidCertificate.Error() != expected {
			t.Fatalf("expected %q, got %q", expected, ErrInvalidCertificate.Error())
		}
	})

	t.Run("custom_error", func(t *testing.T) {
		err := &AuthError{Code: "custom", Message: "something went wrong"}
		expected := "qrdb/auth: custom: something went wrong"
		if err.Error() != expected {
			t.Fatalf("expected %q, got %q", expected, err.Error())
		}
	})
}
