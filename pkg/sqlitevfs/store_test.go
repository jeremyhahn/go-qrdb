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

package sqlitevfs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

const testPageSize = 4096

var errInjected = errors.New("injected commit failure")

// failingBackend is an MVCCBackend whose Commit fails without writing: it
// reads through the real backend, so a refused commit is observed on the
// same store a successful one writes to.
type failingBackend struct {
	*MVCCBackend
}

func (f *failingBackend) Commit(context.Context, map[string][]byte, [][]byte) (int64, error) {
	return 0, errInjected
}

func (f *failingBackend) Publish(context.Context, []byte, []byte, int64, map[string][]byte, [][]byte) (int64, error) {
	return 0, errInjected
}

// stagingBackend is an MVCCBackend that counts staging commits (a Commit
// whose puts are all stage keys) and Publish calls and records every stage
// key a staging commit wrote. failPublish makes Publish fail without
// writing; failStageAfter >= 0 makes every staging commit after that many
// successful ones fail without writing.
type stagingBackend struct {
	*MVCCBackend
	stageCommits   int
	publishes      int
	stageKeys      [][]byte
	failPublish    bool
	failStageAfter int
	// onPublish, when set, runs at the start of every Publish call.
	onPublish func()
}

func newStagingBackend(t *testing.T) *stagingBackend {
	t.Helper()
	return &stagingBackend{MVCCBackend: newTestBackend(t), failStageAfter: -1}
}

func (b *stagingBackend) Commit(ctx context.Context, puts map[string][]byte, deletes [][]byte) (int64, error) {
	staging := len(puts) > 0
	for k := range puts {
		if !bytes.Contains([]byte(k), []byte(stageSegment)) {
			staging = false
		}
	}
	if !staging {
		return b.MVCCBackend.Commit(ctx, puts, deletes)
	}
	if b.failStageAfter >= 0 && b.stageCommits >= b.failStageAfter {
		return 0, errInjected
	}
	rev, err := b.MVCCBackend.Commit(ctx, puts, deletes)
	if err != nil {
		return 0, err
	}
	b.stageCommits++
	for k := range puts {
		b.stageKeys = append(b.stageKeys, []byte(k))
	}
	return rev, nil
}

func (b *stagingBackend) Publish(ctx context.Context, src, dst []byte, expectedKeys int64, puts map[string][]byte, deletes [][]byte) (int64, error) {
	b.publishes++
	if b.onPublish != nil {
		b.onPublish()
	}
	if b.failPublish {
		return 0, errInjected
	}
	return b.MVCCBackend.Publish(ctx, src, dst, expectedKeys, puts, deletes)
}

// revOf returns b's current revision, failing the test on a read error.
func revOf(t *testing.T, b Backend) int64 {
	t.Helper()
	r, err := b.Revision(t.Context())
	if err != nil {
		t.Fatalf("Revision: %v", err)
	}
	return r
}

// assertNoStageKeyLive fails if any stage key b recorded has a live value at
// the backend's current revision.
func assertNoStageKeyLive(t *testing.T, b *stagingBackend) {
	t.Helper()
	rev := revOf(t, b)
	for _, key := range b.stageKeys {
		if _, found, err := b.GetAt(t.Context(), key, rev); err != nil || found {
			t.Fatalf("stage key %q at revision %d: found=%v err=%v", key, rev, found, err)
		}
	}
}

