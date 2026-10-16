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

package mvcc

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// errBatchFault is the failure batchFaultStorage injects.
var errBatchFault = errors.New("injected batch fault")

// batchFaultStorage is a real in-memory storage whose batches fail one
// operation on demand: Set of a key in the setPrefix keyspace, Delete of a key
// in the deletePrefix keyspace, or Commit. A zero prefix fails nothing. Every
// other operation, and every read, is the real storage's.
type batchFaultStorage struct {
	storage.Storage
	setPrefix    byte
	deletePrefix byte
	failCommit   bool
}

func (s *batchFaultStorage) Batch() storage.Batch {
	return &batchFaultBatch{Batch: s.Storage.Batch(), s: s}
}

type batchFaultBatch struct {
	storage.Batch
	s *batchFaultStorage
}

func (b *batchFaultBatch) Set(key, value []byte) error {
	if b.s.setPrefix != 0 && len(key) > 0 && key[0] == b.s.setPrefix {
		return errBatchFault
	}
	return b.Batch.Set(key, value)
}

func (b *batchFaultBatch) Delete(key []byte) error {
	if b.s.deletePrefix != 0 && len(key) > 0 && key[0] == b.s.deletePrefix {
		return errBatchFault
	}
	return b.Batch.Delete(key)
}

func (b *batchFaultBatch) Commit() error {
	if b.s.failCommit {
		return errBatchFault
	}
	return b.Batch.Commit()
}

var _ storage.Storage = (*batchFaultStorage)(nil)

// newBatchFaultStore returns a store over a fault-injecting storage, seeded
// with key "k" = "v1" while no fault is armed.
func newBatchFaultStore(t *testing.T) (*Store, *batchFaultStorage) {
	t.Helper()
	mem := memory.NewStorage()
	t.Cleanup(func() { _ = mem.Close() })
	fs := &batchFaultStorage{Storage: mem}
	s, err := New(fs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Put([]byte("k"), []byte("v1"), 0); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	return s, fs
}

// requireStoreError asserts err is a *StoreError for op wrapping the injected
// fault.
func requireStoreError(t *testing.T, err error, op string) {
	t.Helper()
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("err = %T (%v), want *StoreError", err, err)
	}
	if se.Op != op {
		t.Errorf("Op = %q, want %q", se.Op, op)
	}
	if !errors.Is(err, errBatchFault) {
		t.Errorf("err = %v, want it to wrap the injected fault", err)
	}
}

// requireValue asserts the committed value of key "k".
func requireValue(t *testing.T, s *Store, want string) {
	t.Helper()
	kv, err := s.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(kv.Value) != want {
		t.Errorf("value = %q, want %q: a failed write must commit nothing", kv.Value, want)
	}
}

func TestPut_BatchFaultCommitsNothing(t *testing.T) {
	cases := []struct {
		name   string
		arm    func(*batchFaultStorage)
		wantOp string
	}{
		{"data", func(fs *batchFaultStorage) { fs.setPrefix = prefixData }, "put data"},
		{"meta", func(fs *batchFaultStorage) { fs.setPrefix = prefixMeta }, "put meta"},
		{"revision", func(fs *batchFaultStorage) { fs.setPrefix = prefixRevision }, "put revision"},
		{"rev index", func(fs *batchFaultStorage) { fs.setPrefix = prefixRevIndex }, "put rev index"},
		{"commit", func(fs *batchFaultStorage) { fs.failCommit = true }, "put commit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, fs := newBatchFaultStore(t)
			tc.arm(fs)
			kv, err := s.Put([]byte("k"), []byte("v2"), 0)
			if kv != nil {
				t.Fatalf("Put returned %+v alongside its error", kv)
			}
			requireStoreError(t, err, tc.wantOp)
			requireValue(t, s, "v1")
		})
	}
}

func TestDelete_BatchFaultCommitsNothing(t *testing.T) {
	cases := []struct {
		name   string
		arm    func(*batchFaultStorage)
		wantOp string
	}{
		{"data", func(fs *batchFaultStorage) { fs.setPrefix = prefixData }, "delete data"},
		{"meta", func(fs *batchFaultStorage) { fs.deletePrefix = prefixMeta }, "delete meta"},
		{"revision", func(fs *batchFaultStorage) { fs.setPrefix = prefixRevision }, "delete revision"},
		{"rev index", func(fs *batchFaultStorage) { fs.setPrefix = prefixRevIndex }, "put rev index"},
		{"commit", func(fs *batchFaultStorage) { fs.failCommit = true }, "delete commit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, fs := newBatchFaultStore(t)
			tc.arm(fs)
			kv, err := s.Delete([]byte("k"))
			if kv != nil {
				t.Fatalf("Delete returned %+v alongside its error", kv)
			}
			requireStoreError(t, err, tc.wantOp)
			requireValue(t, s, "v1")
		})
	}
}

func TestPutInBatch_BatchFault(t *testing.T) {
	cases := []struct {
		prefix byte
		wantOp string
	}{
		{prefixData, "batch put data"},
		{prefixMeta, "batch put meta"},
		{prefixRevIndex, "put rev index"},
	}
	for _, tc := range cases {
		t.Run(tc.wantOp, func(t *testing.T) {
			s, fs := newBatchFaultStore(t)
			fs.setPrefix = tc.prefix
			batch := s.Storage().Batch()
			defer func() { _ = batch.Close() }()
			kv, err := s.PutInBatch(batch, []byte("k"), []byte("v2"), 0, 50)
			if kv != nil {
				t.Fatalf("PutInBatch returned %+v alongside its error", kv)
			}
			requireStoreError(t, err, tc.wantOp)
		})
	}
}

