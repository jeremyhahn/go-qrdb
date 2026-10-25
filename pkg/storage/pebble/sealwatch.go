// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pebble

import (
	"errors"
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/quiesce"
)

// ErrBarrierSealed is the cause a *SealedError carries when the database's
// filesystem reported its barrier sealed before any file was refused.
var ErrBarrierSealed = errors.New("qrdb/storage/pebble: the at-rest barrier is sealed")

// SealedError is returned by SealWatch.Flush and SealWatch.Check when the
// at-rest barrier under the database's filesystem is sealed: no new file can
// be created until it is unsealed, so neither a flush nor the WAL rotation a
// full memtable needs can complete. Err is the barrier's own error, or
// ErrBarrierSealed, which Unwrap exposes to errors.Is.
type SealedError struct {
	Err error
}

func (e *SealedError) Error() string {
	return fmt.Sprintf("qrdb/storage/pebble: at-rest barrier sealed: %v", e.Err)
}

func (e *SealedError) Unwrap() error { return e.Err }

// SealWatch makes a sealed at-rest barrier a terminal condition for one Pebble
// database on an encrypting filesystem. Pebble retries a failed flush or
// compaction at once and forever, reporting every attempt to BackgroundError,
// and DB.Flush waits for a flush that cannot succeed while the barrier is
// sealed. The watch logs the first sealed error of an episode and counts the
// rest, ends the episode at the next flush or compaction that succeeds, and
// gives the database's writers Flush, which returns a *SealedError instead of
// waiting. The writer then stops: closing the database ends Pebble's retries.
//
// A sealed barrier also refuses the WAL file every flush and every memtable
// rotation creates, and Pebble panics when it cannot create one, which ends
// the process. When the filesystem can report its barrier's state (see
// pkg/seal/encfs), Check and Flush ask it first and refuse instead.
type SealWatch struct {
	logger   pebble.Logger
	isSealed func(error) bool
	// barrierSealed reports the filesystem's barrier state; nil when the
	// filesystem cannot report it.
	barrierSealed func() bool
	// gate is the filesystem barrier's quiesce gate, and lease this store's
	// hold on it (registered by Open); nil when the filesystem has none.
	gate  *quiesce.Gate
	lease *quiesce.Lease

	mu         sync.Mutex
	episode    *sealEpisode // the current episode, or the next one to begin
	suppressed uint64       // sealed errors not logged in the episode
}

// sealEpisode is one sealed episode. cause is written once, before c closes,
// and never again, so a waiter that receives from c reads it without the lock:
// resume replaces the watch's episode and leaves this one as it was.
type sealEpisode struct {
	c     chan struct{} // closed when the episode begins
	cause error         // the episode's first sealed error; nil before it begins
}

func newSealEpisode() *sealEpisode { return &sealEpisode{c: make(chan struct{})} }

// NewSealWatch returns a watch that classifies a background error as sealed
// with isSealed and logs to logger. A nil isSealed leaves the classification
// to the filesystem's barrier state alone (see Attach).
// barrierSealedProbe returns the BarrierSealed method of fs, or of the
// filesys.FS that VFS presents as fs (an encfs.FS reaches Pebble that way,
// through WrapVFS), or nil when neither reports its barrier's state.
func barrierSealedProbe(fs vfs.FS) func() bool {
	if p, ok := fs.(interface{ BarrierSealed() bool }); ok {
		return p.BarrierSealed
	}
	if a, ok := fs.(*vfsFS); ok {
		if p, ok := a.fs.(interface{ BarrierSealed() bool }); ok {
			return p.BarrierSealed
		}
	}
	return nil
}

// quiesceGateProbe returns the seal-quiesce gate fs exposes, looking through
// the filesys adapter the same way barrierSealedProbe does; nil when fs has
// no gate.
func quiesceGateProbe(fs vfs.FS) *quiesce.Gate {
	if p, ok := fs.(interface{ Quiesce() *quiesce.Gate }); ok {
		return p.Quiesce()
	}
	if a, ok := fs.(*vfsFS); ok {
		if p, ok := a.fs.(interface{ Quiesce() *quiesce.Gate }); ok {
			return p.Quiesce()
		}
	}
	return nil
}

func NewSealWatch(logger pebble.Logger, isSealed func(error) bool) *SealWatch {
	return &SealWatch{logger: logger, isSealed: isSealed, episode: newSealEpisode()}
}

// Attach installs the watch on opts. Every listener already in opts still
// runs, except that a sealed background error reaches the watch alone, which
// is what bounds its logging. When opts.FS reports its barrier's state, the
// watch asks it before every flush (Flush, Check) and classifies a background
// error raised while the barrier is sealed as sealed.
func (w *SealWatch) Attach(opts *pebble.Options) {
	w.barrierSealed = barrierSealedProbe(opts.FS)
	w.gate = quiesceGateProbe(opts.FS)
	var prev pebble.EventListener
	if opts.EventListener != nil {
		prev = *opts.EventListener
	}
	prev.EnsureDefaults(w.logger)
	l := prev
	l.BackgroundError = func(err error) {
		if !w.observe(err) {
			prev.BackgroundError(err)
		}
	}
	l.FlushEnd = func(info pebble.FlushInfo) {
		if info.Err == nil {
			w.resume()
		}
		prev.FlushEnd(info)
	}
	l.CompactionEnd = func(info pebble.CompactionInfo) {
		if info.Err == nil {
			w.resume()
		}
		prev.CompactionEnd(info)
	}
	opts.EventListener = &l
}