func newStagingStore(t *testing.T, b Backend, db string) *Store {
	t.Helper()
	s, err := NewStore(b, db, WithStageThreshold(2*testPageSize), WithStageChunkBytes(testPageSize))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func newTestBackend(t *testing.T) *MVCCBackend {
	t.Helper()
	ms, err := mvcc.New(memory.NewStorage())
	if err != nil {
		t.Fatalf("mvcc.New: %v", err)
	}
	b, err := NewMVCCBackend(ms)
	if err != nil {
		t.Fatalf("NewMVCCBackend: %v", err)
	}
	return b
}

func newTestStore(t *testing.T, b Backend, db string) *Store {
	t.Helper()
	s, err := NewStore(b, db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func begin(t *testing.T, s *Store) *Txn {
	t.Helper()
	txn, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	return txn
}

// pageOf returns a page whose every byte is fill.
func pageOf(fill byte) []byte {
	return bytes.Repeat([]byte{fill}, testPageSize)
}

func writePages(t *testing.T, txn *Txn, fills ...byte) {
	t.Helper()
	for i, fill := range fills {
		n, err := txn.WriteAt(pageOf(fill), int64(i)*testPageSize)
		if err != nil || n != testPageSize {
			t.Fatalf("WriteAt page %d = %d, %v", i+1, n, err)
		}
	}
}

func commit(t *testing.T, txn *Txn) int64 {
	t.Helper()
	rev, err := txn.Commit(context.Background())
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return rev
}

// seed commits a database of one page per fill byte and returns the
// revision.
func seed(t *testing.T, s *Store, fills ...byte) int64 {
	t.Helper()
	txn := begin(t, s)
	writePages(t, txn, fills...)
	return commit(t, txn)
}

func readAll(t *testing.T, txn *Txn) []byte {
	t.Helper()
	buf := make([]byte, txn.Size())
	n, err := txn.ReadAt(buf, 0)
	if err != nil || int64(n) != txn.Size() {
		t.Fatalf("ReadAt whole file = %d, %v", n, err)
	}
	return buf
}

func TestNewStoreAndBackendRefuseNil(t *testing.T) {
	if _, err := NewStore(nil, "db"); !errors.Is(err, ErrNilBackend) {
		t.Fatalf("NewStore(nil) err = %v, want ErrNilBackend", err)
	}
	if _, err := NewMVCCBackend(nil); !errors.Is(err, ErrNilBackend) {
		t.Fatalf("NewMVCCBackend(nil) err = %v, want ErrNilBackend", err)
	}
}

func TestEmptyDatabase(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "db")
	txn := begin(t, s)
	if txn.Size() != 0 {
		t.Fatalf("Size = %d, want 0", txn.Size())
	}
	buf := []byte{1, 2, 3}
	if n, err := txn.ReadAt(buf, 0); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt empty = %d, %v, want 0, io.EOF", n, err)
	}
	if rev := commit(t, txn); rev != 0 {
		t.Fatalf("empty Commit rev = %d, want the pinned revision 0", rev)
	}
}

func TestWriteCommitReadBack(t *testing.T) {
	b := newTestBackend(t)
	s := newTestStore(t, b, "app.db")
	rev := seed(t, s, 0xA1, 0xB2, 0xC3)
	if rev != 1 || revOf(t, b) != 1 {
		t.Fatalf("commit rev = %d, backend revision = %d, want 1 and 1", rev, revOf(t, b))
	}
	txn := begin(t, s)
	if txn.Revision() != rev {
		t.Fatalf("pinned revision = %d, want %d", txn.Revision(), rev)
	}
	if txn.Size() != 3*testPageSize {
		t.Fatalf("Size = %d, want %d", txn.Size(), 3*testPageSize)
	}
	want := append(append(pageOf(0xA1), pageOf(0xB2)...), pageOf(0xC3)...)
	if got := readAll(t, txn); !bytes.Equal(got, want) {
		t.Fatal("read-back bytes differ from the written pages")
	}

	metaKey, _ := MetaKey("app.db")
	value, found, err := b.GetAt(t.Context(), metaKey, rev)
	if err != nil || !found {
		t.Fatalf("meta at rev: found=%v err=%v", found, err)
	}
	var m Meta
	if err := m.UnmarshalBinary(value); err != nil {
		t.Fatal(err)
	}
	if m != (Meta{FormatVersion: 1, PageSize: testPageSize, PageCount: 3, ChangeCounter: 1}) {
		t.Fatalf("committed meta = %+v", m)
	}
}

func TestReadAt(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "db")
	seed(t, s, 0x11, 0x22, 0x33)
	txn := begin(t, s)
	size := int64(3 * testPageSize)

	tests := []struct {
		name    string
		off     int64
		len     int
		wantN   int
		wantErr error
		want    []byte
	}{
		{"offset 0", 0, 100, 100, nil, bytes.Repeat([]byte{0x11}, 100)},
		{"header", 0, 16, 16, nil, bytes.Repeat([]byte{0x11}, 16)},
		{"mid page", testPageSize + 100, 50, 50, nil, bytes.Repeat([]byte{0x22}, 50)},
		{"across a page boundary", testPageSize - 2, 4, 4, nil, []byte{0x11, 0x11, 0x22, 0x22}},
		{"across every page", 10, 2*testPageSize + 10, 2*testPageSize + 10, nil,
			append(append(bytes.Repeat([]byte{0x11}, testPageSize-10), pageOf(0x22)...), bytes.Repeat([]byte{0x33}, 20)...)},
		{"last byte", size - 1, 1, 1, nil, []byte{0x33}},
		{"at end", size, 8, 0, io.EOF, make([]byte, 8)},
		{"wholly past end", size + testPageSize, 8, 0, io.EOF, make([]byte, 8)},
		{"short read past end", size - 4, 10, 4, io.ErrUnexpectedEOF, append(bytes.Repeat([]byte{0x33}, 4), make([]byte, 6)...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := bytes.Repeat([]byte{0xEE}, tc.len)
			n, err := txn.ReadAt(buf, tc.off)
			if n != tc.wantN {
				t.Fatalf("n = %d, want %d", n, tc.wantN)
			}
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !bytes.Equal(buf, tc.want) {
				t.Fatalf("bytes = %x..., want %x...", buf[:min(8, len(buf))], tc.want[:min(8, len(tc.want))])
			}
		})
	}
}

func TestReadSeesBufferedWrites(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "db")
	seed(t, s, 0x11, 0x22)
	txn := begin(t, s)
	writePages(t, txn, 0x99)
	if _, err := txn.WriteAt(pageOf(0x44), 3*testPageSize); err != nil {
		t.Fatal(err)
	}
	if txn.Size() != 4*testPageSize {
		t.Fatalf("Size = %d, want 4 pages", txn.Size())
	}
	want := append(append(append(pageOf(0x99), pageOf(0x22)...), make([]byte, testPageSize)...), pageOf(0x44)...)
	if !bytes.Equal(readAll(t, txn), want) {
		t.Fatal("buffered read differs: want the dirty page 1, the committed page 2, a zero hole and the dirty page 4")
	}
}