func TestDeleteInBatch_BatchFault(t *testing.T) {
	cases := []struct {
		arm    func(*batchFaultStorage)
		wantOp string
	}{
		{func(fs *batchFaultStorage) { fs.setPrefix = prefixData }, "batch delete data"},
		{func(fs *batchFaultStorage) { fs.deletePrefix = prefixMeta }, "batch delete meta"},
		{func(fs *batchFaultStorage) { fs.setPrefix = prefixRevIndex }, "put rev index"},
	}
	for _, tc := range cases {
		t.Run(tc.wantOp, func(t *testing.T) {
			s, fs := newBatchFaultStore(t)
			tc.arm(fs)
			batch := s.Storage().Batch()
			defer func() { _ = batch.Close() }()
			kv, err := s.DeleteInBatch(batch, []byte("k"), 50)
			if kv != nil {
				t.Fatalf("DeleteInBatch returned %+v alongside its error", kv)
			}
			requireStoreError(t, err, tc.wantOp)
		})
	}
}

func TestGetInBatch_ReadsTheBatchsOwnWrites(t *testing.T) {
	s, _ := newBatchFaultStore(t)
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()

	rev := s.CurrentRevision() + 1
	if _, err := s.PutInBatch(batch, []byte("k"), []byte("v2"), 0, rev); err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}
	if _, err := s.PutInBatch(batch, []byte("new"), []byte("n1"), 0, rev); err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}
	s.SetRevision(rev)

	for key, want := range map[string]string{"k": "v2", "new": "n1"} {
		kv, err := s.GetInBatch(batch, []byte(key))
		if err != nil {
			t.Fatalf("GetInBatch(%q): %v", key, err)
		}
		if string(kv.Value) != want || kv.ModRevision != rev {
			t.Errorf("GetInBatch(%q) = %q at %d, want %q at %d", key, kv.Value, kv.ModRevision, want, rev)
		}
	}
	// Nothing is visible outside the batch until it commits.
	requireValue(t, s, "v1")
	if _, err := s.Get([]byte("new")); err == nil {
		t.Error("Get(new) found a key only an uncommitted batch wrote")
	}
}

func TestGetInBatch_KeyTheBatchDeleted(t *testing.T) {
	s, _ := newBatchFaultStore(t)
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()

	rev := s.CurrentRevision() + 1
	if _, err := s.DeleteInBatch(batch, []byte("k"), rev); err != nil {
		t.Fatalf("DeleteInBatch: %v", err)
	}
	s.SetRevision(rev)
	_, err := s.GetInBatch(batch, []byte("k"))
	var nf *KeyNotFoundError
	if !errors.As(err, &nf) || nf.Key != "k" {
		t.Fatalf("GetInBatch after DeleteInBatch: err = %T (%v), want *KeyNotFoundError for k", err, err)
	}
	requireValue(t, s, "v1")
}

func TestRangeInBatch_SeesTheBatchsWrites(t *testing.T) {
	s, _ := newBatchFaultStore(t)
	if _, err := s.Put([]byte("k2"), []byte("w1"), 0); err != nil {
		t.Fatalf("Put: %v", err)
	}
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()

	// As the engine's apply path does: each in-batch write takes the next
	// revision and advances the store to it before the batch commits.
	rev := s.CurrentRevision() + 1
	if _, err := s.PutInBatch(batch, []byte("k1"), []byte("x1"), 0, rev); err != nil {
		t.Fatalf("PutInBatch: %v", err)
	}
	s.SetRevision(rev)
	rev++
	if _, err := s.DeleteInBatch(batch, []byte("k2"), rev); err != nil {
		t.Fatalf("DeleteInBatch: %v", err)
	}
	s.SetRevision(rev)

	kvs, count, _, err := s.RangeInBatch(batch, []byte("k"), []byte("l"))
	if err != nil {
		t.Fatalf("RangeInBatch: %v", err)
	}
	var got [][]byte
	for _, kv := range kvs {
		got = append(got, kv.Key)
	}
	want := [][]byte{[]byte("k"), []byte("k1")}
	if count != int64(len(want)) || len(got) != len(want) || !bytes.Equal(got[0], want[0]) || !bytes.Equal(got[1], want[1]) {
		t.Fatalf("RangeInBatch keys = %q (count %d), want %q", got, count, want)
	}

	// The committed view still has k2 and not k1.
	kvs, _, _, err = s.Range([]byte("k"), []byte("l"))
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	got = got[:0]
	for _, kv := range kvs {
		got = append(got, kv.Key)
	}
	if len(got) != 2 || !bytes.Equal(got[1], []byte("k2")) {
		t.Errorf("committed Range keys = %q, want [k k2]", got)
	}
}

func TestRangeInBatch_EmptyRange(t *testing.T) {
	s, _ := newBatchFaultStore(t)
	batch := s.Storage().Batch()
	defer func() { _ = batch.Close() }()

	kvs, count, _, err := s.RangeInBatch(batch, []byte("x"), []byte("y"))
	if err != nil {
		t.Fatalf("RangeInBatch: %v", err)
	}
	if len(kvs) != 0 || count != 0 {
		t.Errorf("RangeInBatch over an empty range = %d keys (count %d), want none", len(kvs), count)
	}
}
