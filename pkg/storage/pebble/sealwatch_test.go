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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// errTestSealed stands in for a barrier's sealed sentinel.
var errTestSealed = errors.New("test barrier is sealed")

func isTestSealed(err error) bool { return errors.Is(err, errTestSealed) }

// sealableFS refuses to create an sstable while sealed, as an encrypting
// filesystem does once its barrier seals: files already open keep working.
// Only sstables are refused because that is the failure observed in the field
// ("creating object N"); Pebble panics outright on a WAL it cannot create.
type sealableFS struct {
	vfs.FS
	sealed atomic.Bool
}

func (fs *sealableFS) Create(name string, category vfs.DiskWriteCategory) (vfs.File, error) {
	if fs.sealed.Load() && strings.HasSuffix(name, ".sst") {
		return nil, errTestSealed
	}
	return fs.FS.Create(name, category)
}

// recordingLogger counts what reaches each level.
type recordingLogger struct {
	mu     sync.Mutex
	errors []string
	infos  []string
}

func (l *recordingLogger) Infof(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) Errorf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) Fatalf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func (l *recordingLogger) sealedErrors() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.errors {
		if strings.Contains(e, errTestSealed.Error()) {
			n++
		}
	}
	return n
}

func (l *recordingLogger) resumed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, i := range l.infos {
		if strings.Contains(i, "resumed after") {
			return true
		}
	}
	return false
}

// flushWithin runs w.Flush(db) and fails the test if it has not returned by
// the deadline: before the watch, this call never returned.
func flushWithin(t *testing.T, w *SealWatch, db *pebble.DB) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- w.Flush(db) }()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("SealWatch.Flush is still waiting on a sealed barrier")
		return nil
	}
}

