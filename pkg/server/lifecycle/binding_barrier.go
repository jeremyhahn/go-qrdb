package lifecycle

import (
	"context"
	"sync/atomic"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// databaseBarrierStorage is the store of a database-barrier binding with
// the database's own barrier layered on it (docs/SPEC.md: one full
// multi-model bundle, the barrier layered onto that binding). Every value
// the engine writes -- KV, collections, indexes, time series, vectors, the
// version log -- is sealed under the database's barrier DEK before it
// reaches the store, and opened again on every read; keys stay plaintext so
// ranges and prefixes still scan. Until the Manager attaches the barrier,
// and while it is sealed, every verb is refused with database.SealedError.
type databaseBarrierStorage struct {
	storage.Storage
	barrier atomic.Pointer[barrierRef]
}

// barrierRef holds the attached barrier so it swaps atomically.
type barrierRef struct{ enc database.BarrierEncryptor }

var _ storage.Storage = (*databaseBarrierStorage)(nil)

// attach layers enc on the store, replacing a barrier attached before.
func (s *databaseBarrierStorage) attach(enc database.BarrierEncryptor) {
	s.barrier.Store(&barrierRef{enc: enc})
}

// open returns the attached barrier, or a *database.SealedError when none is
// attached or it is sealed.
func (s *databaseBarrierStorage) open() (database.BarrierEncryptor, error) {
	ref := s.barrier.Load()
	if ref == nil || ref.enc.IsSealed() {
		return nil, &database.SealedError{}
	}
	return ref.enc, nil
}

func (s *databaseBarrierStorage) check() error {
	_, err := s.open()
	return err
}

// sealed reports whether the store refuses every verb.
func (s *databaseBarrierStorage) sealed() bool { return s.check() != nil }

func barrierGet(r storage.Reader, enc database.BarrierEncryptor, key []byte) ([]byte, error) {
	ct, err := r.Get(key)
	if err != nil || ct == nil {
		return ct, err
	}
	pt, err := enc.Decrypt(nil, ct)
	if err != nil {
		return nil, &database.BarrierDecryptError{Key: string(key), Err: err}
	}
	return pt, nil
}

func barrierSet(w interface{ Set(k, v []byte) error }, enc database.BarrierEncryptor, key, value []byte) error {
	ct, err := enc.Encrypt(nil, value)
	if err != nil {
		return &database.BarrierEncryptError{Key: string(key), Err: err}
	}
	return w.Set(key, ct)
}

func (s *databaseBarrierStorage) reader(r storage.Reader) barrierReader {
	return barrierReader{r: r, s: s}
}

// barrierReader opens the values of r under the store's barrier.
type barrierReader struct {
	r storage.Reader
	s *databaseBarrierStorage
}

func (b barrierReader) Get(key []byte) ([]byte, error) {
	enc, err := b.s.open()
	if err != nil {
		return nil, err
	}
	return barrierGet(b.r, enc, key)
}

func (b barrierReader) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	it := b.r.NewIterator(opts)
	enc, err := b.s.open()
	if err != nil {
		return gatedIterator(it, func() error { return err })
	}
	return &barrierIterator{Iterator: it, enc: enc}
}

func (s *databaseBarrierStorage) Get(key []byte) ([]byte, error) {
	return s.reader(s.Storage).Get(key)
}

func (s *databaseBarrierStorage) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return s.reader(s.Storage).NewIterator(opts)
}

func (s *databaseBarrierStorage) Set(key, value []byte) error {
	enc, err := s.open()
	if err != nil {
		return err
	}
	return barrierSet(s.Storage, enc, key, value)
}

func (s *databaseBarrierStorage) Delete(key []byte) error {
	if err := s.check(); err != nil {
		return err
	}
	return s.Storage.Delete(key)
}

func (s *databaseBarrierStorage) DeleteRange(start, end []byte) error {
	if err := s.check(); err != nil {
		return err
	}
	return s.Storage.DeleteRange(start, end)
}

func (s *databaseBarrierStorage) List(prefix []byte) ([][]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return s.Storage.List(prefix)
}

func (s *databaseBarrierStorage) Exists(key []byte) (bool, error) {
	if err := s.check(); err != nil {
		return false, err
	}
	return s.Storage.Exists(key)
}

func (s *databaseBarrierStorage) Batch() storage.Batch {
	return &barrierBatch{Batch: s.Storage.Batch(), s: s}
}

func (s *databaseBarrierStorage) Snapshot() (storage.Snapshot, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	snap, err := s.Storage.Snapshot()
	if err != nil {
		return nil, err
	}
	return &barrierSnapshot{Snapshot: snap, s: s}, nil
}

// barrierBatch seals the values a batch writes and opens the ones it reads.
type barrierBatch struct {
	storage.Batch
	s *databaseBarrierStorage
}

