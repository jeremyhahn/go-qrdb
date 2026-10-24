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
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble/v2"
)

// SealedError is returned by SealWatch.Flush when the at-rest barrier under
// the database's filesystem is sealed: no new sstable can be written until it
// is unsealed, so a flush cannot complete. Err is the barrier's own error,
// which Unwrap exposes to errors.Is.
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
type SealWatch struct {
	logger   pebble.Logger
	isSealed func(error) bool

	mu         sync.Mutex
	sealedC    chan struct{} // closed when an episode begins
	cause      error         // the episode's first sealed error; nil outside one
	suppressed uint64        // sealed errors not logged in the episode
}

// NewSealWatch returns a watch that classifies a background error as sealed
// with isSealed and logs to logger.
func NewSealWatch(logger pebble.Logger, isSealed func(error) bool) *SealWatch {
	return &SealWatch{logger: logger, isSealed: isSealed, sealedC: make(chan struct{})}
}

// Attach installs the watch on opts. Every listener already in opts still
// runs, except that a sealed background error reaches the watch alone, which
// is what bounds its logging.
func (w *SealWatch) Attach(opts *pebble.Options) {
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

// Flush flushes db's memtable and waits for it, like DB.Flush, but returns a
// *SealedError rather than waiting while the barrier is sealed.
func (w *SealWatch) Flush(db *pebble.DB) error {
	w.mu.Lock()
	err, sealedC := w.errLocked(), w.sealedC
	w.mu.Unlock()
	if err != nil {
		return err
	}
	done, err := db.AsyncFlush()
	if err != nil {
		// Rotating the WAL creates a file, which a sealed barrier refuses too.
		if w.isSealed(err) {
			return &SealedError{Err: err}
		}
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
func (w *SealWatch) observe(err error) bool {
	if !w.isSealed(err) {
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
