package node

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestNode_NodeKeyLogIsolation_ReadsSharedDatabaseNames proves an SDK node
// wires the node-key log probe over the configured SQL default database and
// the shared registry's databases (go-qrdb#1565): a tenant whose data sits
// in the shared system store only under the SQL default database name, or
// under a database registered after boot, is refused the tightening to
// strict with its own count, and a tenant with no such data passes the
// probe.
func TestNode_NodeKeyLogIsolation_ReadsSharedDatabaseNames(t *testing.T) {
	ctx := context.Background()
	cfg := newTenantIsolationConfig(t)
	cfg.SQL.DefaultDatabase = "sqldefault"

	n, err := NewStandaloneNode(ctx, cfg, nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(volumeEntitlements()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	createDefaultPool(t, n)

	reg := n.sharedRegistry.Load()
	require.NotNil(t, reg, "wireClientAndServer must store the shared registry")
	reg.Register("registered", n.Services().DatabaseServices("registered", 0))

	svcs := n.Services()
	for _, id := range []string{"sqlt", "regt", "clean"} {
		_, err := svcs.Tenant.CreateTenant(ctx, id, id, tenant.IsolationBarrier, tenant.WithBarrierAutoUnseal(true))
		require.NoErrorf(t, err, "CreateTenant(%s)", id)
	}
	require.NoError(t, svcs.KV.Put(ctx, tenant.ComposeNamespace("sqlt", "sqldefault")+"k1", []byte("v")))
	require.NoError(t, svcs.KV.Put(ctx, tenant.ComposeNamespace("regt", "registered")+"k1", []byte("v")))
	// Data under the system database's own name is the fixed probe name,
	// not one this wiring adds; it must not be listed twice.
	require.NotContains(t, n.sharedDatabaseNames(cfg)(), lifecycle.SystemDatabase)

	strict := tenant.TenantPatch{IsolationPolicy: tenant.TenantIsolationPolicyStrict}
	for _, id := range []string{"sqlt", "regt"} {
		_, err := svcs.Tenant.UpdateTenant(ctx, id, strict)
		var nk *tenant.NodeKeyLogsError
		require.Truef(t, errors.As(err, &nk), "UpdateTenant(%s, strict) = %v; want a NodeKeyLogsError", id, err)
		require.Equal(t, id, nk.TenantID)
		require.Equal(t, 1, nk.Count)
	}
	// A tenant with no data under those names passes the node-key log
	// probe. This standalone node keeps every tenant's audit chain on the
	// system shard, so the audit-route check that follows the probe is what
	// refuses it: the probe itself finds nothing.
	_, err = svcs.Tenant.UpdateTenant(ctx, "clean", strict)
	var nk *tenant.NodeKeyLogsError
	require.Falsef(t, errors.As(err, &nk), "UpdateTenant(clean, strict) = %v; want no NodeKeyLogsError", err)
	var route *tenant.SystemAuditRouteError
	require.Truef(t, errors.As(err, &route), "UpdateTenant(clean, strict) = %v; want only the audit-route refusal", err)
}