// Err returns a *SealedError while the barrier is sealed, and nil otherwise.
func (w *SealWatch) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.episode.err()
}

// Check returns a *SealedError while a sealed episode lasts or while the
// filesystem reports its barrier sealed, and nil otherwise. A writer calls it
// before any write that may rotate the WAL: Pebble panics when a sealed
// barrier refuses the new WAL file, so the refusal has to come first.
func (w *SealWatch) Check() error {
	if err := w.Err(); err != nil {
		return err
	}
	if w.barrierSealed != nil && w.barrierSealed() {
		return &SealedError{Err: ErrBarrierSealed}
	}
	return nil
}

// Admit checks the barrier as Check does and, when it admits, holds the
// store's quiesce lease until Done, so a Seal waits for the operation before
// the key leaves memory. Once the barrier is sealing it refuses with the same
// *SealedError a sealed barrier gives. Every nil return is paired with one
// Done.
func (w *SealWatch) Admit() error {
	if err := w.lease.Acquire(); err != nil {
		return &SealedError{Err: ErrBarrierSealed}
	}
	if err := w.Check(); err != nil {
		w.lease.Release()
		return err
	}
	return nil
}

// Done ends an operation Admit admitted.
func (w *SealWatch) Done() {
	w.lease.Release()
}

// Close removes the store's lease from the barrier's quiesce gate; call it
// when the watched database closes. It is idempotent.
func (w *SealWatch) Close() {
	if w == nil {
		return
	}
	w.lease.Unregister()
}

// Flush flushes db's memtable and waits for it, like DB.Flush, but returns a
// *SealedError rather than waiting while the barrier is sealed. Every flush
// rotates the WAL, so Flush refuses before calling into Pebble when Admit
// does, and holds the store's quiesce lease until the flush ends. It returns
// nil only when its flush completed: a flush whose wait an
// episode ended reports that episode's error even if the episode is over by
// the time it returns.
func (w *SealWatch) Flush(db *pebble.DB) error {
	if err := w.Admit(); err != nil {
		return err
	}
	defer w.Done()
	w.mu.Lock()
	ep := w.episode
	w.mu.Unlock()
	done, err := db.AsyncFlush()
	if err != nil {
		return err
	}
	return ep.await(done)
}

// await waits for done or for the episode to begin. A flush that completed
// succeeds even if the episode began too; otherwise the episode's cause is
// the result, read from the episode itself and not from the watch, which a
// resume may have moved on since.
func (ep *sealEpisode) await(done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ep.c:
		select {
		case <-done:
			return nil
		default:
			return ep.err()
		}
	}
}

// sealed reports whether err comes of a sealed barrier: the classifier says
// so, or the filesystem reports its barrier sealed while the error is raised.
func (w *SealWatch) sealed(err error) bool {
	if w.isSealed != nil && w.isSealed(err) {
		return true
	}
	return w.barrierSealed != nil && w.barrierSealed()
}

// observe records err and reports whether it was a sealed error, logging only
// the first of an episode.
func (w *SealWatch) observe(err error) bool {
	if !w.sealed(err) {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.episode.cause != nil {
		w.suppressed++
		return true
	}
	w.episode.cause = err
	close(w.episode.c)
	w.logger.Errorf("background error: %s; flushes and compactions stop until the barrier is unsealed", err)
	return true
}

// resume ends an episode once a flush or compaction succeeds again.
func (w *SealWatch) resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.episode.cause == nil {
		return
	}
	w.logger.Infof("barrier unsealed: flushes and compactions resumed after %d further sealed errors", w.suppressed)
	w.episode, w.suppressed = newSealEpisode(), 0
}

// err returns the episode's *SealedError once it has begun, and nil before.
// The caller holds the watch's lock or has received from ep.c.
func (ep *sealEpisode) err() error {
	if ep.cause == nil {
		return nil
	}
	return &SealedError{Err: ep.cause}
}

// Open opens the Pebble database in dir with a SealWatch attached, so a
// sealed at-rest barrier under opts.FS is a typed, terminal error with bounded
// logging rather than a flush that never returns. isSealed classifies a
// background error as the barrier's; it may be nil (see NewSealWatch). opts is
// cloned, not modified; a nil opts opens with Pebble's defaults, and a nil
// Logger is the package's slog logger.
func Open(dir string, opts *pebble.Options, isSealed func(error) bool) (*pebble.DB, *SealWatch, error) {
	o := &pebble.Options{}
	if opts != nil {
		o = opts.Clone()
	}
	if o.Logger == nil {
		o.Logger = NewLogger(nil)
	}
	seals := NewSealWatch(o.Logger, isSealed)
	seals.Attach(o)
	db, err := pebble.Open(dir, o)
	if err != nil {
		return nil, nil, err
	}
	seals.lease = seals.gate.Register(dir)
	return db, seals, nil
}
