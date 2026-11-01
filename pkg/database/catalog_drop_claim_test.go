package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A claimed delete of a record that is gone was taken over by the drop that
// deleted it: it is a superseded outcome, never success.
func TestKVCatalog_Delete_ClaimedRecordGoneIsNotFound(t *testing.T) {
	cat := newHookCatalog(t)
	ctx := tenantContext("acme")

	var nf *NotFoundError
	require.ErrorAs(t, cat.Delete(ctx, "acme", "orders", "i1", "c1"), &nf)
	assert.Equal(t, "orders", nf.Name)
}

// An unclaimed delete of an absent record stays idempotent.
func TestKVCatalog_Delete_UnclaimedRecordGoneIsNotAnError(t *testing.T) {
	cat := newHookCatalog(t)
	require.NoError(t, cat.Delete(tenantContext("acme"), "acme", "orders", "i1", ""))
}

// Two nodes drop one database at once. The first passes its incarnation
// check and pauses; the second takes the claim over and deletes the record.
// Exactly one drop reports success: the paused one is superseded, so the
// service releases the database's quota once.
func TestManager_Drop_TakenOverClaimedDropIsSuperseded(t *testing.T) {
	cat := newHookCatalog(t)
	spy := &spyReclaimer{}
	nodeA := NewManager(nil, t.TempDir(), WithKeyspaceReclaimer(spy))
	nodeA.SetCatalog(cat)
	defer func() { _ = nodeA.Close() }()
	nodeB := NewManager(nil, t.TempDir())
	nodeB.SetCatalog(cat.Catalog)
	defer func() { _ = nodeB.Close() }()

	ctx := tenantContext("tenant-a")
	_, err := nodeA.CreateForTenant(ctx, validMemoryConfig("orders"))
	require.NoError(t, err)
	_, err = nodeB.GetForTenant(ctx, "orders")
	require.NoError(t, err)

	// Node A's first catalog read after its reclamation is its incarnation
	// check: node B's whole drop runs between that read and A's delete.
	var dropB error
	ran := false
	spy.before = func() {
		cat.afterGet = func() {
			ran = true
			dropB = nodeB.DropForTenant(ctx, "orders")
		}
	}

	dropA := nodeA.DropForTenant(ctx, "orders")
	require.True(t, ran, "node B's drop never ran inside node A's window")
	require.NoError(t, dropB, "the drop that deleted the record must succeed")
	var superseded *DropSupersededError
	require.ErrorAs(t, dropA, &superseded, "the taken-over drop must not report success")
	assert.Equal(t, "tenant-a", superseded.TenantID)

	var nf *NotFoundError
	_, err = cat.Get(ctx, "tenant-a", "orders")
	require.ErrorAs(t, err, &nf)
}

// A drop no other node interferes with still succeeds and removes the record.
func TestManager_Drop_UncontendedClaimedDropSucceeds(t *testing.T) {
	cat := newHookCatalog(t)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	defer func() { _ = mgr.Close() }()

	ctx := tenantContext("tenant-a")
	_, err := mgr.CreateForTenant(ctx, validMemoryConfig("orders"))
	require.NoError(t, err)
	require.NoError(t, mgr.DropForTenant(ctx, "orders"))

	var nf *NotFoundError
	_, err = cat.Get(ctx, "tenant-a", "orders")
	require.ErrorAs(t, err, &nf)
}