func TestWriteRefusals(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "db")

	empty := begin(t, s)
	for _, n := range []int{100, 256, 4000, 1 << 17} {
		if _, err := empty.WriteAt(make([]byte, n), 0); err == nil {
			t.Fatalf("first write of %d bytes accepted", n)
		}
	}
	if _, err := empty.WriteAt(make([]byte, testPageSize), 100); !errors.Is(err, ErrUnalignedIO) {
		t.Fatalf("first write at offset 100 err = %v, want ErrUnalignedIO", err)
	}
	if err := empty.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}

	seed(t, s, 0x11, 0x22)
	txn := begin(t, s)
	tests := []struct {
		name string
		len  int
		off  int64
		want error
	}{
		{"partial page", 100, 0, ErrUnalignedIO},
		{"unaligned offset", testPageSize, 100, ErrUnalignedIO},
		{"page and a half", testPageSize + testPageSize/2, 0, ErrUnalignedIO},
		{"smaller page size", 1024, 0, ErrPageSizeChanged},
		{"larger page size", 8192, 0, ErrPageSizeChanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := txn.WriteAt(make([]byte, tc.len), tc.off); !errors.Is(err, tc.want) {
				t.Fatalf("WriteAt(%d bytes at %d) err = %v, want %v", tc.len, tc.off, err, tc.want)
			}
		})
	}
	if rev := commit(t, txn); rev != 1 {
		t.Fatalf("a txn whose every write was refused committed at %d, want the pinned 1", rev)
	}
}

func TestTruncate(t *testing.T) {
	b := newTestBackend(t)
	s := newTestStore(t, b, "db")
	seed(t, s, 0x11, 0x22, 0x33)

	txn := begin(t, s)
	if err := txn.Truncate(100); !errors.Is(err, ErrUnalignedIO) {
		t.Fatalf("Truncate(100) err = %v, want ErrUnalignedIO", err)
	}
	if err := txn.Truncate(testPageSize); err != nil {
		t.Fatal(err)
	}
	if txn.Size() != testPageSize {
		t.Fatalf("Size after Truncate = %d", txn.Size())
	}
	rev := commit(t, txn)

	after := begin(t, s)
	if after.Size() != testPageSize {
		t.Fatalf("committed Size = %d, want one page", after.Size())
	}
	if !bytes.Equal(readAll(t, after), pageOf(0x11)) {
		t.Fatal("page 1 changed by the truncate")
	}
	buf := make([]byte, 8)
	if n, err := after.ReadAt(buf, testPageSize); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read past the truncated end = %d, %v, want 0, io.EOF", n, err)
	}
	for pgno := uint32(2); pgno <= 3; pgno++ {
		key, _ := PageKey("db", pgno)
		if _, found, err := b.GetAt(t.Context(), key, rev); err != nil || found {
			t.Fatalf("page %d at rev %d: found=%v err=%v, want deleted", pgno, rev, found, err)
		}
		if _, found, _ := b.GetAt(t.Context(), key, rev-1); !found {
			t.Fatalf("page %d gone at the earlier revision %d", pgno, rev-1)
		}
	}

	// Growing again after a truncate in one txn reads the dropped page as a
	// hole, not as the snapshot's old bytes.
	regrow := begin(t, s)
	if err := regrow.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := regrow.WriteAt(pageOf(0x77), testPageSize); err != nil {
		t.Fatal(err)
	}
	want := append(make([]byte, testPageSize), pageOf(0x77)...)
	if !bytes.Equal(readAll(t, regrow), want) {
		t.Fatal("regrown file does not read page 1 as zeros")
	}
	commit(t, regrow)
	if !bytes.Equal(readAll(t, begin(t, s)), want) {
		t.Fatal("committed regrown file does not read page 1 as zeros")
	}
}

func TestSnapshotIsolation(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "db")
	seed(t, s, 0x11, 0x22)

	a := begin(t, s)
	b := begin(t, s)
	writePages(t, b, 0x99, 0x88, 0x77)
	commit(t, b)

	old := append(pageOf(0x11), pageOf(0x22)...)
	if a.Size() != 2*testPageSize || !bytes.Equal(readAll(t, a), old) {
		t.Fatal("txn A pinned before B's commit does not read its own snapshot")
	}
	fresh := begin(t, s)
	if !bytes.Equal(readAll(t, fresh), append(append(pageOf(0x99), pageOf(0x88)...), pageOf(0x77)...)) {
		t.Fatal("a txn begun after B's commit does not read B's pages")
	}
}

func TestCommitIsAtomic(t *testing.T) {
	b := newTestBackend(t)
	s := newTestStore(t, b, "db")
	rev := seed(t, s, 0x11, 0x22)

	failing := newTestStore(t, &failingBackend{b}, "db")
	txn := begin(t, failing)
	writePages(t, txn, 0x99, 0x88, 0x77)
	if err := txn.Truncate(testPageSize); err != nil {
		t.Fatal(err)
	}
	if _, err := txn.Commit(context.Background()); !errors.Is(err, errInjected) {
		t.Fatalf("Commit err = %v, want the injected failure", err)
	}
	if revOf(t, b) != rev {
		t.Fatalf("revision moved to %d after a failed commit, want %d", revOf(t, b), rev)
	}
	after := begin(t, s)
	if after.Size() != 2*testPageSize || !bytes.Equal(readAll(t, after), append(pageOf(0x11), pageOf(0x22)...)) {
		t.Fatal("a failed commit changed the committed pages or meta")
	}
}

