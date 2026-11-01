package seal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// swapHookBackend runs beforeSwap, once, as a guarded swap is entered and
// before the swap itself: the point at which a passphrase change has read and
// re-wrapped the root key but not yet written it.
type swapHookBackend struct {
	*MemoryBackend
	beforeSwap func()
}

func (s *swapHookBackend) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if hook := s.beforeSwap; hook != nil {
		s.beforeSwap = nil
		hook()
	}
	return s.MemoryBackend.CompareAndSwap(ctx, key, expected, next)
}

// An Unseal that starts while a passphrase change is mid-flight, after a
// concurrent Seal, must not adopt the blob the change is replacing. Had it
// read the old blob before the change's write and adopted it after, the
// load-generation persister would write the old wrapper back: the change
// would report success and be undone, the old passphrase opening the barrier
// again and the new one refused.
func TestChangePassword_UnsealOverlappingTheRewriteCannotRestoreTheOldWrapper(t *testing.T) {
	ctx := t.Context()
	backend := &swapHookBackend{MemoryBackend: NewMemoryBackend()}
	b := newDeferredPassphraseBarrier(t, backend, "p0")

	reached := make(chan struct{})
	changed := make(chan struct{})
	b.rootKeyUnsealWaiting = func() {
		close(reached)
		<-changed
	}
	unsealed := make(chan error, 1)
	backend.beforeSwap = func() {
		require.NoError(t, b.Seal())
		go func() { unsealed <- b.Unseal(ctx, MustCredentials(ctx, []byte("p0"))) }()
		select {
		case <-reached:
		case err := <-unsealed:
			t.Fatalf("Unseal returned %v before reaching the rewrite", err)
		}
	}

	require.NoError(t, b.ChangePassword(ctx, MustCredentials(ctx, []byte("p0")), MustCredentials(ctx, []byte("p1"))))
	close(changed)
	require.Error(t, <-unsealed, "the overlapping Unseal adopted the replaced wrapper")
	b.rootKeyUnsealWaiting = nil

	require.True(t, b.IsSealed())
	require.Error(t, b.Unseal(ctx, MustCredentials(ctx, []byte("p0"))))
	require.NoError(t, b.Unseal(ctx, MustCredentials(ctx, []byte("p1"))))
	requireDiskMatchesLive(t, b)
	requireOpensUnder(t, b, "p1")
	require.NoError(t, b.Seal())
	require.Error(t, b.Unseal(ctx, MustCredentials(ctx, []byte("p0"))))
}

// An Unseal that finds no rewrite in flight takes the rewrite lock without
// waiting, and an Unseal of an already unsealed barrier is refused without
// taking it at all.
func TestUnseal_UncontendedDoesNotWaitOnTheRewrite(t *testing.T) {
	ctx := t.Context()
	b := newDeferredPassphraseBarrier(t, NewMemoryBackend(), "p0")
	b.rootKeyUnsealWaiting = func() { t.Fatal("an uncontended Unseal waited on the rewrite lock") }

	b.rootKeyRewrap.Lock()
	require.ErrorIs(t, b.Unseal(ctx, MustCredentials(ctx, []byte("p0"))), ErrAlreadyUnsealed)
	b.rootKeyRewrap.Unlock()

	requireOpensUnder(t, b, "p0")
	requireDiskMatchesLive(t, b)
	require.True(t, b.rootKeyRewrap.TryLock(), "Unseal left the rewrite lock held")
	b.rootKeyRewrap.Unlock()
}
