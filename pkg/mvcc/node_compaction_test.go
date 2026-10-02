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

package mvcc

import (
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// nodeKeys are outside every database namespace; tenantKeys are inside one.
var (
	nodeKeys   = []string{"__sys:users/42", "raw-key", "__sys:roles"}
	tenantKeys = []string{"acme/app/k", "system/system/k", "beta/db/deep/key", "__sys:cluster/features/x"}
)

// TestCompactNodeKeyspace_NeverTouchesATenantPrefix: the node-keyspace
// compaction discards the history of keys outside every database namespace,
// and of no key inside one -- data entries, change log and as-of reads alike.
func TestCompactNodeKeyspace_NeverTouchesATenantPrefix(t *testing.T) {
	s := newTestStore(t)
	first := map[string]int64{}
	for _, k := range append(append([]string{}, nodeKeys...), tenantKeys...) {
		kv, err := s.Put([]byte(k), []byte("v1"), 0)
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		first[k] = kv.ModRevision
	}
	for _, k := range append(append([]string{}, nodeKeys...), tenantKeys...) {
		if _, err := s.Put([]byte(k), []byte("v2"), 0); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	cut := s.CurrentRevision()

	if err := s.CompactNodeKeyspace(cut); err != nil {
		t.Fatalf("CompactNodeKeyspace: %v", err)
	}

	for _, k := range tenantKeys {
		kv, err := s.GetAtRevision([]byte(k), first[k])
		if err != nil || string(kv.Value) != "v1" {
			t.Fatalf("TENANT HISTORY DISCARDED: %q at %d = %v, %v", k, first[k], kv, err)
		}
		if got := s.CompactedRevisionForKey([]byte(k)); got != 0 {
			t.Fatalf("floor over %q = %d, want 0", k, got)
		}
	}
	for _, k := range nodeKeys {
		var compacted *RevisionCompactedError
		if _, err := s.GetAtRevision([]byte(k), first[k]); !errors.As(err, &compacted) {
			t.Fatalf("node key %q at %d = %v, want compacted", k, first[k], err)
		}
		if kv, err := s.Get([]byte(k)); err != nil || string(kv.Value) != "v2" {
			t.Fatalf("node key %q lost its current value: %v, %v", k, kv, err)
		}
	}

	// The change log of every tenant key survives; no node key's does.
	seen := map[string]int{}
	if err := s.ScanChanges(1, cut, func(c Change) bool {
		seen[string(c.Key)]++
		return true
	}); err != nil {
		t.Fatalf("ScanChanges: %v", err)
	}
	for _, k := range tenantKeys {
		if seen[k] != 2 {
			t.Fatalf("tenant key %q change log = %d entries, want 2", k, seen[k])
		}
	}
	for _, k := range nodeKeys {
		if seen[k] != 0 {
			t.Fatalf("node key %q change log survived (%d)", k, seen[k])
		}
	}

	// A range confined to one database ignores the node floor; any range that
	// may hold a node key is held to it.
	if got := s.CompactedRevisionForRange([]byte("acme/app/"), []byte("acme/app0")); got != 0 {
		t.Fatalf("acme/app/ range floor = %d, want 0", got)
	}
	if _, _, _, err := s.Range([]byte("acme/app/"), []byte("acme/app0"), service.WithRevision(first["acme/app/k"])); err != nil {
		t.Fatalf("historical range inside a database = %v", err)
	}
	if got := s.CompactedRevisionForRange([]byte("a"), []byte("z")); got != cut {
		t.Fatalf("a range spanning node keys reports floor %d, want %d", got, cut)
	}
}

func TestCompactNodeKeyspace_ValidatesAndPersists(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	s, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.Put([]byte("raw"), []byte{byte(i)}, 0); err != nil {
			t.Fatal(err)
		}
	}
	var invalid *CompactRevisionInvalidError
	if err := s.CompactNodeKeyspace(0); !errors.As(err, &invalid) {
		t.Fatalf("revision 0 = %v", err)
	}
	var future *CompactRevisionFutureError
	if err := s.CompactNodeKeyspace(9); !errors.As(err, &future) {
		t.Fatalf("future revision = %v", err)
	}
	if err := s.CompactNodeKeyspace(2); err != nil {
		t.Fatal(err)
	}
	var compacted *RevisionCompactedError
	if err := s.CompactNodeKeyspace(2); !errors.As(err, &compacted) || compacted.Compacted != 2 {
		t.Fatalf("repeat = %v", err)
	}
	reopened, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.NodeCompactionFloor() != 2 {
		t.Fatalf("node floor after reopen = %d", reopened.NodeCompactionFloor())
	}
	if reopened.CompactedRevisionForKey([]byte("acme/app/x")) != 0 {
		t.Fatal("the node floor covers a tenant key")
	}

	if err := store.Set(NodeCompactionFloorKey(), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(store); err == nil || !strings.Contains(err.Error(), "8 bytes") {
		t.Fatalf("a corrupt node floor loaded: %v", err)
	}
}

// The batch form leaves nothing in storage until its batch commits.
func TestCompactNodeKeyspaceInBatch(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	s, _ := New(store)
	for i := 0; i < 3; i++ {
		if _, err := s.Put([]byte("raw"), []byte{byte(i)}, 0); err != nil {
			t.Fatal(err)
		}
	}
	batch := store.Batch()
	if err := s.CompactNodeKeyspaceInBatch(batch, 2); err != nil {
		t.Fatal(err)
	}
	if raw, _ := store.Get(EncodeDataKey([]byte("raw"), 1)); raw == nil {
		t.Fatal("the compaction reached storage before its batch committed")
	}
	if err := batch.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = batch.Close()
	if raw, _ := store.Get(EncodeDataKey([]byte("raw"), 1)); raw != nil {
		t.Fatal("the committed compaction left the old entry")
	}
	if err := s.CompactNodeKeyspaceInBatch(store.Batch(), 0); err == nil {
		t.Fatal("an invalid revision was queued")
	}
}