func TestRollbackDiscards(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "db")
	rev := seed(t, s, 0x11)
	txn := begin(t, s)
	writePages(t, txn, 0x99, 0x88)
	if err := txn.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := begin(t, s)
	if after.Revision() != rev || !bytes.Equal(readAll(t, after), pageOf(0x11)) {
		t.Fatal("a rolled-back write is visible")
	}
}

func TestUseAfterDone(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "db")
	committed := begin(t, s)
	writePages(t, committed, 0x11)
	commit(t, committed)
	rolledBack := begin(t, s)
	if err := rolledBack.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	for name, txn := range map[string]*Txn{"committed": committed, "rolled back": rolledBack} {
		t.Run(name, func(t *testing.T) {
			if _, err := txn.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrTxnDone) {
				t.Fatalf("ReadAt err = %v", err)
			}
			if _, err := txn.WriteAt(pageOf(0), 0); !errors.Is(err, ErrTxnDone) {
				t.Fatalf("WriteAt err = %v", err)
			}
			if err := txn.Truncate(0); !errors.Is(err, ErrTxnDone) {
				t.Fatalf("Truncate err = %v", err)
			}
			if _, err := txn.Commit(context.Background()); !errors.Is(err, ErrTxnDone) {
				t.Fatalf("Commit err = %v", err)
			}
			if err := txn.Rollback(context.Background()); !errors.Is(err, ErrTxnDone) {
				t.Fatalf("Rollback err = %v", err)
			}
		})
	}
}

func TestStoresAreIsolatedByName(t *testing.T) {
	b := newTestBackend(t)
	one := newTestStore(t, b, "one.db")
	two := newTestStore(t, b, "two.db")
	seed(t, one, 0x11, 0x12)
	seed(t, two, 0x21)

	if got := readAll(t, begin(t, one)); !bytes.Equal(got, append(pageOf(0x11), pageOf(0x12)...)) {
		t.Fatal("one.db reads another database's pages")
	}
	if got := readAll(t, begin(t, two)); !bytes.Equal(got, pageOf(0x21)) {
		t.Fatal("two.db reads another database's pages")
	}
	third := begin(t, newTestStore(t, b, "three.db"))
	if third.Size() != 0 {
		t.Fatalf("an unwritten database has size %d", third.Size())
	}
}

// pagesOf concatenates one test page per fill byte.
func pagesOf(fills ...byte) []byte {
	var out []byte
	for _, fill := range fills {
		out = append(out, pageOf(fill)...)
	}
	return out
}

func TestStagedCommit(t *testing.T) {
	b := newStagingBackend(t)
	s := newStagingStore(t, b, "app.db")
	seed(t, s, 0x01, 0x02)
	if b.stageCommits != 0 || b.publishes != 0 {
		t.Fatalf("2-page commit staged: %d stage commits, %d publishes", b.stageCommits, b.publishes)
	}
	old := begin(t, s)

	txn := begin(t, s)
	writePages(t, txn, 0x11, 0x12, 0x13, 0x14, 0x15)
	rev := commit(t, txn)
	if b.stageCommits != 5 || b.publishes != 1 {
		t.Fatalf("5-page commit: %d stage commits, %d publishes, want 5 and 1", b.stageCommits, b.publishes)
	}
	if rev != revOf(t, b) {
		t.Fatalf("Commit revision %d, backend at %d", rev, revOf(t, b))
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x11, 0x12, 0x13, 0x14, 0x15)) {
		t.Fatal("staged commit does not read back byte for byte")
	}
	if len(b.stageKeys) != 5 {
		t.Fatalf("%d stage keys written, want 5", len(b.stageKeys))
	}
	assertNoStageKeyLive(t, b)
	if got := readAll(t, old); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("a transaction pinned before the staged commit does not read the old bytes")
	}
}

func TestStagedCommitTruncates(t *testing.T) {
	b := newStagingBackend(t)
	s := newStagingStore(t, b, "app.db")
	seed(t, s, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06)
	b.publishes = 0
	txn := begin(t, s)
	if err := txn.Truncate(0); err != nil {
		t.Fatal(err)
	}
	writePages(t, txn, 0x21, 0x22, 0x23)
	commit(t, txn)
	if b.publishes != 1 {
		t.Fatalf("%d publishes, want 1", b.publishes)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x21, 0x22, 0x23)) {
		t.Fatal("staged commit with truncation reads back wrong")
	}
	for pgno := uint32(4); pgno <= 6; pgno++ {
		key, err := PageKey("app.db", pgno)
		if err != nil {
			t.Fatal(err)
		}
		if _, found, err := b.GetAt(t.Context(), key, revOf(t, b)); err != nil || found {
			t.Fatalf("truncated page %d is live: found=%v err=%v", pgno, found, err)
		}
	}
	assertNoStageKeyLive(t, b)
}

