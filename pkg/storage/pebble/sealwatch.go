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
// with isSealed and logs to logger.
func NewSealWatch(logger pebble.Logger, isSealed func(error) bool) *SealWatch {
	return &SealWatch{logger: logger, isSealed: isSealed, episode: newSealEpisode()}
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
	return w.episode.err()
}

// Flush flushes db's memtable and waits for it, like DB.Flush, but returns a
// *SealedError rather than waiting while the barrier is sealed. It returns nil
// only when its flush completed: a flush whose wait an episode ended reports
// that episode's error even if the episode is over by the time it returns.
func (w *SealWatch) Flush(db *pebble.DB) error {
	w.mu.Lock()
	ep := w.episode
	err := ep.err()
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

// observe records err and reports whether it was a sealed error, logging only
// the first of an episode.
func (w *SealWatch) observe(err error) bool {
	if !w.isSealed(err) {
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
