package credentials

import (
	"context"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Store is the record store the barrier strategy keeps every credential
// record in: values, version history and the metadata record that carries
// the rotation policy and claim. A node binds the system shard's replicated
// KV, whose PutIfAbsent and CompareAndSwap are Raft transactions, so a
// conditional write decides once for the whole cluster; a tenant's store is
// its home store (NewKVStore), replicated the same way under the tenant's
// own key. Get reports a missing
// key with seal.ErrSecretNotFound.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
}

var (
	_ Store = (*seal.StorageBarrier)(nil)
	_ Store = (*boundStore)(nil)
	_ Store = kvStore{}
)

// kvStore is a Store over a kvstore.AtomicKVStore, the shape of a tenant's
// home store. kvstore reports a missing key as nil, nil; a Store reports it
// as seal.ErrSecretNotFound.
type kvStore struct{ kvstore.AtomicKVStore }

// NewKVStore returns a Store over kv, which must serve conditional writes
// (kvstore.AtomicKVStore): a tenant's home store on a cluster decides them
// in its Raft group, so a rotation claim taken through one node fences every
// other. It returns ErrNilStore for a nil kv and ErrStoreNotAtomic for one
// that serves no conditional writes.
func NewKVStore(kv kvstore.KVStore) (Store, error) {
	if kv == nil {
		return nil, ErrNilStore
	}
	atomicKV, ok := kv.(kvstore.AtomicKVStore)
	if !ok {
		return nil, ErrStoreNotAtomic
	}
	return kvStore{atomicKV}, nil
}

// Get returns key's value, or seal.ErrSecretNotFound when it is absent.
func (s kvStore) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := s.AtomicKVStore.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, seal.ErrSecretNotFound
	}
	return data, nil
}

// storeBox lets an interface value sit behind an atomic.Pointer.
type storeBox struct{ Store }

// boundStore is the Store a service holds. A node builds the credential
// service with its seal subsystem, before the system shard's KV exists, and
// binds the replicated store once it does; until then every call fails
// closed with ErrStoreUnavailable. The swap is lock-free, so a bind never
// blocks a call in flight.
type boundStore struct {
	p atomic.Pointer[storeBox]
}

// newBoundStore returns a boundStore over st, or an unbound one for a nil st.
func newBoundStore(st Store) *boundStore {
	b := &boundStore{}
	if st != nil {
		b.p.Store(&storeBox{st})
	}
	return b
}

// bind attaches st; a nil st is refused with ErrNilStore.
func (b *boundStore) bind(st Store) error {
	if st == nil {
		return ErrNilStore
	}
	b.p.Store(&storeBox{st})
	return nil
}

// current returns the bound store, or ErrStoreUnavailable.
func (b *boundStore) current() (Store, error) {
	box := b.p.Load()
	if box == nil {
		return nil, ErrStoreUnavailable
	}
	return box.Store, nil
}

// Get implements Store.
func (b *boundStore) Get(ctx context.Context, key string) ([]byte, error) {
	st, err := b.current()
	if err != nil {
		return nil, err
	}
	return st.Get(ctx, key)
}

// Put implements Store.
func (b *boundStore) Put(ctx context.Context, key string, value []byte) error {
	st, err := b.current()
	if err != nil {
		return err
	}
	return st.Put(ctx, key, value)
}

// Delete implements Store.
func (b *boundStore) Delete(ctx context.Context, key string) error {
	st, err := b.current()
	if err != nil {
		return err
	}
	return st.Delete(ctx, key)
}

// List implements Store.
func (b *boundStore) List(ctx context.Context, prefix string) ([]string, error) {
	st, err := b.current()
	if err != nil {
		return nil, err
	}
	return st.List(ctx, prefix)
}

// PutIfAbsent implements Store.
func (b *boundStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	st, err := b.current()
	if err != nil {
		return false, err
	}
	return st.PutIfAbsent(ctx, key, value)
}

// CompareAndSwap implements Store.
func (b *boundStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	st, err := b.current()
	if err != nil {
		return false, err
	}
	return st.CompareAndSwap(ctx, key, expected, next)
}
