package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// An undo of the id that lands, is cleaned up on this node and is released
// while a create provisions does not let that create commit: the cleanup
// destroyed what the create provisioned, so the create holds this node's
// acknowledgement until it is over, the undo stays unreleased, and the
// commit meets its tombstone and refuses, leaving no record.
func TestCreateTenant_AnUndoReleasedWhileItProvisionedCannotLetItCommit(t *testing.T) {
	ctx := context.Background()
	store := &undoBeforeCommit{MemoryBackend: seal.NewMemoryBackend()}
	svc, reg := newRollbackService(t, store)
	var releaseErr error
	var released bool
	store.undo = func() {
		putUndone(t, store.MemoryBackend, &TenantInfo{ID: "vund", IsolationType: IsolationVolume}, "node-a")
		_ = svc.ReconcileUndone(ctx, "vund")
		released, releaseErr = releaseUndone(ctx, store.MemoryBackend, rosterOf("node-a", "node-b"), "vund")
	}

	_, err := svc.CreateTenant(ctx, "vund", "Late", IsolationVolume)
	var pending *TenantUndoPendingError
	assert.False(t, released, "the undo was released while a create of its id provisioned")
	require.True(t, errors.As(releaseErr, &pending), "got %v", releaseErr)
	require.True(t, errors.As(err, &pending), "committed over a released undo's cleanup: %v", err)
	_, err = store.Get(ctx, metadataPrefix+"vund")
	assert.True(t, isAbsent(err), "the refused create left its record: %v", err)
	ts := mustReadTombstone(t, store.Get, "vund")
	assert.True(t, ts.Undone())
	_, err = reg.GetTenant("vund")
	assert.ErrorIs(t, err, seal.ErrTenantNotFound, "the refused create left its barrier")

	// The acknowledgement withheld while the create held the id is written
	// once it let go, for the undo's current generation.
	mustReadAck(t, store.Get, "vund", "node-b")
}

// A create that holds the id is not refused by a cleanup pass that withheld
// its acknowledgement: with no undo left at the commit it commits, and the
// withheld acknowledgement finds nothing to do.
func TestCreateTenant_CommitsPastAWithheldAcknowledgementOfAReleasedID(t *testing.T) {
	ctx := context.Background()
	store := &undoBeforeCommit{MemoryBackend: seal.NewMemoryBackend()}
	svc, reg := newRollbackService(t, store)
	store.undo = func() {
		_ = svc.ReconcileUndone(ctx, "vund")
	}

	_, err := svc.CreateTenant(ctx, "vund", "Plain", IsolationVolume)
	require.NoError(t, err)
	_, err = reg.GetTenant("vund")
	require.NoError(t, err)
	_, err = store.Get(ctx, metadataPrefix+"vund")
	require.NoError(t, err)
}
