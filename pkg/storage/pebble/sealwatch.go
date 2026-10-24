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

	mu         sync.Mutex
	sealedC    chan struct{} // closed when an episode begins
	cause      error         // the episode's first sealed error; nil outside one
	suppressed uint64        // sealed errors not logged in the episode
}

// NewSealWatch returns a watch that classifies a background error as sealed
// with isSealed and logs to logger. A nil isSealed leaves the classification
// to the filesystem's barrier state alone (see Attach).
func NewSealWatch(logger pebble.Logger, isSealed func(error) bool) *SealWatch {
	return &SealWatch{logger: logger, isSealed: isSealed, sealedC: make(chan struct{})}
}

// Attach installs the watch on opts. Every listener already in opts still
// runs, except that a sealed background error reaches the watch alone, which
// is what bounds its logging. When opts.FS reports its barrier's state, the
// watch asks it before every flush (Flush, Check) and classifies a background
// error raised while the barrier is sealed as sealed.
func (w *SealWatch) Attach(opts *pebble.Options) {
	if fs, ok := opts.FS.(interface{ BarrierSealed() bool }); ok {
		w.barrierSealed = fs.BarrierSealed
	}
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
	return w.errLocked()
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

// Flush flushes db's memtable and waits for it, like DB.Flush, but returns a
// *SealedError rather than waiting while the barrier is sealed. Every flush
// rotates the WAL, so Flush refuses before calling into Pebble when Check
// does.
func (w *SealWatch) Flush(db *pebble.DB) error {
	if err := w.Check(); err != nil {
		return err
	}
	w.mu.Lock()
	sealedC := w.sealedC
	w.mu.Unlock()
	done, err := db.AsyncFlush()
	if err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-sealedC:
		return w.Err()
	}
}

// observe records err and reports whether it was a sealed error, logging only
// the first of an episode.
// sealed reports whether err comes of a sealed barrier: the classifier says
// so, or the filesystem reports its barrier sealed while the error is raised.
func (w *SealWatch) sealed(err error) bool {
	if w.isSealed != nil && w.isSealed(err) {
		return true
	}
	return w.barrierSealed != nil && w.barrierSealed()
}

func (w *SealWatch) observe(err error) bool {
	if !w.sealed(err) {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cause != nil {
		w.suppressed++
		return true
	}
	w.cause = err
	close(w.sealedC)
	w.logger.Errorf("background error: %s; flushes and compactions stop until the barrier is unsealed", err)
	return true
}

// resume ends an episode once a flush or compaction succeeds again.
func (w *SealWatch) resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cause == nil {
		return
	}
	w.logger.Infof("barrier unsealed: flushes and compactions resumed after %d further sealed errors", w.suppressed)
	w.cause, w.suppressed = nil, 0
	w.sealedC = make(chan struct{})
}

func (w *SealWatch) errLocked() error {
	if w.cause == nil {
		return nil
	}
	return &SealedError{Err: w.cause}
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
	return db, seals, nil
}
