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

package partition

import (
	"fmt"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// ---------------------------------------------------------------------------
// Batch reads across partitions
//
// A partition batch lazily creates a per-partition batch on first write, so a
// read must consult the pending batch when one exists for the key's partition
// and the committed partition otherwise. Getting that wrong in either direction
// breaks storage.Batch's read-your-own-writes contract.
// ---------------------------------------------------------------------------

// TestPartitionBatch_GetSeesPendingWrite requires a buffered write to be
// readable through the batch before it commits.
func TestPartitionBatch_GetSeesPendingWrite(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	b := m.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("pending"), []byte("value")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := b.Get([]byte("pending"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "value" {
		t.Fatalf("Get = %q, want %q", got, "value")
	}

	if committed, cerr := m.Get([]byte("pending")); cerr != nil || committed != nil {
		t.Fatalf("uncommitted write leaked into the manager: %q (err %v)", committed, cerr)
	}
}

// TestPartitionBatch_GetReadsUntouchedPartition requires a key routed to a
// partition this batch has not written to still to read its committed value.
func TestPartitionBatch_GetReadsUntouchedPartition(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	// Seed enough keys that at least one lands in a partition the batch below
	// never touches, then verify every one of them reads back.
	for i := 0; i < 32; i++ {
		key := []byte(fmt.Sprintf("seed-%02d", i))
		if err := m.Set(key, []byte("committed")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	b := m.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("only-one"), []byte("pending")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	for i := 0; i < 32; i++ {
		key := []byte(fmt.Sprintf("seed-%02d", i))
		got, err := b.Get(key)
		if err != nil {
			t.Fatalf("Get %s: %v", key, err)
		}
		if string(got) != "committed" {
			t.Fatalf("Get %s = %q, want committed", key, got)
		}
	}
}

// TestPartitionBatch_GetShadowsAndHides requires a pending write to shadow the
// committed value and a pending delete to hide it.
func TestPartitionBatch_GetShadowsAndHides(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("shadow"), []byte("committed")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := m.Set([]byte("hidden"), []byte("committed")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := m.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("shadow"), []byte("pending")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Delete([]byte("hidden")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := b.Get([]byte("shadow"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "pending" {
		t.Fatalf("Get shadow = %q, want pending", got)
	}

	hidden, err := b.Get([]byte("hidden"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hidden != nil {
		t.Fatalf("Get hidden = %q, want nil", hidden)
	}
}

// TestPartitionBatch_GetAfterClose requires a read on a closed batch to fail.
func TestPartitionBatch_GetAfterClose(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	b := m.Batch()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := b.Get([]byte("k")); err == nil {
		t.Fatal("Get on a closed batch returned no error")
	}
}

// TestPartitionBatch_IteratorMergesEveryPartition requires the merged scan to
// span all partitions, taking pending writes from the ones this batch touched
// and committed data from the rest.
func TestPartitionBatch_IteratorMergesEveryPartition(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	for i := 0; i < 8; i++ {
		if err := m.Set([]byte(fmt.Sprintf("c%02d", i)), []byte("committed")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	b := m.Batch()
	defer func() { _ = b.Close() }()

	for i := 0; i < 8; i++ {
		if err := b.Set([]byte(fmt.Sprintf("p%02d", i)), []byte("pending")); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()

	var keys []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		keys = append(keys, string(it.Key()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	sort.Strings(keys)

	if len(keys) != 16 {
		t.Fatalf("merged scan returned %d keys (%v), want 16", len(keys), keys)
	}
	for i := 0; i < 8; i++ {
		if keys[i] != fmt.Sprintf("c%02d", i) {
			t.Fatalf("keys[%d] = %q, want c%02d", i, keys[i], i)
		}
		if keys[8+i] != fmt.Sprintf("p%02d", i) {
			t.Fatalf("keys[%d] = %q, want p%02d", 8+i, keys[8+i], i)
		}
	}
}

// TestPartitionBatch_IteratorHonoursBounds requires the caller's range to reach
// every partition iterator.
func TestPartitionBatch_IteratorHonoursBounds(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	if err := m.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := m.Batch()
	defer func() { _ = b.Close() }()

	for _, k := range []string{"b", "z"} {
		if err := b.Set([]byte(k), []byte("pending")); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}

	it := b.NewIterator(&storage.IteratorOptions{
		LowerBound: []byte("b"),
		UpperBound: []byte("m"),
	})
	defer func() { _ = it.Close() }()

	var keys []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		keys = append(keys, string(it.Key()))
	}
	if got := fmt.Sprint(keys); got != "[b]" {
		t.Fatalf("bounded batch scan = %s, want [b]", got)
	}
}

// TestPartitionBatch_IteratorAfterClose requires a closed batch to hand back an
// iterator that never positions.
func TestPartitionBatch_IteratorAfterClose(t *testing.T) {
	m := newTestManager(t, 4)
	defer func() { _ = m.Close() }()

	b := m.Batch()
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()

	if it.SeekToFirst() {
		t.Error("iterator on a closed batch positioned")
	}
}
