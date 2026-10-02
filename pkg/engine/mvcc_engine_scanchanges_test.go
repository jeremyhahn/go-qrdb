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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// MVCCScanChangesQuery
//
// The query exists for one reason: a range scan walks the LIVE metadata
// namespace and so cannot report a key that has been deleted, whatever
// revision it targets. These tests pin that the change scan can.
// ---------------------------------------------------------------------------

// scanChanges issues an MVCCScanChangesQuery through the engine's Lookup path.
func scanChanges(t *testing.T, eng *MVCCEngine, q service.MVCCScanChangesQuery) *service.MVCCScanChangesResult {
	t.Helper()
	out, err := eng.Lookup(context.Background(), q)
	if err != nil {
		t.Fatalf("Lookup(MVCCScanChangesQuery): %v", err)
	}
	result, ok := out.(*service.MVCCScanChangesResult)
	if !ok {
		t.Fatalf("Lookup returned %T, want *service.MVCCScanChangesResult", out)
	}
	return result
}

// applyEntries applies entries through the engine's Update path, which is the
// only way the revision index gets written.
func applyEntries(t *testing.T, eng *MVCCEngine, entries []sm.Entry) {
	t.Helper()
	results := make([]sm.Result, len(entries))
	if err := eng.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

func assertKeys(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
	for i := range got {
		// #nosec G602 -- the lengths are compared equal above, and i ranges over got.
		if got[i] != want[i] {
			t.Fatalf("%s: got %v, want %v", label, got, want)
		}
	}
}

func TestMVCCEngine_ScanChanges_ReportsPutsInWindow(t *testing.T) {
	eng, _ := newMVCCEngine(t)

	applyEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("app/a"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("app/b"), []byte("v1"), 0),
	})

	result := scanChanges(t, eng, service.MVCCScanChangesQuery{StartRevision: 1, EndRevision: 100})
	assertKeys(t, "keys", result.Keys, []string{"app/a", "app/b"})
	if result.Revision == 0 {
		t.Fatal("result carries no store revision")
	}
}

// TestMVCCEngine_ScanChanges_ReportsDeletedKeys is the capability the restore
// depends on and the one a range scan cannot provide.
func TestMVCCEngine_ScanChanges_ReportsDeletedKeys(t *testing.T) {
	eng, _ := newMVCCEngine(t)

	applyEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("app/gone"), []byte("v1"), 0)})
	afterPut := eng.store.CurrentRevision()
	applyEntries(t, eng, []sm.Entry{mvccDeleteEntry(2, []byte("app/gone"))})

	// A range scan at the revision the key existed no longer sees it, because
	// it enumerates keys that are live NOW.
	kvs, _, _, err := eng.store.Range([]byte("app/"), []byte("app0"), service.WithRevision(afterPut))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(kvs) != 0 {
		t.Fatalf("precondition failed: the range scan still sees the deleted key: %v", kvs)
	}

	// The change scan does.
	result := scanChanges(t, eng, service.MVCCScanChangesQuery{StartRevision: 1, EndRevision: 100})
	assertKeys(t, "keys", result.Keys, []string{"app/gone"})
}

func TestMVCCEngine_ScanChanges_DeduplicatesRepeatedlyChangedKeys(t *testing.T) {
	eng, _ := newMVCCEngine(t)

	applyEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("app/a"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("app/a"), []byte("v2"), 0),
		mvccPutEntry(3, []byte("app/a"), []byte("v3"), 0),
		mvccDeleteEntry(4, []byte("app/a")),
	})

	result := scanChanges(t, eng, service.MVCCScanChangesQuery{StartRevision: 1, EndRevision: 100})
	assertKeys(t, "keys", result.Keys, []string{"app/a"})
}

func TestMVCCEngine_ScanChanges_RestrictsToPrefix(t *testing.T) {
	eng, _ := newMVCCEngine(t)

	applyEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("app/a"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("other/a"), []byte("v1"), 0),
		mvccPutEntry(3, []byte("app/b"), []byte("v1"), 0),
	})

	result := scanChanges(t, eng, service.MVCCScanChangesQuery{
		StartRevision: 1, EndRevision: 100, Prefix: []byte("app/"),
	})
	assertKeys(t, "keys", result.Keys, []string{"app/a", "app/b"})
}

func TestMVCCEngine_ScanChanges_HonoursRevisionWindow(t *testing.T) {
	eng, _ := newMVCCEngine(t)

	applyEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("app/early"), []byte("v1"), 0)})
	boundary := eng.store.CurrentRevision()
	applyEntries(t, eng, []sm.Entry{mvccPutEntry(2, []byte("app/late"), []byte("v1"), 0)})

	// Strictly after the boundary: only the later key.
	result := scanChanges(t, eng, service.MVCCScanChangesQuery{
		StartRevision: boundary + 1, EndRevision: 100,
	})
	assertKeys(t, "after boundary", result.Keys, []string{"app/late"})

	// Up to the boundary: only the earlier key.
	result = scanChanges(t, eng, service.MVCCScanChangesQuery{
		StartRevision: 1, EndRevision: boundary,
	})
	assertKeys(t, "up to boundary", result.Keys, []string{"app/early"})
}