func (b *barrierBatch) Get(key []byte) ([]byte, error) { return b.s.reader(b.Batch).Get(key) }

func (b *barrierBatch) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return b.s.reader(b.Batch).NewIterator(opts)
}

func (b *barrierBatch) Set(key, value []byte) error {
	enc, err := b.s.open()
	if err != nil {
		return err
	}
	return barrierSet(b.Batch, enc, key, value)
}

func (b *barrierBatch) Delete(key []byte) error {
	if err := b.s.check(); err != nil {
		return err
	}
	return b.Batch.Delete(key)
}

func (b *barrierBatch) DeleteRange(start, end []byte) error {
	if err := b.s.check(); err != nil {
		return err
	}
	return b.Batch.DeleteRange(start, end)
}

func (b *barrierBatch) Commit() error {
	if err := b.s.check(); err != nil {
		return err
	}
	return b.Batch.Commit()
}

// barrierSnapshot opens the values a snapshot reads.
type barrierSnapshot struct {
	storage.Snapshot
	s *databaseBarrierStorage
}

func (b *barrierSnapshot) Get(key []byte) ([]byte, error) { return b.s.reader(b.Snapshot).Get(key) }

func (b *barrierSnapshot) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	return b.s.reader(b.Snapshot).NewIterator(opts)
}

// barrierIterator opens each value it yields; a value that does not open
// ends the iteration with a *database.BarrierDecryptError, never plaintext
// of the ciphertext.
type barrierIterator struct {
	storage.Iterator
	enc database.BarrierEncryptor
	err error
}

func (it *barrierIterator) Value() []byte {
	if it.err != nil {
		return nil
	}
	ct := it.Iterator.Value()
	if ct == nil {
		return nil
	}
	pt, err := it.enc.Decrypt(nil, ct)
	if err != nil {
		it.err = &database.BarrierDecryptError{Key: string(it.Iterator.Key()), Err: err}
		return nil
	}
	return pt
}

func (it *barrierIterator) Valid() bool { return it.err == nil && it.Iterator.Valid() }

func (it *barrierIterator) Error() error {
	if it.err != nil {
		return it.err
	}
	return it.Iterator.Error()
}

// sealedHost is the service host of a barrier binding whose engine is not
// open yet because its barrier is sealed: every call refuses with err.
type sealedHost struct {
	id  string
	err error
}

var _ service.Host = sealedHost{}

func (h sealedHost) ID() string           { return h.id }
func (h sealedHost) RaftAddress() string  { return "" }
func (h sealedHost) NodeIdentity() string { return "" }
func (h sealedHost) SyncPropose(context.Context, uint64, []byte) (sm.Result, error) {
	return sm.Result{}, h.err
}
func (h sealedHost) SyncRead(context.Context, uint64, interface{}) (interface{}, error) {
	return nil, h.err
}
func (h sealedHost) ProposeWithMode(context.Context, uint64, []byte, writemode.Mode) (sm.Result, error) {
	return sm.Result{}, h.err
}
func (h sealedHost) StaleRead(context.Context, uint64, interface{}) (interface{}, error) {
	return nil, h.err
}
func (h sealedHost) ReadWithConsistency(context.Context, uint64, interface{}, consistency.ReadOptions) (interface{}, error) {
	return nil, h.err
}
func (h sealedHost) FollowerRead(context.Context, uint64, interface{}) (interface{}, error) {
	return nil, h.err
}
func (h sealedHost) LeaderID(uint64) (uint64, bool, error) { return 0, false, h.err }
func (h sealedHost) Close() error                          { return nil }

// LayerBarrier layers the database's barrier on this binding's store
// (database.BarrierLayered): the Manager calls it in place of wrapping the
// binding in a database.BarrierBackend, so the binding keeps serving its
// full bundle from its own host. A binding without a database barrier
// ignores it.
func (m *MaterializedBinding) LayerBarrier(enc database.BarrierEncryptor) {
	if m.barrier != nil {
		m.barrier.attach(enc)
	}
}

// IsSealed reports whether the binding's database barrier refuses every
// verb: none is attached yet, or it is sealed.
func (m *MaterializedBinding) IsSealed() bool {
	return m.barrier != nil && m.barrier.sealed()
}

// Ready opens the binding's engine, host and vector service if they are not
// open yet. A barrier binding defers them until its barrier is attached and
// unsealed, since the engine reads the store at open; Ready refuses with
// *database.SealedError until then.
func (m *MaterializedBinding) Ready() error {
	if m.ready.Load() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ready.Load() {
		return nil
	}
	if m.closed {
		return &database.SealedError{}
	}
	if m.barrier != nil {
		if err := m.barrier.check(); err != nil {
			return err
		}
	}
	if err := m.build(); err != nil {
		return err
	}
	m.ready.Store(true)
	return nil
}
