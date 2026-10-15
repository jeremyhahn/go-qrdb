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
		if id.HasRole("custodian") {
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

func TestUserSubject(t *testing.T) {
	if got := UserSubject(7302841958340608001); got != "7302841958340608001" {
		t.Fatalf("UserSubject = %q, want the id in base 10", got)
	}
	if got := UserSubject(1); got != "1" {
		t.Fatalf("UserSubject(1) = %q, want %q", got, "1")
	}
}

func TestParseUserSubject(t *testing.T) {
	cases := []struct {
		subject string
		want    uint64
		ok      bool
	}{
		{"7302841958340608001", 7302841958340608001, true},
		{"1", 1, true},
		{"0", 0, false},
		{"", 0, false},
		{"alice", 0, false},
		{"012", 0, false},
		{"+12", 0, false},
		{"-12", 0, false},
		{" 12", 0, false},
		{"12a", 0, false},
		{"18446744073709551616", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseUserSubject(tc.subject)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseUserSubject(%q) = (%d, %v), want (%d, %v)", tc.subject, got, ok, tc.want, tc.ok)
		}
	}
}

func TestIdentity_BindUser(t *testing.T) {
	t.Run("sets_id_subject_and_name", func(t *testing.T) {
		id := &Identity{Subject: "alice", TenantID: "acme"}
		id.BindUser(42, "alice")
		if id.UserID != 42 || id.Subject != "42" || id.Username != "alice" {
			t.Fatalf("BindUser left %+v, want UserID 42, Subject %q, Username %q", id, "42", "alice")
		}
		if id.TenantID != "acme" {
			t.Fatalf("BindUser changed TenantID to %q", id.TenantID)
		}
	})

	t.Run("subject_survives_a_rename", func(t *testing.T) {
		before := &Identity{}
		before.BindUser(42, "alice")
		after := &Identity{}
		after.BindUser(42, "alice-renamed")
		if before.Subject != after.Subject {
			t.Fatalf("Subject changed with the username: %q then %q", before.Subject, after.Subject)
		}
	})
}

func TestIdentity_AccountName(t *testing.T) {
	t.Run("resolved_account", func(t *testing.T) {
		id := &Identity{Subject: "alice"}
		id.BindUser(42, "alice")
		if got := id.AccountName(); got != "alice" {
			t.Fatalf("AccountName = %q, want the account's username", got)
		}
	})

	t.Run("unresolved_credential", func(t *testing.T) {
		id := &Identity{Subject: "cn-from-cert"}
		if got := id.AccountName(); got != "cn-from-cert" {
			t.Fatalf("AccountName = %q, want the credential's own name", got)
		}
	})

	t.Run("nil_identity", func(t *testing.T) {
		var id *Identity
		if got := id.AccountName(); got != "" {
			t.Fatalf("AccountName on nil = %q, want empty", got)
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
