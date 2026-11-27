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

package wiring

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/group"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// statusOnlyReader is a TenantStatusReader that is NOT an
// IsolationPolicyReader: BuildAuthMiddleware must not install the
// operator-access gate's reader from it.
type statusOnlyReader struct{ gets int }

func (r *statusOnlyReader) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	r.gets++
	return &tenant.TenantInfo{ID: id, Status: tenant.StatusActive}, nil
}

// policyReader is a TenantStatusReader that also reads isolation policy,
// so BuildAuthMiddleware wires it to the operator-access gate as well.
type policyReader struct {
	statusOnlyReader
	policyReads int
}

func (r *policyReader) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	r.policyReads++
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

type firstAdminNone struct{}

func (firstAdminNone) TenantFirstAdmin(context.Context, string) (string, error) { return "", nil }

type groupNone struct{}

func (groupNone) GetGroup(context.Context, string) (*group.Group, error) { return nil, nil }

func TestBuildAuthMiddleware_TenantReadersAndResolversAreAccepted(t *testing.T) {
	tests := []struct {
		name   string
		reader interface {
			GetTenant(context.Context, string) (*tenant.TenantInfo, error)
		}
	}{
		{name: "status reader only", reader: &statusOnlyReader{}},
		{name: "status and isolation policy reader", reader: &policyReader{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
				Enabled:            true,
				RoleService:        adminRoleService(),
				UserService:        &fakeUserService{},
				FirstAdminPins:     firstAdminNone{},
				TenantStatusReader: tc.reader,
				GroupResolver:      groupNone{},
			})
			require.NoError(t, err)
			require.NotNil(t, mw)
			ctx := auth.WithIdentity(context.Background(), adminIdentity())
			require.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))
		})
	}
}