func TestStagedCommitPublishFailure(t *testing.T) {
	b := newStagingBackend(t)
	s := newStagingStore(t, b, "app.db")
	before := seed(t, s, 0x01, 0x02)
	b.failPublish = true
	txn := begin(t, s)
	writePages(t, txn, 0x11, 0x12, 0x13, 0x14, 0x15)
	if _, err := txn.Commit(context.Background()); !errors.Is(err, errInjected) {
		t.Fatalf("Commit err = %v, want errInjected", err)
	}
	if b.stageCommits != 5 || b.publishes != 1 {
		t.Fatalf("%d stage commits, %d publishes, want 5 and 1", b.stageCommits, b.publishes)
	}
	assertNoStageKeyLive(t, b)
	after := begin(t, s)
	if after.Size() != 2*testPageSize {
		t.Fatalf("size after a failed publish = %d", after.Size())
	}
	if got := readAll(t, after); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("a failed publish changed the pages")
	}
	if revOf(t, b) <= before {
		t.Fatalf("cleanup wrote no revision: %d <= %d", revOf(t, b), before)
	}
}

func TestStagedCommitStageFailure(t *testing.T) {
	b := newStagingBackend(t)
	s := newStagingStore(t, b, "app.db")
	seed(t, s, 0x01, 0x02)
	b.failStageAfter = 2
	txn := begin(t, s)
	writePages(t, txn, 0x11, 0x12, 0x13, 0x14, 0x15)
	if _, err := txn.Commit(context.Background()); !errors.Is(err, errInjected) {
		t.Fatalf("Commit err = %v, want errInjected", err)
	}
	if b.stageCommits != 2 || b.publishes != 0 {
		t.Fatalf("%d stage commits, %d publishes, want 2 and 0", b.stageCommits, b.publishes)
	}
	assertNoStageKeyLive(t, b)
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("a failed staging changed the pages")
	}
}

func TestStagedCommitCleanupIgnoresCancel(t *testing.T) {
	b := newStagingBackend(t)
	s := newStagingStore(t, b, "app.db")
	seed(t, s, 0x01, 0x02)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b.failPublish = true
	b.onPublish = cancel
	txn := begin(t, s)
	writePages(t, txn, 0x11, 0x12, 0x13, 0x14, 0x15)
	if _, err := txn.Commit(ctx); !errors.Is(err, errInjected) {
		t.Fatalf("Commit err = %v, want errInjected", err)
	}
	if ctx.Err() == nil || b.stageCommits != 5 || b.publishes != 1 {
		t.Fatalf("ctx err %v, %d stage commits, %d publishes, want cancelled, 5 and 1", ctx.Err(), b.stageCommits, b.publishes)
	}
	assertNoStageKeyLive(t, b)
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("a failed publish changed the pages")
	}
}

// ctxBackend is an MVCCBackend that records the context of its last GetAt
// and its last Commit call.
type ctxBackend struct {
	*MVCCBackend
	getCtx    context.Context
	commitCtx context.Context
}

func (b *ctxBackend) GetAt(ctx context.Context, key []byte, rev int64) ([]byte, bool, error) {
	b.getCtx = ctx
	return b.MVCCBackend.GetAt(ctx, key, rev)
}

func (b *ctxBackend) Commit(ctx context.Context, puts map[string][]byte, deletes [][]byte) (int64, error) {
	b.commitCtx = ctx
	return b.MVCCBackend.Commit(ctx, puts, deletes)
}

func TestStoreReadsWithBeginContext(t *testing.T) {
	b := &ctxBackend{MVCCBackend: newTestBackend(t)}
	s := newTestStore(t, b, "app.db")
	seed(t, s, 0x01, 0x02)
	ctxA := context.WithValue(t.Context(), callKey{}, "A")
	ctxB := context.WithValue(t.Context(), callKey{}, "B")
	txn, err := s.Begin(ctxA)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	b.getCtx = nil
	if got := readAll(t, txn); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
		t.Fatal("the seeded pages do not read back")
	}
	if b.getCtx == nil || b.getCtx.Value(callKey{}) != "A" {
		t.Fatalf("page read context = %v, want Begin's context A", b.getCtx)
	}
	writePages(t, txn, 0x11)
	rev, err := txn.Commit(ctxB)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if b.commitCtx == nil || b.commitCtx.Value(callKey{}) != "B" {
		t.Fatalf("Backend.Commit context = %v, want Commit's context B", b.commitCtx)
	}
	if rev != revOf(t, b) {
		t.Fatalf("Commit revision %d, backend at %d", rev, revOf(t, b))
	}
}

