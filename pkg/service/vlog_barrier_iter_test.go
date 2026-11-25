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

//go:build qrdb_vlog

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// countingKV counts the body reads of its store, refuses Get or List with
// a set error, and records whether anything scanned it (an eager read of
// every value).
type countingKV struct {
	kvstore.KVStore
	gets    int
	getErr  error
	listErr error
	scanned bool
}

func (c *countingKV) Get(ctx context.Context, key string) ([]byte, error) {
	c.gets++
	if c.getErr != nil {
		return nil, c.getErr
	}
	return c.KVStore.Get(ctx, key)
}

func (c *countingKV) List(ctx context.Context, prefix string) ([]string, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return c.KVStore.List(ctx, prefix)
}

func (c *countingKV) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	c.scanned = true
	return c.KVStore.Scan(ctx, prefix, fn)
}

// newCountingBarrierReader seeds a memory store with log records a, b1,
// b2, b3, c and z, each holding "body-<key>".
func newCountingBarrierReader(t *testing.T) (barrierKVReader, *countingKV) {
	t.Helper()
	ctx := context.Background()
	mem := kvstore.NewMemoryKVStore()
	for _, k := range []string{"z", "b3", "a", "b1", "c", "b2"} {
		if err := mem.Put(ctx, barrierLogKey([]byte(k)), []byte("body-"+k)); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}
	kv := &countingKV{KVStore: mem}
	return barrierKVReader{ctx: ctx, kv: kv}, kv
}

var bBounds = &storage.IteratorOptions{LowerBound: []byte("b"), UpperBound: []byte("c")}

func TestBarrierKVReader_IteratorReadsBodiesOnDemand(t *testing.T) {
	r, kv := newCountingBarrierReader(t)
	it := r.NewIterator(bBounds)
	defer it.Close()
	var got []string
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		got = append(got, string(it.Key()))
	}
	if len(got) != 3 || got[0] != "b1" || got[1] != "b2" || got[2] != "b3" {
		t.Fatalf("keys = %q, want [b1 b2 b3]", got)
	}
	if kv.gets != 0 || kv.scanned {
		t.Fatalf("walking keys read %d bodies (scanned %v); want none", kv.gets, kv.scanned)
	}
	if !it.Seek([]byte("b2")) || string(it.Key()) != "b2" {
		t.Fatalf("Seek(b2) at %q, want b2", it.Key())
	}
	if v := it.Value(); string(v) != "body-b2" || kv.gets != 1 {
		t.Fatalf("Value at b2 = %q after %d Gets, want body-b2 after 1", v, kv.gets)
	}
	if v := it.Value(); string(v) != "body-b2" || kv.gets != 1 {
		t.Fatalf("second Value at b2 = %q after %d Gets, want the cached body", v, kv.gets)
	}
	if !it.Seek([]byte("b3")) || string(it.Key()) != "b3" {
		t.Fatalf("Seek(b3) at %q, want b3", it.Key())
	}
	if !it.Prev() || string(it.Key()) != "b2" {
		t.Fatalf("Prev at %q, want b2", it.Key())
	}
	if !it.SeekToLast() || string(it.Key()) != "b3" {
		t.Fatalf("SeekToLast at %q, want b3", it.Key())
	}
	if it.Next() || it.Valid() || it.Key() != nil || it.Value() != nil {
		t.Fatal("Next past the last key stayed valid")
	}
	if it.Seek([]byte("b4")) {
		t.Fatalf("Seek(b4) at %q, want past the end", it.Key())
	}
	if kv.gets != 1 || kv.scanned || it.Error() != nil {
		t.Fatalf("iterator read %d bodies (scanned %v, err %v); want 1, no scan, no error", kv.gets, kv.scanned, it.Error())
	}
}

func TestBarrierKVReader_IteratorFaults(t *testing.T) {
	r, kv := newCountingBarrierReader(t)
	kv.listErr = errors.New("list refused")
	it := r.NewIterator(bBounds)
	if it.SeekToFirst() || it.Valid() || !errors.Is(it.Error(), kv.listErr) {
		t.Fatalf("iterator over a failed listing: valid %v, error %v; want invalid with the list error", it.Valid(), it.Error())
	}

	kv.listErr = nil
	kv.getErr = errors.New("get refused")
	it = r.NewIterator(bBounds)
	if !it.SeekToFirst() {
		t.Fatalf("SeekToFirst = false (%v), want b1", it.Error())
	}
	if v := it.Value(); v != nil || it.Valid() || !errors.Is(it.Error(), kv.getErr) {
		t.Fatalf("Value over a failed Get = %q, valid %v, error %v; want nil, invalid, the get error", v, it.Valid(), it.Error())
	}
	if it.SeekToFirst() || it.Seek([]byte("b")) || it.Next() || it.Prev() || it.SeekToLast() {
		t.Fatal("a positioning call succeeded after a Get error")
	}

	it = r.NewIterator(&storage.IteratorOptions{Reverse: true})
	if !errors.Is(it.Error(), errReverseUnsupported) {
		t.Fatalf("Reverse iterator error = %v, want errReverseUnsupported", it.Error())
	}
}
