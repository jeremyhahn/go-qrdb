// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package watch

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// counted is the context a transport opens a client watch with.
func counted() context.Context { return CountedContext(context.Background()) }

// openWatch opens a counted exact-key watch on h and closes it at test end.
func openWatch(t *testing.T, h *Hub, key string) Watcher {
	t.Helper()
	w, err := h.Watch(counted(), key)
	if err != nil {
		t.Fatalf("Watch(%q): %v", key, err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

// requireLimitError fails unless err is a *WatcherLimitError naming want.
func requireLimitError(t *testing.T, err error, want uint64) {
	t.Helper()
	var le *WatcherLimitError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v (%T), want *WatcherLimitError", err, err)
	}
	if le.Max != want {
		t.Fatalf("WatcherLimitError.Max = %d, want %d", le.Max, want)
	}
}

func TestNewWatcherLimit_NonPositiveIsUnlimited(t *testing.T) {
	for _, n := range []int{0, -1} {
		if l := NewWatcherLimit(n); l != nil {
			t.Errorf("NewWatcherLimit(%d) = %+v, want nil (unlimited)", n, l)
		}
	}
}

func TestNewWatcherLimit_PositiveCaps(t *testing.T) {
	l := NewWatcherLimit(3)
	if l == nil {
		t.Fatal("NewWatcherLimit(3) = nil, want a limit")
	}
	if l.limit != 3 || l.Active() != 0 {
		t.Fatalf("limit = %d, active = %d; want 3 and 0", l.limit, l.Active())
	}
}

func TestWatcherLimit_NilIsUnlimited(t *testing.T) {
	var l *WatcherLimit
	for i := 0; i < 8; i++ {
		if err := l.acquire(); err != nil {
			t.Fatalf("nil limit acquire %d: %v", i, err)
		}
	}
	l.release()
	if got := l.Active(); got != 0 {
		t.Fatalf("nil limit Active() = %d, want 0", got)
	}
}

func TestWatcherLimit_AcquireRefusesAtCapAndReleaseFrees(t *testing.T) {
	l := NewWatcherLimit(2)
	if err := l.acquire(); err != nil {
		t.Fatal(err)
	}
	if err := l.acquire(); err != nil {
		t.Fatal(err)
	}
	requireLimitError(t, l.acquire(), 2)
	if got := l.Active(); got != 2 {
		t.Fatalf("Active() after refusal = %d, want 2: a refusal takes no slot", got)
	}
	l.release()
	if err := l.acquire(); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestWatcherLimit_ConcurrentAcquireNeverExceedsCap(t *testing.T) {
	const slots, callers = 5, 64
	l := NewWatcherLimit(slots)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.acquire() == nil {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != slots || l.Active() != slots {
		t.Fatalf("granted = %d, active = %d; want both %d", granted, l.Active(), slots)
	}
}

func TestWatcherLimitError_ErrorNamesTheCap(t *testing.T) {
	want := "qrdb/watch: watcher limit reached (max_watchers 7)"
	if got := (&WatcherLimitError{Max: 7}).Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestWatcherLimitError_IsNotAnotherWatchError(t *testing.T) {
	err := error(&WatcherLimitError{Max: 1})
	if errors.Is(err, ErrWatcherOverflow) || errors.Is(err, ErrCompacted) {
		t.Fatalf("%v must not match another watch sentinel", err)
	}
}

func TestWithEventBufferSize_SetsEveryWatchersBuffer(t *testing.T) {
	h := NewHub(WithEventBufferSize(2))
	defer h.Close()
	w := openWatch(t, h, "k")
	if got := cap(w.(*watcher).events); got != 2 {
		t.Fatalf("event buffer = %d, want 2", got)
	}
	// A consumer that falls more than the buffer behind is evicted.
	for i := int64(1); i <= 3; i++ {
		h.NotifyPut([]byte("k"), &kv.KeyValue{Key: []byte("k"), ModRevision: i}, nil)
	}
	for range w.Events() {
	}
	if !errors.Is(w.Err(), ErrWatcherOverflow) {
		t.Fatalf("Err() = %v, want ErrWatcherOverflow at a 2-response buffer", w.Err())
	}
}

func TestWithEventBufferSize_NonPositiveKeepsDefault(t *testing.T) {
	for _, n := range []int{0, -4} {
		h := NewHub(WithEventBufferSize(n))
		w := openWatch(t, h, "k")
		if got := cap(w.(*watcher).events); got != defaultEventBufferSize {
			t.Errorf("WithEventBufferSize(%d): event buffer = %d, want default %d", n, got, defaultEventBufferSize)
		}
		h.Close()
	}
}

func TestWithWatcherLimit_RefusesWatchAndWatchPrefixAtCap(t *testing.T) {
	h := NewHub(WithWatcherLimit(NewWatcherLimit(1)))
	defer h.Close()
	openWatch(t, h, "a")

	_, err := h.Watch(counted(), "b")
	requireLimitError(t, err, 1)
	_, err = h.WatchPrefix(counted(), "p/")
	requireLimitError(t, err, 1)
	if got := h.WatchCount(); got != 1 {
		t.Fatalf("WatchCount() = %d, want 1: a refused watch registers nothing", got)
	}
}

func TestWithWatcherLimit_CloseFreesTheSlot(t *testing.T) {
	l := NewWatcherLimit(1)
	h := NewHub(WithWatcherLimit(l))
	defer h.Close()
	w, err := h.WatchPrefix(counted(), "p/")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Close returns only after the slot is released, so the next watch at
	// the cap is admitted without waiting.
	if got := l.Active(); got != 0 {
		t.Fatalf("Active() after Close = %d, want 0", got)
	}
	openWatch(t, h, "a")
}

func TestWithWatcherLimit_SharedAcrossHubs(t *testing.T) {
	l := NewWatcherLimit(2)
	h1 := NewHub(WithWatcherLimit(l))
	h2 := NewHub(WithWatcherLimit(l))
	defer h1.Close()
	defer h2.Close()
	openWatch(t, h1, "a")
	openWatch(t, h2, "b")

	_, err := h1.Watch(counted(), "c")
	requireLimitError(t, err, 2)
	_, err = h2.Watch(counted(), "c")
	requireLimitError(t, err, 2)
}

func TestWithWatcherLimit_NilLeavesHubUnlimited(t *testing.T) {
	h := NewHub(WithWatcherLimit(nil))
	defer h.Close()
	for _, k := range []string{"a", "b", "c"} {
		openWatch(t, h, k)
	}
	if got := h.WatchCount(); got != 3 {
		t.Fatalf("WatchCount() = %d, want 3", got)
	}
}

func TestCountedContext_MarksOnlyTheMarkedContext(t *testing.T) {
	if isCounted(context.Background()) {
		t.Fatal("a bare context must not be counted")
	}
	if !isCounted(counted()) {
		t.Fatal("CountedContext must mark its context")
	}
	type other struct{}
	if !isCounted(context.WithValue(counted(), other{}, 1)) {
		t.Fatal("a context derived from a counted one stays counted")
	}
}

func TestWatcherLimit_AcquireForTakesASlotOnlyWhenCounted(t *testing.T) {
	l := NewWatcherLimit(1)
	if slot, err := l.acquireFor(context.Background()); slot || err != nil {
		t.Fatalf("uncounted acquireFor = (%v, %v), want (false, nil)", slot, err)
	}
	if slot, err := l.acquireFor(counted()); !slot || err != nil {
		t.Fatalf("counted acquireFor = (%v, %v), want (true, nil)", slot, err)
	}
	_, err := l.acquireFor(counted())
	requireLimitError(t, err, 1)
	var unlimited *WatcherLimit
	if slot, err := unlimited.acquireFor(counted()); slot || err != nil {
		t.Fatalf("nil limit acquireFor = (%v, %v), want (false, nil)", slot, err)
	}
}

func TestWithWatcherLimit_UncountedWatchesAreNotCapped(t *testing.T) {
	l := NewWatcherLimit(1)
	h := NewHub(WithWatcherLimit(l))
	defer h.Close()
	openWatch(t, h, "client")
	// The node's own watchers open uncounted: a full cap never refuses them,
	// and they hold no slot.
	for _, k := range []string{"a", "b", "c"} {
		w, err := h.WatchPrefix(context.Background(), k)
		if err != nil {
			t.Fatalf("uncounted WatchPrefix(%q) at the cap: %v", k, err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := l.Active(); got != 1 {
		t.Fatalf("Active() = %d, want 1: only the counted watch holds a slot", got)
	}
}