func TestMVCCBackendHonoursDoneContext(t *testing.T) {
	b := newTestBackend(t)
	if _, err := b.Commit(t.Context(), map[string][]byte{"src/1": []byte("a")}, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	before := revOf(t, b)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := map[string]func() error{
		"Revision": func() error { _, err := b.Revision(ctx); return err },
		"GetAt":    func() error { _, _, err := b.GetAt(ctx, []byte("src/1"), before); return err },
		"Keys":     func() error { _, err := b.Keys(ctx, []byte("src/"), before); return err },
		"Commit": func() error {
			_, err := b.Commit(ctx, map[string][]byte{"new": []byte("x")}, [][]byte{[]byte("src/1")})
			return err
		},
		"Publish": func() error {
			_, err := b.Publish(ctx, []byte("src/"), []byte("dst/"), 1, map[string][]byte{"new": []byte("x")}, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("%s with a cancelled context = %v, want context.Canceled", name, err)
			}
		})
	}
	if got := revOf(t, b); got != before {
		t.Fatalf("cancelled calls moved the revision to %d, want %d", got, before)
	}
	for _, key := range []string{"new", "dst/1"} {
		if _, found, err := b.GetAt(t.Context(), []byte(key), before); err != nil || found {
			t.Fatalf("cancelled call wrote %q: found=%v err=%v", key, found, err)
		}
	}
	if _, found, err := b.GetAt(t.Context(), []byte("src/1"), before); err != nil || !found {
		t.Fatalf("cancelled call deleted src/1: found=%v err=%v", found, err)
	}
}

func TestPublishCountMismatchWritesNothing(t *testing.T) {
	b := newTestBackend(t)
	if _, err := b.Commit(t.Context(), map[string][]byte{"src/1": []byte("a")}, nil); err != nil {
		t.Fatal(err)
	}
	before := revOf(t, b)
	_, err := b.Publish(t.Context(), []byte("src/"), []byte("dst/"), 2, map[string][]byte{"other": []byte("x")}, nil)
	if !errors.Is(err, ErrPublishConflict) {
		t.Fatalf("Publish with a count mismatch = %v, want ErrPublishConflict", err)
	}
	if got := revOf(t, b); got != before {
		t.Fatalf("failed Publish moved the revision to %d", got)
	}
	if _, found, err := b.GetAt(t.Context(), []byte("dst/1"), before); err != nil || found {
		t.Fatalf("failed Publish wrote dst/1: found=%v err=%v", found, err)
	}
}

func TestPublishRefusesInvalidRequest(t *testing.T) {
	b := newTestBackend(t)
	before := revOf(t, b)
	cases := []struct {
		name     string
		src, dst string
		expected int64
	}{
		{"empty src", "", "dst/", 1},
		{"empty dst", "src/", "", 1},
		{"dst under src", "src/", "src/x/", 1},
		{"src under dst", "dst/x/", "dst/", 1},
		{"zero expected", "src/", "dst/", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := b.Publish(t.Context(), []byte(tc.src), []byte(tc.dst), tc.expected, nil, nil)
			if !errors.Is(err, ErrInvalidPublish) {
				t.Fatalf("Publish = %v, want ErrInvalidPublish", err)
			}
		})
	}
	if got := revOf(t, b); got != before {
		t.Fatalf("refused Publish moved the revision to %d", got)
	}
}

func TestNewStoreRefusesOptions(t *testing.T) {
	b := newTestBackend(t)
	for name, opt := range map[string]Option{
		"threshold 0":  WithStageThreshold(0),
		"threshold -1": WithStageThreshold(-1),
		"chunk 0":      WithStageChunkBytes(0),
		"chunk -4096":  WithStageChunkBytes(-4096),
	} {
		if _, err := NewStore(b, "app.db", opt); !errors.Is(err, ErrInvalidOption) {
			t.Fatalf("NewStore(%s) err = %v, want ErrInvalidOption", name, err)
		}
	}
}

