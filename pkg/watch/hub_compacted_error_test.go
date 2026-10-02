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
	"testing"
	"time"
)

func TestCompactedError_IsErrCompactedAndReadsAsIt(t *testing.T) {
	err := error(&CompactedError{CompactRevision: 9})
	if !errors.Is(err, ErrCompacted) {
		t.Fatal("CompactedError does not match ErrCompacted")
	}
	if err.Error() != ErrCompacted.Error() {
		t.Fatalf("Error() = %q, want ErrCompacted's text %q", err.Error(), ErrCompacted.Error())
	}
	if errors.Is(err, ErrStartRevisionUnsupported) {
		t.Fatal("CompactedError matches an unrelated sentinel")
	}
}

// TestHub_CompactedRefusalCarriesTheWatchedRangesFloor: a start revision at
// or below the floor is refused with that floor -- the one covering the
// watched key or range, not another database's higher one.
func TestHub_CompactedRefusalCarriesTheWatchedRangesFloor(t *testing.T) {
	h := NewHub()
	t.Cleanup(h.Close)
	h.SetHistory(&prefixFloorHistory{current: 50, floors: map[string]int64{"acme/app/": 15, "beta/app/": 40}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	for name, open := range map[string]func() (Watcher, error){
		"key":    func() (Watcher, error) { return h.Watch(ctx, "acme/app/k", WithStartRevision(10)) },
		"prefix": func() (Watcher, error) { return h.WatchPrefix(ctx, "acme/app/", WithStartRevision(15)) },
	} {
		_, err := open()
		var compacted *CompactedError
		if !errors.As(err, &compacted) || compacted.CompactRevision != 15 {
			t.Fatalf("%s: Watch below acme/app/'s floor = %v, want a CompactedError carrying 15", name, err)
		}
	}
}

// TestHub_CompactionOvertakingAReplayCancelsWithTheFloor: compaction that
// passes the start revision while the replay runs cancels the watch, and the
// watcher's error carries the floor it was overtaken by.
func TestHub_CompactionOvertakingAReplayCancelsWithTheFloor(t *testing.T) {
	gate := make(chan struct{})
	compactedRead := make(chan int64, 1)
	fh := &fakeHistory{
		current:       8,
		changes:       []fakeChange{{rev: 6, key: "k", value: "a"}, {rev: 7, key: "k", value: "b"}},
		gate:          gate,
		firstN:        1,
		compactedRead: compactedRead,
	}
	h := NewHub()
	t.Cleanup(h.Close)
	h.SetHistory(fh)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	w, err := h.Watch(ctx, "k", WithStartRevision(6))
	if err != nil {
		t.Fatalf("Watch above the floor = %v", err)
	}
	// prepareReplay's read saw no floor; the replay is now parked after its
	// first event. Compact past the start revision, then let it finish:
	// close(gate) orders this write before the replay's fail-check read.
	<-compactedRead
	fh.compacted = 7
	close(gate)

	deadline := time.After(3 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-w.Events():
		case <-deadline:
			t.Fatal("the overtaken watch was never cancelled")
		}
	}
	var compacted *CompactedError
	if err := w.Err(); !errors.As(err, &compacted) || compacted.CompactRevision != 7 {
		t.Fatalf("watcher.Err() = %v, want a CompactedError carrying 7", err)
	}
}
