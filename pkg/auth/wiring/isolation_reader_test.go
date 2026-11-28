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

// statusOnly is a tenant record reader without the isolation-policy reads.
type statusOnly struct{}

func (statusOnly) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Name: id}, nil
}

// statusAndPolicy also answers the cluster isolation floor, so the composer
// wires it as the operator-access gate's isolation-policy reader.
type statusAndPolicy struct {
	statusOnly
	policyReads int
}

func (s *statusAndPolicy) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	s.policyReads++
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

// noFirstAdmin has no pinned first admin for any tenant.
type noFirstAdmin struct{}

func (noFirstAdmin) TenantFirstAdmin(context.Context, string) (string, error) { return "", nil }

// noGroups resolves no IAM group.
type noGroups struct{}

func (noGroups) GetGroup(context.Context, string) (*group.Group, error) { return nil, nil }

// TestBuildAuthMiddleware_TenantReadersAreWired: a tenant record store with
// or without the isolation-policy reads, the first-admin pins and the group
// store are optional inputs; each builds a middleware that still authorizes
// through the primary services.
func TestBuildAuthMiddleware_TenantReadersAreWired(t *testing.T) {
	cases := []struct {
		name   string
		reader interface {
			GetTenant(context.Context, string) (*tenant.TenantInfo, error)
		}
	}{
		{name: "status reader only", reader: statusOnly{}},
		{name: "status and isolation-policy reader", reader: &statusAndPolicy{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mw, err := BuildAuthMiddleware(AuthMiddlewareConfig{
				Enabled:            true,
				RoleService:        adminRoleService(),
				UserService:        &fakeUserService{},
				TenantStatusReader: tc.reader,
				FirstAdminPins:     noFirstAdmin{},
				GroupResolver:      noGroups{},
			})
			require.NoError(t, err)
			require.NotNil(t, mw)
			ctx := auth.WithIdentity(context.Background(), adminIdentity())
			require.NoError(t, mw.AuthorizeTenant(ctx, tenantid.System, permission.AdminStatus))
		})
	}
}
