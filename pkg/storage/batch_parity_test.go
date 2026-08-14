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

// batch_parity_test.go pins storage.Batch's read-your-own-writes contract
// across EVERY backend that implements it. The contract is what keeps a Raft
// apply loop deterministic: the loop packs a timing-dependent number of
// committed entries into one batch, and an entry that could not see the writes
// of the entries packed ahead of it would make that packing observable in the
// resulting state. A backend that quietly served committed-only reads would
// reintroduce exactly that divergence, so the guarantee is checked here for all
// of them at once rather than only for the one the production state machine
// happens to use.
package storage_test

import (
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/namespace"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/partition"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newBatchParityBackends constructs one instance of every storage.Batch
// implementation in the tree.
func newBatchParityBackends(t *testing.T) []namedBackend {
	t.Helper()

	pdb, err := pebble.New(&pebble.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}
	fdb, err := file.New(t.TempDir())
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	obj, err := objstore.New("memory", map[string]string{})
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}
	pm, err := partition.NewManager(
		partition.NewHashRouter(4),
		partition.NewMemoryFactory(),
		&partition.Config{PartitionCount: 4, BackendType: partition.BackendMemory},
	)
	if err != nil {
		t.Fatalf("partition.NewManager: %v", err)
	}

	backends := []namedBackend{
		{"memory", memory.NewStorage()},
		{"pebble", pdb},
		{"file", fdb},
		{"objstore", obj},
		{"partition", pm},
		{"namespace", namespace.New(memory.NewStorage(), []byte("tenant"))},
	}
	t.Cleanup(func() {
		for _, b := range backends {
			_ = b.s.Close()
		}
	})
	return backends
}

// batchGet reads a key through a batch and renders the outcome for comparison.
func batchGet(b storage.Batch, key string) string {
	v, err := b.Get([]byte(key))
	if err != nil {
		return "ERR"
	}
	if v == nil {
		return "<nil>"
	}
	return string(v)
}

// batchScan renders the batch's merged view of the whole keyspace.
func batchScan(b storage.Batch, opts *storage.IteratorOptions) string {
	it := b.NewIterator(opts)
	defer func() { _ = it.Close() }()

	var pairs []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		pairs = append(pairs, string(it.Key())+"="+string(it.Value()))
	}
	if err := it.Error(); err != nil {
		return "ERR"
	}
	return fmt.Sprintf("%v", pairs)
}

// TestBatchReadYourOwnWritesParity drives every backend's batch through the
// same read-through-pending-writes scenario and requires identical transcripts.
func TestBatchReadYourOwnWritesParity(t *testing.T) {
	backends := newBatchParityBackends(t)

	transcripts := make(map[string][]string, len(backends))
	for _, b := range backends {
		transcripts[b.name] = runBatchReadScenario(t, b.s)
	}

	ref := backends[0]
	refLines := transcripts[ref.name]
	for _, b := range backends[1:] {
		got := transcripts[b.name]
		if len(got) != len(refLines) {
			t.Fatalf("%s produced %d transcript lines, %s produced %d",
				b.name, len(got), ref.name, len(refLines))
		}
		for i := range refLines {
			if got[i] != refLines[i] {
				t.Errorf("batch-read parity divergence at step %d:\n  %s: %q\n  %s: %q",
					i, ref.name, refLines[i], b.name, got[i])
			}
		}
	}
}

// runBatchReadScenario exercises the read half of storage.Batch and returns a
// transcript of observable outcomes.
func runBatchReadScenario(t *testing.T, s storage.Storage) []string {
	t.Helper()

	var tr []string
	rec := func(format string, args ...any) { tr = append(tr, fmt.Sprintf(format, args...)) }

	// Committed baseline.
	for _, kv := range [][2]string{{"c1", "committed-1"}, {"c2", "committed-2"}, {"c3", "committed-3"}} {
		if err := s.Set([]byte(kv[0]), []byte(kv[1])); err != nil {
			t.Fatalf("Set %s: %v", kv[0], err)
		}
	}

	b := s.Batch()
	defer func() { _ = b.Close() }()

	// 1. A key the batch has not touched still reads its committed value.
	rec("untouched c2=%s", batchGet(b, "c2"))

	// 2. A brand-new key is visible before commit.
	if err := b.Set([]byte("b1"), []byte("pending-1")); err != nil {
		t.Fatalf("Set b1: %v", err)
	}
	rec("new b1=%s", batchGet(b, "b1"))

	// 3. A pending write shadows the committed value.
	if err := b.Set([]byte("c1"), []byte("overwritten")); err != nil {
		t.Fatalf("Set c1: %v", err)
	}
	rec("shadowed c1=%s", batchGet(b, "c1"))

	// 4. A pending delete hides the committed value.
	if err := b.Delete([]byte("c3")); err != nil {
		t.Fatalf("Delete c3: %v", err)
	}
	rec("deleted c3=%s", batchGet(b, "c3"))

	// 5. The last write to a key wins.
	if err := b.Set([]byte("b1"), []byte("pending-2")); err != nil {
		t.Fatalf("Set b1 again: %v", err)
	}
	rec("rewritten b1=%s", batchGet(b, "b1"))

	// 6. A key absent from both reads as missing.
	rec("absent=%s", batchGet(b, "nowhere"))

	// 7. Scanning through the batch sees the merged view.
	rec("scan=%s", batchScan(b, nil))

	// 8. Bounded scanning applies the same bounds to both sides.
	rec("scan bounds=%s", batchScan(b, &storage.IteratorOptions{
		LowerBound: []byte("c"),
		UpperBound: []byte("d"),
	}))

	// 9. The committed state is untouched until Commit runs.
	preCommit, err := s.Get([]byte("c1"))
	if err != nil {
		t.Fatalf("Get c1: %v", err)
	}
	rec("pre-commit c1=%s", string(preCommit))

	// 10. After commit the batch's view is the committed view.
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for _, k := range []string{"b1", "c1", "c2", "c3"} {
		v, err := s.Get([]byte(k))
		if err != nil {
			rec("post-commit %s=ERR", k)
			continue
		}
		if v == nil {
			rec("post-commit %s=<nil>", k)
			continue
		}
		rec("post-commit %s=%s", k, v)
	}

	return tr
}

// TestBatchGetRejectsEmptyKeyParity requires every backend to refuse a read of
// the empty key rather than invent an answer for it.
func TestBatchGetRejectsEmptyKeyParity(t *testing.T) {
	for _, backend := range newBatchParityBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			b := backend.s.Batch()
			defer func() { _ = b.Close() }()

			if _, err := b.Get(nil); err == nil {
				t.Error("Get(nil) returned no error")
			}
		})
	}
}

// TestBatchReadsAfterCloseParity requires every backend's batch to report a
// closed batch on read rather than serve one.
func TestBatchReadsAfterCloseParity(t *testing.T) {
	for _, backend := range newBatchParityBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			b := backend.s.Batch()
			if err := b.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			if _, err := b.Get([]byte("k")); err == nil {
				t.Error("Get on a closed batch returned no error")
			}

			it := b.NewIterator(nil)
			defer func() { _ = it.Close() }()
			if it.SeekToFirst() {
				t.Error("iterator on a closed batch positioned")
			}
		})
	}
}
