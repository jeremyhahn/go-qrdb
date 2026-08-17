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

package wiring

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// stubAuthenticator is an embedder-style credential authenticator: it
// authenticates any context carrying an HTTP Authorization value equal to
// its token, standing in for the OAuth 2.1 bearer validator go-quicbaas
// registers via ExtraAuthenticators (qrdb ships no such validator itself).
type stubAuthenticator struct {
	token string
	calls int
}

func (s *stubAuthenticator) Authenticate(ctx context.Context) (*auth.Identity, error) {
	s.calls++
	if auth.HTTPAuthorizationFromContext(ctx) != s.token {
		return nil, auth.ErrUnauthenticated
	}
	return &auth.Identity{Subject: "bearer-subject", TenantID: tenant.SystemTenantID}, nil
}

func (s *stubAuthenticator) Name() string { return "stub-bearer" }

// TestBuildAuthMiddleware_ExtraAuthenticatorAuthenticates verifies that an
// embedder-supplied authenticator participates in the chain: a request with
// no mTLS peer but a matching Authorization carrier authenticates through
// the extra authenticator.
func TestBuildAuthMiddleware_ExtraAuthenticatorAuthenticates(t *testing.T) {
	stub := &stubAuthenticator{token: "Bearer good"}
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         adminRoleService(),
		UserService:         &fakeUserService{},
		ExtraAuthenticators: []auth.Authenticator{stub},
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	ctx := auth.WithHTTPAuthorization(context.Background(), "Bearer good")
	authCtx, id, err := mw.Authenticate(ctx)
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, "bearer-subject", id.Subject)
	assert.NotNil(t, auth.IdentityFromContext(authCtx))
	assert.Positive(t, stub.calls, "extra authenticator must have been consulted")
}

// TestBuildAuthMiddleware_ExtraAuthenticatorRejects verifies fail-closed
// chaining: when neither mTLS nor the extra authenticator accepts the
// request, authentication fails rather than admitting an identity-less
// request.
func TestBuildAuthMiddleware_ExtraAuthenticatorRejects(t *testing.T) {
	stub := &stubAuthenticator{token: "Bearer good"}
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         adminRoleService(),
		UserService:         &fakeUserService{},
		ExtraAuthenticators: []auth.Authenticator{stub},
	})
	require.NoError(t, err)

	ctx := auth.WithHTTPAuthorization(context.Background(), "Bearer WRONG")
	_, _, err = mw.Authenticate(ctx)
	require.Error(t, err)
}

// TestBuildAuthMiddleware_NilExtraAuthenticatorSkipped verifies a nil entry
// in ExtraAuthenticators is skipped rather than installed (a nil in the
// chain would panic on first use).
func TestBuildAuthMiddleware_NilExtraAuthenticatorSkipped(t *testing.T) {
	stub := &stubAuthenticator{token: "Bearer good"}
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:             true,
		RoleService:         adminRoleService(),
		UserService:         &fakeUserService{},
		ExtraAuthenticators: []auth.Authenticator{nil, stub},
	})
	require.NoError(t, err)

	ctx := auth.WithHTTPAuthorization(context.Background(), "Bearer good")
	_, id, err := mw.Authenticate(ctx)
	require.NoError(t, err)
	require.NotNil(t, id)
}

// denyAllAuthorizer denies every decision, standing in for embedder logic
// wrapped over the built-in stack.
type denyAllAuthorizer struct{ inner auth.Authorizer }

func (d *denyAllAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return errors.New("wrapped: deny all")
}

// TestBuildAuthMiddleware_WrapAuthorizerComposes verifies the wrapper's
// decision replaces the inner stack's: an identity RBAC would admit is
// denied by the wrapping authorizer.
func TestBuildAuthMiddleware_WrapAuthorizerComposes(t *testing.T) {
	var sawInner auth.Authorizer
	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{},
		WrapAuthorizer: func(inner auth.Authorizer) auth.Authorizer {
			sawInner = inner
			return &denyAllAuthorizer{inner: inner}
		},
	})
	require.NoError(t, err)
	require.NotNil(t, sawInner, "wrapper must receive the built inner authorizer")

	ctx := auth.WithIdentity(context.Background(), adminIdentity())
	err = mw.AuthorizeTenant(ctx, tenant.SystemTenantID, permission.AdminStatus)
	require.Error(t, err, "wrapped deny-all must override the RBAC allow")
}

// TestBuildAuthMiddleware_WrapAuthorizerNilFailsClosed verifies a wrapper
// returning nil aborts construction instead of leaving the middleware with
// no authorizer.
func TestBuildAuthMiddleware_WrapAuthorizerNilFailsClosed(t *testing.T) {
	_, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:        true,
		RoleService:    adminRoleService(),
		UserService:    &fakeUserService{},
		WrapAuthorizer: func(auth.Authorizer) auth.Authorizer { return nil },
	})
	require.Error(t, err)
	var missing *MissingServiceError
	assert.ErrorAs(t, err, &missing)
}
