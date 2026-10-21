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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// fakeTenantGetter answers GetTenant from a fixed set of tenants, the way
// tenant.TenantService does: ErrTenantNotFound for a tenant never created.
type fakeTenantGetter struct {
	tenants map[string]bool
	err     error
	// nilInfo makes a found tenant answer (nil, nil).
	nilInfo bool
}

var _ TenantGetter = (*fakeTenantGetter)(nil)

func (g *fakeTenantGetter) GetTenant(_ context.Context, tenantID string) (*tenant.TenantInfo, error) {
	if g.err != nil {
		return nil, g.err
	}
	if !g.tenants[tenantID] {
		return nil, tenant.ErrTenantNotFound
	}
	if g.nilInfo {
		return nil, nil
	}
	return &tenant.TenantInfo{ID: tenantID, Name: tenantID}, nil
}

func TestTenantDirectory_TenantExists(t *testing.T) {
	cause := errors.New("tenant store unavailable")
	cases := map[string]struct {
		getter     TenantGetter
		wantExists bool
		wantErr    error
	}{
		"no tenant service":  {nil, false, nil},
		"tenant created":     {&fakeTenantGetter{tenants: map[string]bool{"acme": true}}, true, nil},
		"tenant not created": {&fakeTenantGetter{tenants: map[string]bool{"other": true}}, false, nil},
		"lookup fails":       {&fakeTenantGetter{err: cause}, false, cause},
		"no record returned": {&fakeTenantGetter{tenants: map[string]bool{"acme": true}, nilInfo: true}, false, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			exists, err := tenantDirectory{tenants: tc.getter}.TenantExists(context.Background(), "acme")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantExists, exists)
		})
	}
}

// tenantCertResponse sends the revocation fixture's tenant certificate
// through the production auth stack wired over tenants, and reports the
// status and whether the handler ran.
func tenantCertResponse(t *testing.T, tenants TenantGetter) (int, bool) {
	t.Helper()
	const username, tenantID = "alice", "acme"
	f := newRevocationFixture(t, username, tenantID)

	mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
		Enabled:     true,
		RoleService: adminRoleService(),
		UserService: &fakeUserService{u: &user.User{
			Username:  username,
			TenantID:  tenantID,
			RoleIDs:   []string{testAdminRoleID},
			Lifecycle: user.LifecycleActive,
		}},
		CertProvider: f.provider,
		Tenants:      tenants,
		// The fixed fake user carries no cert bindings; as in the revocation
		// tests, "enrolled" is the explicit choice for that reason (#548).
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
	})
	require.NoError(t, err)

	reached := false
	handler := mw.RESTMiddleware(statusRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, f.request())
	return rec.Code, reached
}

func TestBuildAuthMiddleware_CertificateForUnknownTenantIsUnauthenticated(t *testing.T) {
	for name, tenants := range map[string]TenantGetter{
		"no tenant service":  nil,
		"tenant not created": &fakeTenantGetter{tenants: map[string]bool{"other": true}},
	} {
		t.Run(name, func(t *testing.T) {
			code, reached := tenantCertResponse(t, tenants)
			assert.Equal(t, http.StatusUnauthorized, code)
			assert.False(t, reached, "the handler ran for a certificate naming a tenant that does not exist")
		})
	}
}

func TestBuildAuthMiddleware_CertificateForKnownTenantAuthenticates(t *testing.T) {
	code, reached := tenantCertResponse(t, &fakeTenantGetter{tenants: map[string]bool{"acme": true}})
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, reached, "a certificate for a created tenant did not reach the handler")
}

func TestBuildAuthMiddleware_TenantLookupFailureIsRetryable(t *testing.T) {
	code, reached := tenantCertResponse(t, &fakeTenantGetter{err: errors.New("tenant store unavailable")})
	assert.Equal(t, http.StatusServiceUnavailable, code)
	assert.False(t, reached)
}
