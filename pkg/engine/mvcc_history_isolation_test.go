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
	"bytes"
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// TestMVCCHistory_ReplayIsNotCancelledByAnotherDatabasesCompaction: a watch
// replay of database A reads only A's keys. Database B compacting while A's
// replay is running -- interleaved writes, so B's changes sit between A's in
// the change log -- must not fail or cancel A's replay: a tenant-isolation
// property. Before the fix the replay read B's data entries before filtering
// them out, and B's compaction removed those entries mid-scan.
func TestMVCCHistory_ReplayIsNotCancelledByAnotherDatabasesCompaction(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	var entries []sm.Entry
	for i := range 10 {
		entries = append(entries,
			mvccPutEntry(uint64(2*i+1), []byte("acme/app/k"), []byte{byte('a' + i)}, 0),
			mvccPutEntry(uint64(2*i+2), []byte("beta/app/k"), []byte{byte('a' + i)}, 0),
		)
	}
	applyMVCCEntries(t, eng, entries)
	cur := eng.Store().CurrentRevision()

	history := newMVCCHistoryAdapter(eng.Store())
	inA := func(k []byte) bool { return bytes.HasPrefix(k, []byte("acme/app/")) }
	compactedB := false
	var replayed int
	err := history.Replay(1, cur, inA, false, func(ev *watch.Event) bool {
		replayed++
		if !compactedB {
			// B compacts its whole history while A's replay is under way.
			if cerr := eng.Store().CompactPrefix([]byte("beta/app/"), cur); cerr != nil {
				t.Fatalf("CompactPrefix(beta): %v", cerr)
			}
			compactedB = true
		}
		return true
	})
	if err != nil {
		t.Fatalf("A's replay failed after B compacted: %v", err)
	}
	if replayed != 10 {
		t.Fatalf("A's replay delivered %d of its 10 changes", replayed)
	}
}