func TestBumpGenerationEmptyWritesNothing(t *testing.T) {
	b := newTestBackend(t)
	s := newTestStore(t, b, "app.db")
	before := revOf(t, b)
	rev, err := s.BumpGeneration(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rev != before || revOf(t, b) != before {
		t.Fatalf("BumpGeneration on an empty db = %d, backend at %d, want %d", rev, revOf(t, b), before)
	}
	if txn := begin(t, s); txn.Size() != 0 {
		t.Fatalf("empty db has size %d after BumpGeneration", txn.Size())
	}
}

func TestGenerationChange(t *testing.T) {
	b := newTestBackend(t)
	s := newTestStore(t, b, "app.db")
	seed(t, s, 0x01, 0x02)
	open := begin(t, s)
	writePages(t, open, 0x31)

	// The restore: another Store writes the restored pages and bumps the
	// generation.
	restorer := newTestStore(t, b, "app.db")
	seed(t, restorer, 0x41, 0x42, 0x43)
	bumped, err := restorer.BumpGeneration(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := open.Commit(context.Background()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("Commit across a restore err = %v, want ErrSnapshotChanged", err)
	}
	if revOf(t, b) != bumped {
		t.Fatalf("refused Commit wrote: revision %d, want %d", revOf(t, b), bumped)
	}
	if _, err := s.Begin(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("first Begin after a restore err = %v, want ErrSnapshotChanged", err)
	}
	txn := begin(t, s)
	if got := readAll(t, txn); !bytes.Equal(got, pagesOf(0x41, 0x42, 0x43)) {
		t.Fatal("Begin after the restore does not read the restored bytes")
	}
	if txn.base.Generation != 1 {
		t.Fatalf("generation after one bump = %d", txn.base.Generation)
	}
	writePages(t, txn, 0x51)
	commit(t, txn)
	if got := begin(t, s).base.Generation; got != 1 {
		t.Fatalf("a commit did not carry the generation forward: %d", got)
	}

	fresh := begin(t, newTestStore(t, b, "app.db"))
	if got := readAll(t, fresh); !bytes.Equal(got, pagesOf(0x51, 0x42, 0x43)) {
		t.Fatal("a Store that first pins after the bump reads wrong bytes")
	}
}

// newLockedStore returns a staging test Store over b whose writers are
// serialized through locker as holder.
func newLockedStore(t *testing.T, b Backend, locker lock.Locker, holder string) *Store {
	t.Helper()
	s, err := NewStore(b, "app.db", WithStageThreshold(2*testPageSize), WithStageChunkBytes(testPageSize), WithWriterLock(newWriter(t, locker, holder)))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// beginReserved begins a transaction on s and reserves the writer lock.
func beginReserved(t *testing.T, s *Store) *Txn {
	t.Helper()
	txn := begin(t, s)
	if err := txn.Reserve(t.Context()); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return txn
}

func changeCounter(t *testing.T, s *Store) uint64 {
	t.Helper()
	meta, _, err := s.readMeta(t.Context(), revOf(t, s.backend))
	if err != nil {
		t.Fatalf("readMeta: %v", err)
	}
	return meta.ChangeCounter
}

func TestWriterLockSerializesWriters(t *testing.T) {
	ctx := t.Context()
	b := newTestBackend(t)
	locker := newMemLocker()
	sa := newLockedStore(t, b, locker, "a")
	sb := newLockedStore(t, b, locker, "b")

	first := begin(t, sa)
	if _, err := first.WriteAt(pageOf(0x01), 0); !errors.Is(err, ErrNotReserved) {
		t.Fatalf("WriteAt before Reserve err = %v, want ErrNotReserved", err)
	}
	if err := first.Truncate(0); !errors.Is(err, ErrNotReserved) {
		t.Fatalf("Truncate before Reserve err = %v, want ErrNotReserved", err)
	}
	if err := first.Reserve(ctx); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := first.Reserve(ctx); err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	writePages(t, first, 0x01)
	commit(t, first)
	if got := changeCounter(t, sa); got != 1 {
		t.Fatalf("change counter after one commit = %d, want 1", got)
	}

	ta := beginReserved(t, sa)
	tb := begin(t, sb)
	if err := tb.Reserve(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("Reserve while held err = %v, want ErrBusy", err)
	}
	if got := readAll(t, tb); !bytes.Equal(got, pageOf(0x01)) {
		t.Fatal("a busy transaction cannot read its snapshot")
	}
	writePages(t, ta, 0x02)
	commit(t, ta)
	if err := tb.Reserve(ctx); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("stale snapshot Reserve err = %v, want ErrSnapshotChanged", err)
	}
	if err := tb.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	fresh := beginReserved(t, sb)
	writePages(t, fresh, 0x03)
	commit(t, fresh)
	if got := readAll(t, begin(t, sa)); !bytes.Equal(got, pageOf(0x03)) {
		t.Fatal("the fresh writer's commit is not visible")
	}
	if got := changeCounter(t, sa); got != 3 {
		t.Fatalf("change counter after three commits = %d, want 3", got)
	}
}

func TestWriterLeaseLostFailsCommit(t *testing.T) {
	b := newTestBackend(t)
	locker := newMemLocker()
	sa := newLockedStore(t, b, locker, "a")
	sb := newLockedStore(t, b, locker, "b")
	seeded := beginReserved(t, sa)
	writePages(t, seeded, 0x01)
	before := commit(t, seeded)

	txn := beginReserved(t, sa)
	writePages(t, txn, 0x09)
	locker.expire(sa.lockKey)
	if _, err := txn.Commit(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("Commit after lease loss err = %v, want ErrBusy", err)
	}
	if _, err := txn.Commit(t.Context()); !errors.Is(err, ErrTxnDone) {
		t.Fatalf("Commit again err = %v, want ErrTxnDone", err)
	}
	if revOf(t, b) != before {
		t.Fatalf("a commit without its lease wrote revision %d, want %d", revOf(t, b), before)
	}
	if got := readAll(t, begin(t, sb)); !bytes.Equal(got, pageOf(0x01)) {
		t.Fatal("a commit without its lease changed the pages")
	}
	other := beginReserved(t, sb)
	if err := other.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

func TestWriterLockReleasedAtRollbackAndReadOnlyCommit(t *testing.T) {
	b := newTestBackend(t)
	locker := newMemLocker()
	sa := newLockedStore(t, b, locker, "a")
	sb := newLockedStore(t, b, locker, "b")

	rolled := beginReserved(t, sa)
	writePages(t, rolled, 0x01)
	if err := rolled.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	readOnly := beginReserved(t, sb)
	rev := readOnly.Revision()
	if got := commit(t, readOnly); got != rev {
		t.Fatalf("read-only Commit = %d, want %d", got, rev)
	}
	last := beginReserved(t, sa)
	if err := last.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if l, err := locker.Status(t.Context(), sa.lockKey); err != nil || l != nil {
		t.Fatalf("lock after Rollback = %v, %v, want released", l, err)
	}
}

func TestReserveSweepsAbandonedStages(t *testing.T) {
	ctx := t.Context()
	b := newStagingBackend(t)
	locker := newMemLocker()
	sa := newLockedStore(t, b, locker, "a")
	sb := newLockedStore(t, b, locker, "b")
	seeded := beginReserved(t, sa)
	writePages(t, seeded, 0x01, 0x02)
	commit(t, seeded)

	// A writer stages five pages and its publish fails.
	b.failPublish = true
	crashed := beginReserved(t, sa)
	writePages(t, crashed, 0x11, 0x12, 0x13, 0x14, 0x15)
	if _, err := crashed.Commit(ctx); !errors.Is(err, errInjected) {
		t.Fatalf("Commit err = %v, want errInjected", err)
	}
	b.failPublish = false
	if len(b.stageKeys) != 5 {
		t.Fatalf("%d stage keys, want 5", len(b.stageKeys))
	}
	// Model a writer that crashed before its cleanup: its stage keys are
	// live and it still holds the lock.
	leftovers := make(map[string][]byte, len(b.stageKeys))
	for _, key := range b.stageKeys {
		leftovers[string(key)] = pageOf(0xEE)
	}
	if _, err := b.MVCCBackend.Commit(t.Context(), leftovers, nil); err != nil {
		t.Fatalf("rewrite stage keys: %v", err)
	}
	if _, err := locker.TryLock(ctx, lock.LockOptions{Key: sa.lockKey, HolderID: "a", TTL: time.Minute}); err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	txn := begin(t, sb)
	if err := txn.Reserve(ctx); !errors.Is(err, ErrBusy) {
		t.Fatalf("Reserve while the crashed writer holds the lock err = %v, want ErrBusy", err)
	}
	locker.expire(sa.lockKey)
	rev := revOf(t, b)
	if err := txn.Reserve(ctx); err != nil {
		t.Fatalf("Reserve after expiry: %v", err)
	}
	if revOf(t, b) != rev+1 {
		t.Fatalf("sweep wrote revisions %d..%d, want one", rev+1, revOf(t, b))
	}
	assertNoStageKeyLive(t, b)
	root, err := StageRoot("app.db")
	if err != nil {
		t.Fatalf("StageRoot: %v", err)
	}
	if keys, err := b.Keys(t.Context(), root, revOf(t, b)); err != nil || len(keys) != 0 {
		t.Fatalf("stage keys after the sweep = %q, %v", keys, err)
	}
	writePages(t, txn, 0x21)
	commit(t, txn)
	if got := readAll(t, begin(t, sa)); !bytes.Equal(got, pagesOf(0x21, 0x02)) {
		t.Fatal("the sweeping writer's commit is not visible")
	}
	// A reserve with nothing to sweep writes no revision.
	rev = revOf(t, b)
	idle := beginReserved(t, sa)
	if revOf(t, b) != rev {
		t.Fatalf("Reserve with no stages wrote revision %d", revOf(t, b))
	}
	if err := idle.Rollback(ctx); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
}

func TestStoreWithoutWriterLock(t *testing.T) {
	ctx := t.Context()
	b := newTestBackend(t)
	s := newTestStore(t, b, "app.db")
	seed(t, s, 0x01)
	rev := revOf(t, b)
	txn := begin(t, s)
	if err := txn.Reserve(ctx); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := txn.Reserve(ctx); err != nil {
		t.Fatalf("second Reserve: %v", err)
	}
	if revOf(t, b) != rev {
		t.Fatal("Reserve without a writer lock wrote a revision")
	}
	writePages(t, txn, 0x02)
	commit(t, txn)
	// Without the lock two writers on one snapshot both commit (the
	// optimistic read-set check is a later slice); the counter still counts.
	one, two := begin(t, s), begin(t, s)
	writePages(t, one, 0x03)
	writePages(t, two, 0x04)
	commit(t, one)
	commit(t, two)
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pageOf(0x04)) {
		t.Fatal("the last unlocked commit is not visible")
	}
	if got := changeCounter(t, s); got != 3 {
		t.Fatalf("change counter = %d, want 3", got)
	}
}

func TestMVCCBackendKeys(t *testing.T) {
	b := newTestBackend(t)
	if keys, err := b.Keys(t.Context(), []byte("p/"), 0); err != nil || keys != nil {
		t.Fatalf("Keys at revision 0 = %q, %v", keys, err)
	}
	rev1, err := b.Commit(t.Context(), map[string][]byte{"p/a": []byte("1"), "q/a": []byte("x")}, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rev2, err := b.Commit(t.Context(), map[string][]byte{"p/b": []byte("2")}, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rev3, err := b.Commit(t.Context(), nil, [][]byte{[]byte("p/a")})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	for _, tc := range []struct {
		rev  int64
		want []string
	}{
		{rev1, []string{"p/a"}},
		{rev2, []string{"p/a", "p/b"}},
		{rev3, []string{"p/b"}},
	} {
		keys, err := b.Keys(t.Context(), []byte("p/"), tc.rev)
		if err != nil {
			t.Fatalf("Keys at %d: %v", tc.rev, err)
		}
		got := make([]string, 0, len(keys))
		for _, k := range keys {
			got = append(got, string(k))
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("Keys at %d = %v, want %v", tc.rev, got, tc.want)
		}
	}
}