func TestMVCCEngine_ScanChanges_EmptyWindowReturnsNoKeys(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	applyEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("app/a"), []byte("v1"), 0)})

	// An inverted window (start > end) is the shape a restore produces when
	// the target revision is already the current one.
	result := scanChanges(t, eng, service.MVCCScanChangesQuery{StartRevision: 100, EndRevision: 1})
	if len(result.Keys) != 0 {
		t.Fatalf("expected no keys, got %v", result.Keys)
	}
}

// TestMVCCEngine_ScanChanges_MarksTruncationRatherThanCutting checks the limit
// is reported, not silently applied: a caller that needs a complete set can
// only tell the difference if the flag is set.
func TestMVCCEngine_ScanChanges_MarksTruncationRatherThanCutting(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	applyEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("app/a"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("app/b"), []byte("v1"), 0),
		mvccPutEntry(3, []byte("app/c"), []byte("v1"), 0),
	})

	result := scanChanges(t, eng, service.MVCCScanChangesQuery{
		StartRevision: 1, EndRevision: 100, Limit: 2,
	})
	if !result.Truncated {
		t.Fatal("Truncated = false, want true when the limit is reached")
	}
	if len(result.Keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(result.Keys))
	}

	// A limit at or above the match count is not truncation.
	result = scanChanges(t, eng, service.MVCCScanChangesQuery{
		StartRevision: 1, EndRevision: 100, Limit: 3,
	})
	if result.Truncated {
		t.Fatal("Truncated = true when the limit exactly fits")
	}
}

// TestMVCCEngine_ScanChanges_ReturnsInternalKeysUnfiltered documents the
// deliberate division of responsibility: like the MVCCRangeQuery path, this
// query reports what the requested window contains, and the internal-key
// refusal is applied by the caller that needs it (selective restore in
// pkg/service, where it is a tested security guard).
func TestMVCCEngine_ScanChanges_ReturnsInternalKeysUnfiltered(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	applyEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("__sys:roles/admin"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("app/a"), []byte("v1"), 0),
	})

	result := scanChanges(t, eng, service.MVCCScanChangesQuery{StartRevision: 1, EndRevision: 100})
	assertKeys(t, "keys", result.Keys, []string{"__sys:roles/admin", "app/a"})
}

// TestMVCCEngine_ScanChanges_ReturnsNoValues pins that the query is metadata
// only. Returning historical values here would make it a bulk export of
// arbitrary history behind a single read.
func TestMVCCEngine_ScanChanges_ReturnsNoValues(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	applyEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("app/a"), []byte("secret"), 0)})

	out, err := eng.Lookup(context.Background(), service.MVCCScanChangesQuery{
		StartRevision: 1, EndRevision: 100,
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	result := out.(*service.MVCCScanChangesResult)

	// The result type carries keys only — there is no field a value could
	// travel in. Assert the shape so a future field addition is a deliberate
	// decision rather than an accident.
	assertKeys(t, "keys", result.Keys, []string{"app/a"})
}

// TestMVCCEngine_ScanChanges_FailsClosedBelowCompactionFloor checks the scan
// inherits the store's compaction contract rather than returning a partial
// window.
func TestMVCCEngine_ScanChanges_FailsClosedBelowCompactionFloor(t *testing.T) {
	eng, _ := newMVCCEngine(t)
	applyEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("app/a"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("app/a"), []byte("v2"), 0),
	})
	floor := eng.store.CurrentRevision()
	if err := eng.store.CompactPrefix([]byte("app/"), floor); err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// Compaction garbage-collects the index entries at or below the floor, so
	// a scan of the compacted window would report an empty -- incomplete --
	// set as if nothing had changed. It is refused instead, as a historical
	// read below the floor is.
	_, err := eng.Lookup(context.Background(), service.MVCCScanChangesQuery{StartRevision: 1, EndRevision: floor, Prefix: []byte("app/")})
	var compacted *mvcc.RevisionCompactedError
	if !errors.As(err, &compacted) || compacted.Compacted != floor {
		t.Fatalf("scan of a compacted window = %v, want *mvcc.RevisionCompactedError at floor %d", err, floor)
	}

	// Another prefix's change log is untouched by that compaction, and a
	// window above the floor still scans.
	applyEntries(t, eng, []sm.Entry{mvccPutEntry(3, []byte("other/b"), []byte("v1"), 0)})
	result := scanChanges(t, eng, service.MVCCScanChangesQuery{StartRevision: 1, EndRevision: 100, Prefix: []byte("other/")})
	assertKeys(t, "other prefix", result.Keys, []string{"other/b"})
	result = scanChanges(t, eng, service.MVCCScanChangesQuery{StartRevision: floor + 1, EndRevision: 100, Prefix: []byte("app/")})
	if len(result.Keys) != 0 {
		t.Fatalf("window above the floor = %v, want nothing (no change since)", result.Keys)
	}
}
