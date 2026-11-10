package credentials

import (
	"context"
	"sync"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// captureStore keeps a reference to every buffer it hands a caller from Get
// and to every buffer a caller hands it in a guarded write, so a test can see
// what the caller left in them. While failWrites is set a guarded write fails
// with the error the replicated backend passes up from a shard without a
// leader, after its buffers are captured.
type captureStore struct {
	Store
	mu         sync.Mutex
	gets       map[string][][]byte
	writes     map[string][][]byte
	failWrites bool
}

func (c *captureStore) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := c.Store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gets == nil {
		c.gets = map[string][][]byte{}
	}
	c.gets[key] = append(c.gets[key], value)
	return value, nil
}

func (c *captureStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	c.mu.Lock()
	if c.writes == nil {
		c.writes = map[string][][]byte{}
	}
	for _, w := range writes {
		c.writes[w.Key] = append(c.writes[w.Key], w.Value)
	}
	fail := c.failWrites
	c.mu.Unlock()
	if fail {
		return false, quicraft.ErrShardNotReady
	}
	return c.Store.PutAllIfAllUnchanged(ctx, guards, writes)
}

// reset forgets the buffers captured so far.
func (c *captureStore) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gets, c.writes = nil, nil
}

// requireWiped asserts that at least one buffer was captured under key and
// that every one of them is all zero.
func requireWiped(t *testing.T, captured map[string][][]byte, key string) {
	t.Helper()
	require.NotEmpty(t, captured[key], "no buffer captured for %s", key)
	for _, buf := range captured[key] {
		require.NotEmpty(t, buf)
		assert.Equal(t, make([]byte, len(buf)), buf, "plaintext left in a dropped buffer for %s", key)
	}
}

// An import that fails leaves no copy of the legacy value behind: the buffer
// handed to the rotation is wiped whatever the rotation's error was.
func TestResolveMigrationConflict_FailedImportWipesItsValue(t *testing.T) {
	ctx := context.Background()
	home := &captureStore{Store: newHomeStore(t)}
	legacyA := seal.NewMemoryBackend()
	legacyB := newLegacyBarrier(t)
	writeLegacy(t, legacyA, "db", "v2", "v1", nil)
	writeLegacy(t, legacyB, "db", "kept", "kept-old", nil)
	require.NoError(t, newMigratingService(t, legacyA).BindStore(ctx, home))
	svc, err := newUnboundServiceWith(t).ForTenant(ctx, home, legacyB)
	require.NoError(t, err)
	conflicts, err := svc.ListMigrationConflicts(ctx)
	require.NoError(t, err)
	require.Len(t, conflicts, 1)

	home.reset()
	home.failWrites = true
	err = svc.ResolveMigrationConflict(ctx, "db", MigrationImport)
	require.ErrorIs(t, err, quicraft.ErrShardNotReady)
	requireWiped(t, home.writes, credentialPrefix+"db")

	home.failWrites = false
	require.NoError(t, svc.ResolveMigrationConflict(ctx, "db", MigrationImport))
	assert.Equal(t, "kept", retrieveString(t, svc, "db"), "an import that succeeds stores the value")
}