func TestSealWatch_FlushUnderSealedBarrierReturnsSealedError(t *testing.T) {
	fs := &sealableFS{FS: vfs.NewMem()}
	logger := &recordingLogger{}
	w := NewSealWatch(logger, isTestSealed)
	opts := &pebble.Options{FS: fs, Logger: logger}
	w.Attach(opts)
	db, err := pebble.Open("db", opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Set([]byte("k"), []byte("v"), pebble.NoSync); err != nil {
		t.Fatalf("Set: %v", err)
	}

	fs.sealed.Store(true)
	err = flushWithin(t, w, db)
	var sealed *SealedError
	if !errors.As(err, &sealed) || !errors.Is(err, errTestSealed) {
		t.Fatalf("Flush under a sealed barrier = %v; want a *SealedError wrapping the barrier's error", err)
	}
	if !errors.Is(w.Err(), errTestSealed) {
		t.Fatalf("Err() = %v; want the sealed episode's error", w.Err())
	}
	// A writer that comes back while the episode lasts is refused at once.
	if err := flushWithin(t, w, db); !errors.As(err, &sealed) {
		t.Fatalf("second Flush = %v; want a *SealedError", err)
	}

	// Pebble kept retrying the flush the whole time; one line was logged.
	if n := logger.sealedErrors(); n != 1 {
		t.Fatalf("logged %d sealed background errors; want 1 per episode", n)
	}
	if !strings.Contains(sealed.Error(), "at-rest barrier sealed") {
		t.Fatalf("SealedError.Error() = %q", sealed.Error())
	}

	// Unsealing lets Pebble's next retry succeed, which ends the episode.
	fs.sealed.Store(false)
	if err := db.Flush(); err != nil {
		t.Fatalf("db.Flush after unseal: %v", err)
	}
	if err := w.Err(); err != nil {
		t.Fatalf("Err() after a successful flush = %v; want nil", err)
	}
	if !logger.resumed() {
		t.Fatal("the end of the sealed episode was not logged")
	}
	if err := flushWithin(t, w, db); err != nil {
		t.Fatalf("Flush after unseal = %v; want nil", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSealWatch_ClosingASealedDatabaseEndsTheRetries(t *testing.T) {
	fs := &sealableFS{FS: vfs.NewMem()}
	logger := &recordingLogger{}
	w := NewSealWatch(logger, isTestSealed)
	opts := &pebble.Options{FS: fs, Logger: logger}
	w.Attach(opts)
	db, err := pebble.Open("db", opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Set([]byte("k"), []byte("v"), pebble.NoSync); err != nil {
		t.Fatalf("Set: %v", err)
	}
	fs.sealed.Store(true)
	if err := flushWithin(t, w, db); !errors.Is(err, errTestSealed) {
		t.Fatalf("Flush = %v; want the sealed error", err)
	}

	// The writer stops: Close returns while the barrier is still sealed.
	closed := make(chan error, 1)
	go func() { closed <- db.Close() }()
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("Close is still waiting on a flush the sealed barrier refuses")
	}
	if n := logger.sealedErrors(); n != 1 {
		t.Fatalf("logged %d sealed background errors; want 1", n)
	}
}

func TestSealWatch_AttachKeepsExistingListeners(t *testing.T) {
	logger := &recordingLogger{}
	w := NewSealWatch(logger, isTestSealed)
	var background []error
	var flushes, compactions int
	opts := &pebble.Options{EventListener: &pebble.EventListener{
		BackgroundError: func(err error) { background = append(background, err) },
		FlushEnd:        func(pebble.FlushInfo) { flushes++ },
		CompactionEnd:   func(pebble.CompactionInfo) { compactions++ },
	}}
	w.Attach(opts)
	l := opts.EventListener

	other := errors.New("disk full")
	l.BackgroundError(other)
	if len(background) != 1 || background[0] != other || w.Err() != nil {
		t.Fatalf("an unsealed error: listener got %v, Err() = %v; want it passed through", background, w.Err())
	}

	l.BackgroundError(errTestSealed)
	l.BackgroundError(errTestSealed)
	if len(background) != 1 {
		t.Fatalf("listener got %d errors; a sealed error must reach the watch alone", len(background))
	}
	if n := logger.sealedErrors(); n != 1 {
		t.Fatalf("logged %d sealed errors; want 1", n)
	}

	l.FlushEnd(pebble.FlushInfo{Err: errTestSealed})
	if w.Err() == nil {
		t.Fatal("a failed flush ended the sealed episode")
	}
	l.CompactionEnd(pebble.CompactionInfo{})
	if w.Err() != nil || !logger.resumed() {
		t.Fatalf("a successful compaction left Err() = %v, resumed logged = %v", w.Err(), logger.resumed())
	}
	l.FlushEnd(pebble.FlushInfo{})
	if flushes != 2 || compactions != 1 {
		t.Fatalf("listener saw %d flushes and %d compactions; want 2 and 1", flushes, compactions)
	}
}

func TestSealWatch_AttachWithoutListenerLogsOtherErrors(t *testing.T) {
	logger := &recordingLogger{}
	w := NewSealWatch(logger, isTestSealed)
	opts := &pebble.Options{}
	w.Attach(opts)

	opts.EventListener.BackgroundError(errors.New("disk full"))
	opts.EventListener.BackgroundError(errTestSealed)
	opts.EventListener.BackgroundError(errTestSealed)
	if len(logger.errors) != 2 || !strings.Contains(logger.errors[0], "disk full") {
		t.Fatalf("logged %q; want the unsealed error and one sealed error", logger.errors)
	}
}

// TestSealWatch_FlushWokenAfterResumeReturnsTheEpisodeError drives the
// interleaving in which a sealed background error ends a flush's wait and a
// successful flush or compaction ends the episode before the waiter runs. The
// waiter's flush never completed, so it must report the episode's error, not
// success.
func TestSealWatch_FlushWokenAfterResumeReturnsTheEpisodeError(t *testing.T) {
	logger := &recordingLogger{}
	w := NewSealWatch(logger, isTestSealed)
	w.mu.Lock()
	ep := w.episode
	w.mu.Unlock()

	w.observe(errTestSealed)
	w.resume()
	if err := w.Err(); err != nil {
		t.Fatalf("Err() after resume = %v; want nil", err)
	}

	err := ep.await(make(chan struct{}))
	var sealed *SealedError
	if !errors.As(err, &sealed) || !errors.Is(err, errTestSealed) {
		t.Fatalf("await = %v; want a *SealedError wrapping the episode's error", err)
	}
}

// TestSealWatch_CompletedFlushSucceeds checks that a flush whose done channel
// fires returns nil, whether or not an episode began while it waited.
func TestSealWatch_CompletedFlushSucceeds(t *testing.T) {
	w := NewSealWatch(&recordingLogger{}, isTestSealed)
	w.mu.Lock()
	ep := w.episode
	w.mu.Unlock()
	done := make(chan struct{})
	close(done)

	if err := ep.await(done); err != nil {
		t.Fatalf("await with a completed flush = %v; want nil", err)
	}
	w.observe(errTestSealed)
	if err := ep.await(done); err != nil {
		t.Fatalf("await with a completed flush in an episode = %v; want nil", err)
	}
}
