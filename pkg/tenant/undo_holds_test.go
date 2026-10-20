package tenant

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// A pass acknowledges only the undo it began under: once a rolled-back create
// re-nonced the tombstone, the acknowledgement is that create's to write.
func TestAckUndoneUnder_RefusesALaterUndo(t *testing.T) {
	ctx := context.Background()
	store := seal.NewMemoryBackend()
	ts := putUndone(t, store, &TenantInfo{ID: "acme"})

	var pending *TenantUndoPendingError
	require.True(t, errors.As(ackUndoneUnder(ctx, store, "acme", "node-b", "earlier"), &pending))
	_, err := store.Get(ctx, undoneAckKey("acme", ts.Undo, "node-b"))
	assert.True(t, isAbsent(err), "acknowledged an undo the pass did not clean up: %v", err)

	require.NoError(t, ackUndoneUnder(ctx, store, "acme", "node-b", ts.Undo))
	mustReadAck(t, store.Get, "acme", "node-b")
}

// A pass begun while the tombstone is gone has nothing to do: the id was
// released, and anything under it is a later tenant's.
func TestBeginCleanupPass_ReleasedIDIsDone(t *testing.T) {
	ctx := context.Background()
	svc := undoneService(t, seal.NewMemoryBackend(), "node-b")
	p, done, err := svc.beginCleanupPass(ctx, "acme")
	require.NoError(t, err)
	require.NotNil(t, p)
	assert.True(t, done)
	svc.endCleanupPass(ctx, p)
	assert.False(t, svc.undoAckHolds.held("acme"))
}

// The pass reports a store failure reading this node's acknowledgement.
func TestBeginCleanupPass_ReportsAnAcknowledgementReadFailure(t *testing.T) {
	ctx := context.Background()
	backend := seal.NewMemoryBackend()
	ts := putUndone(t, backend, &TenantInfo{ID: "acme"})
	store := &failingGetStore{MemoryBackend: backend, key: undoneAckKey("acme", ts.Undo, "node-b")}
	svc := undoneService(t, store, "node-b")

	p, _, err := svc.beginCleanupPass(ctx, "acme")
	require.NotNil(t, p)
	defer svc.endCleanupPass(ctx, p)
	var storeErr *StoreError
	require.True(t, errors.As(err, &storeErr), "got %v", err)
	assert.Equal(t, "undo-ack-get", storeErr.Op)
}

// failingGetStore fails every read of key.
type failingGetStore struct {
	*seal.MemoryBackend
	key string
}

func (s *failingGetStore) Get(ctx context.Context, key string) ([]byte, error) {
	if key == s.key {
		return nil, errors.New("get failed")
	}
	return s.MemoryBackend.Get(ctx, key)
}

func TestUndoPassHolds(t *testing.T) {
	t.Run("a pass acknowledges only as the sole holder", func(t *testing.T) {
		var h undoAckHolds
		first, ok := h.pass("acme")
		require.True(t, ok)
		second, ok := h.pass("acme")
		require.True(t, ok)
		assert.False(t, first.beginAck())
		assert.False(t, second.beginAck())
		assert.False(t, first.end(), "a holder remains; nothing is reported yet")
		assert.True(t, second.end(), "the last holder must run the deferred pass")
		assert.False(t, h.held("acme"))
	})

	t.Run("no pass begins while another writes the acknowledgement", func(t *testing.T) {
		var h undoAckHolds
		p, ok := h.pass("acme")
		require.True(t, ok)
		require.True(t, p.beginAck())
		_, ok = h.pass("acme")
		assert.False(t, ok)
		release := h.hold("acme")
		p.endAck()
		later, ok := h.pass("acme")
		require.True(t, ok)
		assert.False(t, release())
		assert.False(t, p.end())
		assert.False(t, later.end())
		assert.False(t, h.held("acme"))
	})

	t.Run("a create or rollback hold withholds the acknowledgement and is told of it", func(t *testing.T) {
		var h undoAckHolds
		release := h.hold("acme")
		p, ok := h.pass("acme")
		require.True(t, ok)
		assert.False(t, p.beginAck())
		assert.False(t, p.end())
		assert.True(t, release(), "the deferred acknowledgement was lost")
		assert.False(t, h.held("acme"))
	})
}
