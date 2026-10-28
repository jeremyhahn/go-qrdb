package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two tenants hold the same name: a replica update resolves by tenant and
// name, and changes only that tenant's catalog record.
func TestManager_UpdateReplicasForTenant_SharedNameChangesOnlyThatTenant(t *testing.T) {
	cat := newHookCatalog(t)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	defer func() { _ = mgr.Close() }()

	ctxA, ctxB := tenantContext("tenant-a"), tenantContext("tenant-b")
	_, err := mgr.CreateForTenant(ctxA, validMemoryConfig("orders"))
	require.NoError(t, err)
	_, err = mgr.CreateForTenant(ctxB, validMemoryConfig("orders"))
	require.NoError(t, err)

	require.NoError(t, mgr.UpdateReplicasForTenant(ctxA, "orders", 3))

	metaA, err := cat.Get(ctxA, "tenant-a", "orders")
	require.NoError(t, err)
	assert.Equal(t, 3, metaA.Replicas)
	metaB, err := cat.Get(ctxB, "tenant-b", "orders")
	require.NoError(t, err)
	assert.NotEqual(t, 3, metaB.Replicas, "the other tenant's record was changed")
}

// A tenant that does not hold the name gets *NotFoundError, even when
// another tenant holds it.
func TestManager_UpdateReplicasForTenant_OtherTenantsNameIsNotFound(t *testing.T) {
	cat := newHookCatalog(t)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	defer func() { _ = mgr.Close() }()

	_, err := mgr.CreateForTenant(tenantContext("tenant-a"), validMemoryConfig("orders"))
	require.NoError(t, err)

	var nf *NotFoundError
	require.ErrorAs(t, mgr.UpdateReplicasForTenant(tenantContext("tenant-b"), "orders", 3), &nf)
}
