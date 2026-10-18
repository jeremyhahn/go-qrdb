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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// ---------------------------------------------------------------------------
// mockAuthorizer implements auth.Authorizer for unit tests.
// ---------------------------------------------------------------------------

type mockAuthorizer struct {
	authorizeFn func(ctx context.Context, identity *auth.Identity, resource, action string) error
}

func (m *mockAuthorizer) Authorize(ctx context.Context, identity *auth.Identity, resource, action string) error {
	if m.authorizeFn != nil {
		return m.authorizeFn(ctx, identity, resource, action)
	}
	return nil
}

// ---------------------------------------------------------------------------
// authorize
// ---------------------------------------------------------------------------

// TestAuthorize_UnknownMethodAllowed verifies that a method not present in
// the QUIC permission map is permitted without calling the authorizer.
func TestAuthorize_UnknownMethodDenied(t *testing.T) {
	callCount := 0
	authorizer := &mockAuthorizer{
		authorizeFn: func(_ context.Context, _ *auth.Identity, _, _ string) error {
			callCount++
			return nil
		},
	}
	mw := authmw.New(authorizer, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithAuthMiddleware(mw),
	)

	// DEFAULT-DENY: "unknown.Method" is not in the QUIC permission map, so it
	// is REJECTED rather than allowed through. The fine-grained authorizer is
	// not consulted (the request is denied before the permission check).
	ctx := context.Background()
	err := srv.authorize(ctx, "unknown.Method")
	if err == nil {
		t.Fatal("expected unmapped method to be denied, got nil")
	}
	if callCount != 0 {
		t.Errorf("expected authorizer not to be called for unknown method, got %d calls", callCount)
	}
}

// TestAuthorize_KnownMethodDenied verifies that a method in the QUIC permission
// map results in an AuthorizationError when the authorizer denies the request.
func TestAuthorize_KnownMethodDenied(t *testing.T) {
	denyErr := &auth.AuthError{Code: "unauthorized", Message: "denied"}
	authorizer := &mockAuthorizer{
		authorizeFn: func(_ context.Context, _ *auth.Identity, _, _ string) error {
			return denyErr
		},
	}
	mw := authmw.New(authorizer, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithAuthMiddleware(mw),
	)

	// "kv.Put" is a known QUIC method that requires authorization.
	// Inject an identity so AuthorizeQUIC can proceed to the authorizer.
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "test-user",
		TenantID: "tenant-1",
	})

	err := srv.authorize(ctx, "kv.Put")
	if err == nil {
		t.Fatal("expected AuthorizationError, got nil")
	}

	var authzErr *AuthorizationError
	if !errors.As(err, &authzErr) {
		t.Fatalf("expected *AuthorizationError, got %T: %v", err, err)
	}
	if authzErr.Method != "kv.Put" {
		t.Errorf("expected method 'kv.Put', got %q", authzErr.Method)
	}
}

// TestAuthorize_KnownMethodAllowed verifies that a method in the QUIC
// permission map succeeds when the authorizer permits the request.
func TestAuthorize_KnownMethodAllowed(t *testing.T) {
	authorizer := &mockAuthorizer{
		authorizeFn: func(_ context.Context, _ *auth.Identity, _, _ string) error {
			return nil
		},
	}
	mw := authmw.New(authorizer, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithAuthMiddleware(mw),
	)

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		Subject:  "admin",
		TenantID: "system",
	})

	err := srv.authorize(ctx, "kv.Get")
	if err != nil {
		t.Fatalf("expected nil error for authorized method, got: %v", err)
	}
}

// TestAuthorize_NoIdentityInContext verifies that calling authorize with a
// known method but no identity in context returns an AuthorizationError
// (because AuthorizeQUIC returns an error when identity is nil).
func TestAuthorize_NoIdentityInContext(t *testing.T) {
	authorizer := &mockAuthorizer{}
	mw := authmw.New(authorizer, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithAuthMiddleware(mw),
	)

	// No identity injected, so AuthorizeQUIC will return an unauthenticated error.
	err := srv.authorize(context.Background(), "kv.Delete")
	if err == nil {
		t.Fatal("expected error when no identity in context")
	}

	var authzErr *AuthorizationError
	if !errors.As(err, &authzErr) {
		t.Fatalf("expected *AuthorizationError, got %T: %v", err, err)
	}
}
