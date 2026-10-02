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
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
)

// prefixFloorHistory is a HistoryReplayer whose compaction floors are per
// keyspace prefix, as the MVCC store's are, and which records the ranges the
// hub asks about.
type prefixFloorHistory struct {
	current int64
	floors  map[string]int64

	mu    sync.Mutex
	asked [][2][]byte
}

func (h *prefixFloorHistory) CurrentRevision() int64 { return h.current }

// CompactedRevision answers the highest floor whose prefix covers key -- the
// single key when rangeEnd is nil, the range's start otherwise, which is
// enough for the prefix watches these tests open.
func (h *prefixFloorHistory) CompactedRevision(key, rangeEnd []byte) int64 {
	h.mu.Lock()
	h.asked = append(h.asked, [2][]byte{append([]byte(nil), key...), append([]byte(nil), rangeEnd...)})
	h.mu.Unlock()
	var floor int64
	for p, rev := range h.floors {
		if bytes.HasPrefix(key, []byte(p)) && rev > floor {
			floor = rev
		}
	}
	return floor
}

func (h *prefixFloorHistory) Replay(int64, int64, func([]byte) bool, bool, func(*Event) bool) error {
	return nil
}

var _ HistoryReplayer = (*prefixFloorHistory)(nil)

// TestHub_CompactionFloorIsTheWatchedRangesOwn: a compaction floor refuses a
// replay of the keys it covers and nothing else. Another database's
// compaction -- a floor on another prefix -- neither refuses nor cancels a
// watch that starts below it.
func TestHub_CompactionFloorIsTheWatchedRangesOwn(t *testing.T) {
	h := NewHub()
	t.Cleanup(h.Close)
	hist := &prefixFloorHistory{current: 20, floors: map[string]int64{"acme/app/": 15}}
	h.SetHistory(hist)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if _, err := h.Watch(ctx, "acme/app/k", WithStartRevision(10)); !errors.Is(err, ErrCompacted) {
		t.Fatalf("Watch below acme/app/'s floor = %v, want ErrCompacted", err)
	}
	if _, err := h.WatchPrefix(ctx, "acme/app/", WithStartRevision(10)); !errors.Is(err, ErrCompacted) {
		t.Fatalf("WatchPrefix below acme/app/'s floor = %v, want ErrCompacted", err)
	}

	w, err := h.Watch(ctx, "beta/app/k", WithStartRevision(10))
	if err != nil {
		t.Fatalf("Watch on another database below acme's floor = %v, want served", err)
	}
	collectN(t, w, 1) // the Created response: the replay completed, it was not cancelled
	if _, err := h.WatchPrefix(ctx, "beta/app/", WithStartRevision(10)); err != nil {
		t.Fatalf("WatchPrefix on another database below acme's floor = %v, want served", err)
	}

	hist.mu.Lock()
	defer hist.mu.Unlock()
	var sawExact, sawPrefix bool
	for _, a := range hist.asked {
		if string(a[0]) == "acme/app/k" && len(a[1]) == 0 {
			sawExact = true
		}
		if string(a[0]) == "acme/app/" && string(a[1]) == "acme/app0" {
			sawPrefix = true
		}
	}
	if !sawExact || !sawPrefix {
		t.Fatalf("the hub asked for floors over %q; want the exact key and the prefix's [acme/app/, acme/app0) range", hist.asked)
	}
}
