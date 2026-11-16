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

//go:build !integration

package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// TestNode_OperatorIdentityAccess_ReachesNodeLocalTrail pins the SDK node's
// composition of the operator-access trail (docs/multi-tenancy/architecture.md,
// dual-trail access contract): a tenant without the replicated-audit
// entitlement reads only the node-local trail, so an operator's access to
// that tenant's identity records must land there too, not only in the
// replicated chain the tenant cannot query.
func TestNode_OperatorIdentityAccess_ReachesNodeLocalTrail(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	trail := audit.NewRingBuffer(256)
	n, err := NewStandaloneNode(context.Background(), cfg, []server.ServerOption{server.WithAuditLogger(trail)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	mw := n.AuthMiddleware()
	require.NotNil(t, mw)
	svcNode := n.Services()
	ctx := context.Background()
	// acme is created without the replicated-audit entitlement.
	_, err = svcNode.Tenant.CreateTenant(ctx, "acme", "Acme", tenant.IsolationPrefix)
	require.NoError(t, err)
	systemAdminRoleID := seedAdminRole(t, svcNode.Role, tenantid.System)
	operator := asIdentity(&auth.Identity{Subject: "root", TenantID: tenantid.System, Roles: []string{systemAdminRoleID}})

	// The outcome (admitted or refused) does not matter here: either way
	// the access is recorded for acme.
	_, _ = mw.AuthorizeOperatorIdentityAccess(operator, "acme", permission.UsersReadTenant)

	page, err := trail.Query(ctx, audit.Filter{TenantID: "acme", Action: middleware.OperatorIdentityAccessAction})
	require.NoError(t, err)
	require.NotEmpty(t, page.Events,
		"an operator's access to acme's identity records must reach the node-local trail acme can read")
}
