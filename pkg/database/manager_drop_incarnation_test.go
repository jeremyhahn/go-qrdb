package database

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestManager_DropIncarnationForTenant_RefusesARecreatedName pins the drop a
// strict-refused create's rollback retry uses: it is bound to the refused
// database's ID and incarnation, so once the name holds a recreated
// database the retry's drop is refused and the recreated database stays.
func TestManager_DropIncarnationForTenant_RefusesARecreatedName(t *testing.T) {
	mgr := newCatalogManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	ctx := tenant.WithTenantID(context.Background(), "t1")

	refused, err := mgr.CreateForTenant(ctx, DatabaseConfig{Name: "app", Backend: BackendMemory})
	require.NoError(t, err)
	refusedID, refusedInc := refused.ID(), refused.Incarnation()
	require.NoError(t, mgr.DropForTenant(ctx, "app"))
	replacement, err := mgr.CreateForTenant(ctx, DatabaseConfig{Name: "app", Backend: BackendMemory})
	require.NoError(t, err)

	err = mgr.DropIncarnationForTenant(ctx, "app", refusedID, refusedInc)
	var superseded *DropSupersededError
	require.True(t, errors.As(err, &superseded), "drop of the refused incarnation = %v, want *DropSupersededError", err)

	got, err := mgr.GetForTenant(ctx, "app")
	require.NoError(t, err, "the recreated database must survive")
	require.Equal(t, replacement.ID(), got.ID())
	require.Equal(t, replacement.Incarnation(), got.Incarnation())

	require.NoError(t, mgr.DropIncarnationForTenant(ctx, "app", replacement.ID(), replacement.Incarnation()))
	_, err = mgr.GetForTenant(ctx, "app")
	var notFound *NotFoundError
	require.True(t, errors.As(err, &notFound), "read after the bound drop = %v, want *NotFoundError", err)
}
