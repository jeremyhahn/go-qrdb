package credentials

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// barrierOf returns the StorageBarrier a test service was built over.
func barrierOf(t *testing.T, svc *CredentialService) *seal.StorageBarrier {
	t.Helper()
	st, err := svc.store.current()
	require.NoError(t, err)
	b, ok := st.(*seal.StorageBarrier)
	require.True(t, ok, "store is %T, not a StorageBarrier", st)
	return b
}

func newUnboundService(t *testing.T) *CredentialService {
	t.Helper()
	return newUnboundServiceWith(t)
}

func newUnboundServiceWith(t *testing.T, opts ...Option) *CredentialService {
	t.Helper()
	svc, err := NewCredentialService(&Config{Strategy: StrategyBarrier}, nil, testLogger(), opts...)
	require.NoError(t, err)
	return svc
}

// TestBoundStore_UnboundFailsClosed covers every Store verb before a bind:
// each answers ErrStoreUnavailable and touches nothing.
func TestBoundStore_UnboundFailsClosed(t *testing.T) {
	ctx := context.Background()
	b := newBoundStore(nil)
	for name, call := range map[string]func() error{
		"get":    func() error { _, err := b.Get(ctx, "k"); return err },
		"put":    func() error { return b.Put(ctx, "k", []byte("v")) },
		"delete": func() error { return b.Delete(ctx, "k") },
		"list":   func() error { _, err := b.List(ctx, "k"); return err },
		"put-if-absent": func() error {
			_, err := b.PutIfAbsent(ctx, "k", []byte("v"))
			return err
		},
		"compare-and-swap": func() error {
			_, err := b.CompareAndSwap(ctx, "k", []byte("a"), []byte("b"))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, call(), ErrStoreUnavailable)
		})
	}
}

// TestBoundStore_BoundDelegates covers every Store verb after a bind: each
// reaches the bound store.
func TestBoundStore_BoundDelegates(t *testing.T) {
	ctx := context.Background()
	b := newBoundStore(nil)
	require.NoError(t, b.bind(seal.NewMemoryBackend()))

	stored, err := b.PutIfAbsent(ctx, "k", []byte("v1"))
	require.NoError(t, err)
	assert.True(t, stored)
	swapped, err := b.CompareAndSwap(ctx, "k", []byte("v1"), []byte("v2"))
	require.NoError(t, err)
	assert.True(t, swapped)
	got, err := b.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("v2"), got)
	require.NoError(t, b.Put(ctx, "j", []byte("w")))
	keys, err := b.List(ctx, "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"j", "k"}, keys)
	require.NoError(t, b.Delete(ctx, "k"))
	_, err = b.Get(ctx, "k")
	assert.ErrorIs(t, err, seal.ErrSecretNotFound)
}

func TestBindStore_NilRefused(t *testing.T) {
	svc := newUnboundService(t)
	assert.ErrorIs(t, svc.BindStore(context.Background(), nil), ErrNilStore)
	assert.ErrorIs(t, svc.StoreCredential(context.Background(), "k", []byte("v")), ErrStoreUnavailable)
}

// TestBindStore_AttachesStore is the node's sequence: the service is built
// before the replicated KV exists, fails closed, and serves once bound.
func TestBindStore_AttachesStore(t *testing.T) {
	ctx := context.Background()
	svc := newUnboundService(t)
	_, err := svc.ListCredentials(ctx)
	require.ErrorIs(t, err, ErrStoreUnavailable)

	require.NoError(t, svc.BindStore(context.Background(), seal.NewMemoryBackend()))
	require.NoError(t, svc.StoreCredential(ctx, "k", []byte("v1")))
	names, err := svc.ListCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, names)
}

// TestBindStore_SharedStoreIsOneStore is the property the ruling asks for:
// two services (two nodes) bound to one store see each other's credentials
// and policy, and two concurrent passes rotate a due credential once.
func TestBindStore_SharedStoreIsOneStore(t *testing.T) {
	ctx := context.Background()
	shared := seal.NewMemoryBackend()
	var calls atomic.Int32
	source := WithRotationValueSource(func(_ context.Context, name string) (*secure.Buffer, error) {
		calls.Add(1)
		return secure.NewBufferFromBytes([]byte(name + "-rotated"))
	})
	nodeA := newUnboundServiceWith(t, source)
	nodeB := newUnboundServiceWith(t, source)
	require.NoError(t, nodeA.BindStore(context.Background(), shared))
	require.NoError(t, nodeB.BindStore(context.Background(), shared))

	require.NoError(t, nodeA.StoreCredential(ctx, "k", []byte("v1")))
	policy := RotationPolicy{Enabled: true, Interval: time.Hour, RetainVersions: 2}
	require.NoError(t, nodeA.SetRotationPolicy(ctx, "k", policy))
	names, err := nodeB.ListCredentials(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"k"}, names)
	got, err := nodeB.RotationPolicy(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, policy, got)

	due := time.Now().Add(2 * time.Hour)
	var rotated atomic.Int32
	var wg sync.WaitGroup
	for _, node := range []*CredentialService{nodeA, nodeB} {
		wg.Go(func() {
			n, err := node.RotateDue(ctx, due)
			assert.NoError(t, err)
			rotated.Add(int32(n))
		})
	}
	wg.Wait()
	assert.Equal(t, int32(1), rotated.Load())
	assert.Equal(t, int32(1), calls.Load())
}

// TestBindStore_ReplacesStore covers a rebind, which a node does when its
// lifecycle is rebuilt (an encryption migration): the newer store wins.
func TestBindStore_ReplacesStore(t *testing.T) {
	ctx := context.Background()
	first, second := seal.NewMemoryBackend(), seal.NewMemoryBackend()
	svc := newUnboundService(t)
	require.NoError(t, svc.BindStore(context.Background(), first))
	require.NoError(t, svc.StoreCredential(ctx, "old", []byte("v")))
	require.NoError(t, svc.BindStore(context.Background(), second))

	names, err := svc.ListCredentials(ctx)
	require.NoError(t, err)
	assert.Empty(t, names)
}
