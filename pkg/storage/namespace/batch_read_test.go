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

package namespace

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newNamespacedEngine returns a namespaced engine over fresh memory storage,
// plus the inner storage so a test can check what the prefixing produced.
func newNamespacedEngine(t *testing.T) (*Engine, storage.Storage) {
	t.Helper()
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })
	return New(inner, []byte("tenant")), inner
}

// TestBatch_GetSeesPendingWriteWithinNamespace requires a batch read to observe
// the batch's own pending write, addressed by the unprefixed key.
func TestBatch_GetSeesPendingWriteWithinNamespace(t *testing.T) {
	e, _ := newNamespacedEngine(t)

	b := e.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("k"), []byte("pending")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "pending" {
		t.Fatalf("Get = %q, want %q", got, "pending")
	}
}

// TestBatch_GetFallsThroughToNamespacedCommittedValue requires an untouched key
// to read the committed value stored under the namespace prefix.
func TestBatch_GetFallsThroughToNamespacedCommittedValue(t *testing.T) {
	e, _ := newNamespacedEngine(t)
	if err := e.Set([]byte("k"), []byte("committed")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := e.Batch()
	defer func() { _ = b.Close() }()

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "committed" {
		t.Fatalf("Get = %q, want %q", got, "committed")
	}
}

// TestBatch_GetIsIsolatedFromOtherNamespaces requires a key written under a
// different namespace to stay invisible to this batch.
func TestBatch_GetIsIsolatedFromOtherNamespaces(t *testing.T) {
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })

	other := New(inner, []byte("other"))
	if err := other.Set([]byte("k"), []byte("theirs")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := New(inner, []byte("tenant")).Batch()
	defer func() { _ = b.Close() }()

	got, err := b.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Fatalf("batch read across namespaces: got %q", got)
	}
}

// TestBatch_GetRejectsEmptyKey requires the empty key to be refused.
func TestBatch_GetRejectsEmptyKey(t *testing.T) {
	e, _ := newNamespacedEngine(t)

	b := e.Batch()
	defer func() { _ = b.Close() }()

	if _, err := b.Get(nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("Get(nil) = %v, want ErrEmptyKey", err)
	}
}

// TestBatch_IteratorMergesAndStripsPrefix requires a scan through the batch to
// merge pending and committed keys and to return them unprefixed.
func TestBatch_IteratorMergesAndStripsPrefix(t *testing.T) {
	e, _ := newNamespacedEngine(t)
	for _, k := range []string{"a", "c"} {
		if err := e.Set([]byte(k), []byte("committed-"+k)); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}

	b := e.Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("b"), []byte("pending-b")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := b.Delete([]byte("c")); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()

	var pairs []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		pairs = append(pairs, string(it.Key())+"="+string(it.Value()))
	}
	if err := it.Error(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	want := "[a=committed-a b=pending-b]"
	if got := fmt.Sprint(pairs); got != want {
		t.Fatalf("merged scan = %s, want %s", got, want)
	}
}

// TestBatch_IteratorExcludesOtherNamespaces requires the batch scan to stay
// inside its own namespace even when the inner store holds other tenants.
func TestBatch_IteratorExcludesOtherNamespaces(t *testing.T) {
	inner := memory.NewStorage()
	t.Cleanup(func() { _ = inner.Close() })

	if err := New(inner, []byte("aaa")).Set([]byte("k"), []byte("theirs")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := New(inner, []byte("zzz")).Set([]byte("k"), []byte("theirs")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := New(inner, []byte("tenant")).Batch()
	defer func() { _ = b.Close() }()

	if err := b.Set([]byte("mine"), []byte("ours")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	it := b.NewIterator(nil)
	defer func() { _ = it.Close() }()

	var pairs []string
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		pairs = append(pairs, string(it.Key())+"="+string(it.Value()))
	}
	want := "[mine=ours]"
	if got := fmt.Sprint(pairs); got != want {
		t.Fatalf("namespaced batch scan = %s, want %s", got, want)
	}
}

// TestBatch_IteratorHonoursBounds requires caller bounds to be translated into
// the namespace's key range.
func TestBatch_IteratorHonoursBounds(t *testing.T) {
	e, _ := newNamespacedEngine(t)
	if err := e.Set([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	b := e.Batch()
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
	want := "[b]"
	if got := fmt.Sprint(keys); got != want {
		t.Fatalf("bounded batch scan = %s, want %s", got, want)
	}
}
