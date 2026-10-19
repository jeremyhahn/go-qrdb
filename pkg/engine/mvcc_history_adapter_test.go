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

package engine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// The compaction floor a watch is checked against follows the shape of its
// key selection: a nil range end asks about one key, an empty one about
// every key from the start, and any other about the half-open range. Only
// the compacted database's floor is reported for a selection that reaches it.
func TestMVCCHistory_CompactedRevisionFollowsTheSelection(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("acme/app/k"), []byte("a"), 0),
		mvccPutEntry(2, []byte("acme/app/k"), []byte("b"), 0),
		mvccPutEntry(3, []byte("beta/app/k"), []byte("a"), 0),
	})
	floor := eng.Store().CurrentRevision()
	if err := eng.Store().CompactPrefix([]byte("acme/app/"), floor); err != nil {
		t.Fatalf("CompactPrefix: %v", err)
	}
	history := newMVCCHistoryAdapter(eng.Store())

	cases := []struct {
		name      string
		key, end  []byte
		wantFloor int64
	}{
		{"compacted key", []byte("acme/app/k"), nil, floor},
		{"uncompacted key", []byte("beta/app/k"), nil, 0},
		{"open range over the compacted database", []byte("acme/app/"), []byte{}, floor},
		{"open range past the compacted database", []byte("beta/app/"), []byte{}, 0},
		{"bounded range in the compacted database", []byte("acme/app/"), []byte("acme/app0"), floor},
		{"bounded range in another database", []byte("beta/app/"), []byte("beta/app0"), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := history.CompactedRevision(tc.key, tc.end); got != tc.wantFloor {
				t.Fatalf("CompactedRevision(%q, %q) = %d, want %d", tc.key, tc.end, got, tc.wantFloor)
			}
		})
	}
}

// Replay delivers each committed change in revision order shaped as the live
// notification was, with the previous version attached only when asked for,
// and stops as soon as the callback declines the next event.
func TestMVCCHistory_ReplayShapesEventsAndStopsOnRequest(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("a"), 0),
		mvccPutEntry(2, []byte("k"), []byte("b"), 0),
		mvccDeleteEntry(3, []byte("k")),
	})
	cur := eng.Store().CurrentRevision()
	history := newMVCCHistoryAdapter(eng.Store())
	all := func([]byte) bool { return true }

	type shape struct {
		typ   watch.EventType
		value string
		prev  string
	}
	collect := func(t *testing.T, wantPrev bool, limit int) []shape {
		t.Helper()
		var got []shape
		err := history.Replay(1, cur, all, wantPrev, func(ev *watch.Event) bool {
			s := shape{typ: ev.Type, value: string(ev.Kv.Value)}
			if ev.PrevKv != nil {
				s.prev = string(ev.PrevKv.Value)
			}
			got = append(got, s)
			return len(got) < limit
		})
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		return got
	}

	cases := []struct {
		name     string
		wantPrev bool
		limit    int
		want     []shape
	}{
		{"with previous versions", true, 10, []shape{
			{watch.EventPut, "a", ""},
			{watch.EventPut, "b", "a"},
			{watch.EventDelete, "", "b"},
		}},
		{"without previous versions", false, 10, []shape{
			{watch.EventPut, "a", ""},
			{watch.EventPut, "b", ""},
			{watch.EventDelete, "", ""},
		}},
		{"stopped after the first event", true, 1, []shape{
			{watch.EventPut, "a", ""},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collect(t, tc.wantPrev, tc.limit)
			if len(got) != len(tc.want) {
				t.Fatalf("Replay delivered %d events %+v, want %+v", len(got), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("event %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
