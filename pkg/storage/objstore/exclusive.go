package objstore

import "github.com/jeremyhahn/go-qrdb/pkg/storage"

var _ storage.Exclusive = (*Engine)(nil)

// Exclusive implements storage.Exclusive: fn runs with every other write in
// this process excluded, after any pending commit journal is settled, so its
// reads see what a failed batch committed rather than the values the journal
// is about to replace. It takes the locks CompareAndSwap takes. The
// ReadWriter is valid only during the call; fn must not call the engine's own
// methods. Like CompareAndSwap it does not guard against a second process
// writing the bucket.
func (e *Engine) Exclusive(fn func(storage.ReadWriter) error) error {
	leave, err := e.enter()
	if err != nil {
		return err
	}
	defer leave()
	e.journalMu.Lock()
	defer e.journalMu.Unlock()
	if err := e.resolvePendingJournal(); err != nil {
		return err
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if e.reclaimed {
		return &storage.ClosedError{}
	}
	return fn(exclusiveReadWriter{e: e})
}

// exclusiveReadWriter reads and writes through the engine's internal get,
// put and remove; its caller (Exclusive) holds every lock they need.
type exclusiveReadWriter struct {
	e *Engine
}

func (rw exclusiveReadWriter) Get(key []byte) ([]byte, error) {
	return rw.e.get(key)
}

func (rw exclusiveReadWriter) Set(key, value []byte) error {
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	return rw.e.put(key, value)
}

func (rw exclusiveReadWriter) Delete(key []byte) error {
	if len(key) == 0 {
		return storage.ErrEmptyKey
	}
	return rw.e.remove(key)
}
