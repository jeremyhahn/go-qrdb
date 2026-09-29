package node

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// Is IAM visible across a tenant's databases? Bootstrap writes with no
// database bound; a request carries one.
func TestIAMAcrossDatabases(t *testing.T) {
	ctx := context.Background()
	n := newSharedTenantNode(ctx, t)
	resolver := n.Services().StoreResolver

	// Bootstrap-style: no database in context.
	bootStore, err := resolver.Resolve(ctx, tenantid.System)
	require.NoError(t, err)
	require.NoError(t, bootStore.Put(ctx, kvstore.ReservedPrefixSystem+"users/id/admin", []byte("admin")))

	// Request-style: a database IS bound (configs create a "default" database).
	reqCtx := tenant.WithDatabase(ctx, "default")
	reqStore, err := resolver.Resolve(reqCtx, tenantid.System)
	require.NoError(t, err)

	got, err := reqStore.Get(reqCtx, kvstore.ReservedPrefixSystem+"users/id/admin")
	require.NoError(t, err)
	require.NotNil(t, got,
		"the admin user written at bootstrap is INVISIBLE to a request bound to "+
			"another database — identity is per-TENANT, not per-database")
}
