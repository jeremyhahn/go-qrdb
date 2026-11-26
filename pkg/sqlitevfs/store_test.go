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
	"encoding/json"
	"errors"
	"io"
	"math"
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

func (f *failingBackend) Commit(context.Context, []ReadCheck, map[string][]byte, [][]byte) (int64, error) {
	return 0, errInjected
}

func (f *failingBackend) Publish(context.Context, []byte, []byte, int64, []ReadCheck, map[string][]byte, [][]byte) (int64, error) {
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
	// onStage, when set, runs after every staging commit lands.
	onStage func()
}

func newStagingBackend(t *testing.T) *stagingBackend {
	t.Helper()
	return &stagingBackend{MVCCBackend: newTestBackend(t), failStageAfter: -1}
}

func (b *stagingBackend) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	staging := len(puts) > 0
	for k := range puts {
		if !bytes.Contains([]byte(k), []byte(stageSegment)) {
			staging = false
		}
	}
	if !staging {
		return b.MVCCBackend.Commit(ctx, reads, puts, deletes)
	}
	if b.failStageAfter >= 0 && b.stageCommits >= b.failStageAfter {
		return 0, errInjected
	}
	rev, err := b.MVCCBackend.Commit(ctx, reads, puts, deletes)
	if err != nil {
		return 0, err
	}
	b.stageCommits++
	for k := range puts {
		b.stageKeys = append(b.stageKeys, []byte(k))
	}
	if b.onStage != nil {
		b.onStage()
	}
	return rev, nil
}

func (b *stagingBackend) Publish(ctx context.Context, src, dst []byte, expectedKeys int64, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	b.publishes++
	if b.onPublish != nil {
		b.onPublish()
	}
	if b.failPublish {
		return 0, errInjected
	}
	return b.MVCCBackend.Publish(ctx, src, dst, expectedKeys, reads, puts, deletes)
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
	// Reserve is a no-op on a Store without a writer lock.
	txn := beginReserved(t, s)
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

func (b *ctxBackend) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	b.commitCtx = ctx
	return b.MVCCBackend.Commit(ctx, reads, puts, deletes)
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
	if _, err := b.Commit(t.Context(), nil, map[string][]byte{"src/1": []byte("a")}, nil); err != nil {
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
			_, err := b.Commit(ctx, nil, map[string][]byte{"new": []byte("x")}, [][]byte{[]byte("src/1")})
			return err
		},
		"Publish": func() error {
			_, err := b.Publish(ctx, []byte("src/"), []byte("dst/"), 1, nil, map[string][]byte{"new": []byte("x")}, nil)
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

// errBackend is an MVCCBackend whose Revision, GetAt, Keys and Commit fail
// with errInjected, without reading or writing, while the matching field is
// set.
type errBackend struct {
	*MVCCBackend
	failRevision, failGet, failKeys, failCommit bool
}

func (b *errBackend) Revision(ctx context.Context) (int64, error) {
	if b.failRevision {
		return 0, errInjected
	}
	return b.MVCCBackend.Revision(ctx)
}

func (b *errBackend) GetAt(ctx context.Context, key []byte, rev int64) ([]byte, bool, error) {
	if b.failGet {
		return nil, false, errInjected
	}
	return b.MVCCBackend.GetAt(ctx, key, rev)
}

func (b *errBackend) Keys(ctx context.Context, prefix []byte, rev int64) ([][]byte, error) {
	if b.failKeys {
		return nil, errInjected
	}
	return b.MVCCBackend.Keys(ctx, prefix, rev)
}

func (b *errBackend) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if b.failCommit {
		return 0, errInjected
	}
	return b.MVCCBackend.Commit(ctx, reads, puts, deletes)
}

func TestStoreBackendErrors(t *testing.T) {
	tests := []struct {
		name string
		// fail sets the failing method after the database is seeded.
		fail func(*errBackend)
		call func(*testing.T, *Store) error
	}{
		{"Begin revision", func(b *errBackend) { b.failRevision = true }, func(t *testing.T, s *Store) error {
			txn, err := s.Begin(t.Context())
			if txn != nil {
				t.Fatal("a failed Begin returned a transaction")
			}
			return err
		}},
		{"Begin meta read", func(b *errBackend) { b.failGet = true }, func(t *testing.T, s *Store) error {
			_, err := s.Begin(t.Context())
			return err
		}},
		{"BumpGeneration revision", func(b *errBackend) { b.failRevision = true }, func(t *testing.T, s *Store) error {
			_, err := s.BumpGeneration(t.Context())
			return err
		}},
		{"BumpGeneration meta read", func(b *errBackend) { b.failGet = true }, func(t *testing.T, s *Store) error {
			_, err := s.BumpGeneration(t.Context())
			return err
		}},
		{"BumpGeneration commit", func(b *errBackend) { b.failCommit = true }, func(t *testing.T, s *Store) error {
			_, err := s.BumpGeneration(t.Context())
			return err
		}},
		{"page read", func(b *errBackend) { b.failGet = true }, func(t *testing.T, s *Store) error {
			s.backend.(*errBackend).failGet = false
			txn := begin(t, s)
			s.backend.(*errBackend).failGet = true
			_, err := txn.ReadAt(make([]byte, 8), 0)
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &errBackend{MVCCBackend: newTestBackend(t)}
			s := newTestStore(t, b, "app.db")
			if s.DB() != "app.db" {
				t.Fatalf("DB = %q, want app.db", s.DB())
			}
			before := seed(t, s, 0x01, 0x02)
			tc.fail(b)
			if err := tc.call(t, s); !errors.Is(err, errInjected) {
				t.Fatalf("err = %v, want errInjected", err)
			}
			*b = errBackend{MVCCBackend: b.MVCCBackend}
			if got := revOf(t, b); got != before {
				t.Fatalf("a failed call moved the revision to %d, want %d", got, before)
			}
			if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x01, 0x02)) {
				t.Fatal("a failed call changed the pages")
			}
		})
	}
}

func TestReserveBackendErrorsReleaseTheLock(t *testing.T) {
	tests := []struct {
		name string
		fail func(*errBackend)
	}{
		{"revision", func(b *errBackend) { b.failRevision = true }},
		{"meta read", func(b *errBackend) { b.failGet = true }},
		{"stage sweep", func(b *errBackend) { b.failKeys = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &errBackend{MVCCBackend: newTestBackend(t)}
			locker := newMemLocker()
			s := newLockedStore(t, b, locker, "a")
			seeded := beginReserved(t, s)
			writePages(t, seeded, 0x01)
			before := commit(t, seeded)
			txn := begin(t, s)
			tc.fail(b)
			if err := txn.Reserve(t.Context()); !errors.Is(err, errInjected) {
				t.Fatalf("Reserve err = %v, want errInjected", err)
			}
			*b = errBackend{MVCCBackend: b.MVCCBackend}
			if l, err := locker.Status(t.Context(), s.lockKey); err != nil || l != nil {
				t.Fatalf("lock after a failed Reserve = %v, %v, want released", l, err)
			}
			if _, err := txn.WriteAt(pageOf(0x09), 0); !errors.Is(err, ErrNotReserved) {
				t.Fatalf("WriteAt after a failed Reserve err = %v, want ErrNotReserved", err)
			}
			if got := revOf(t, b); got != before {
				t.Fatalf("a failed Reserve moved the revision to %d, want %d", got, before)
			}
		})
	}
}

func TestStoreRefusesCorruptRecords(t *testing.T) {
	b := newTestBackend(t)
	s := newTestStore(t, b, "app.db")
	seed(t, s, 0x01, 0x02)
	pageKey, err := PageKey("app.db", 2)
	if err != nil {
		t.Fatalf("PageKey: %v", err)
	}
	if _, err := b.Commit(t.Context(), nil, map[string][]byte{string(pageKey): []byte("short")}, nil); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	txn := begin(t, s)
	buf := make([]byte, 2*testPageSize)
	if n, err := txn.ReadAt(buf, 0); err == nil || n != testPageSize {
		t.Fatalf("ReadAt over a short page = %d, %v, want page 1 and an error", n, err)
	}

	metaKey, err := MetaKey("app.db")
	if err != nil {
		t.Fatalf("MetaKey: %v", err)
	}
	rev, err := b.Commit(t.Context(), nil, map[string][]byte{string(metaKey): []byte("garbage")}, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := s.Begin(t.Context()); !errors.Is(err, ErrInvalidMeta) {
		t.Fatalf("Begin over a corrupt meta record err = %v, want ErrInvalidMeta", err)
	}
	if _, err := s.BumpGeneration(t.Context()); !errors.Is(err, ErrInvalidMeta) {
		t.Fatalf("BumpGeneration over a corrupt meta record err = %v, want ErrInvalidMeta", err)
	}
	if got := revOf(t, b); got != rev {
		t.Fatalf("a refused BumpGeneration moved the revision to %d, want %d", got, rev)
	}
}

func TestTxnChangeCounter(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "app.db")

	empty := begin(t, s)
	if got := empty.ChangeCounter(); got != 0 {
		t.Fatalf("ChangeCounter of an empty store = %d, want 0", got)
	}
	if err := empty.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	seed(t, s, 0x01)
	seed(t, s, 0x02)
	txn := begin(t, s)
	defer func() { _ = txn.Rollback(context.Background()) }()
	if got := txn.ChangeCounter(); got != 2 {
		t.Fatalf("ChangeCounter after two commits = %d, want 2", got)
	}
}

func TestStoreBeginAt(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "app.db")
	first := seed(t, s, 0x01)
	second := seed(t, s, 0x02)

	for _, tc := range []struct {
		rev     int64
		fill    byte
		counter uint64
	}{{first, 0x01, 1}, {second, 0x02, 2}} {
		txn, err := s.BeginAt(t.Context(), tc.rev)
		if err != nil {
			t.Fatalf("BeginAt(%d): %v", tc.rev, err)
		}
		if got := txn.Revision(); got != tc.rev {
			t.Fatalf("BeginAt(%d).Revision() = %d", tc.rev, got)
		}
		if got := txn.ChangeCounter(); got != tc.counter {
			t.Fatalf("BeginAt(%d).ChangeCounter() = %d, want %d", tc.rev, got, tc.counter)
		}
		if got := readAll(t, txn); !bytes.Equal(got, pageOf(tc.fill)) {
			t.Fatalf("BeginAt(%d) reads page fill %#x, want %#x", tc.rev, got[0], tc.fill)
		}
		if err := txn.Rollback(t.Context()); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
	}
	if _, err := s.BeginAt(t.Context(), -1); err == nil {
		t.Fatal("BeginAt(-1) succeeded")
	}
}

func TestTxnCommitNoNetChangeWritesNoRevision(t *testing.T) {
	b := newTestBackend(t)
	s := newTestStore(t, b, "app.db")
	rev := seed(t, s, 0x01, 0x02)

	// Spilled writes and a page past the end, then the journal played
	// back over them and the file truncated to its old size.
	txn := beginReserved(t, s)
	writePages(t, txn, 0xAA, 0xBB, 0xCC)
	writePages(t, txn, 0x01, 0x02)
	if err := txn.Truncate(2 * testPageSize); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if got := commit(t, txn); got != rev {
		t.Fatalf("no-net-change Commit = revision %d, want the pinned %d", got, rev)
	}
	if got := revOf(t, b); got != rev {
		t.Fatalf("backend revision moved from %d to %d", rev, got)
	}

	// Only new pages spilled: the played-back pages are written once,
	// equal to the snapshot, and the file shrinks back.
	txn = beginReserved(t, s)
	writePages(t, txn, 0x01, 0x02, 0xCC, 0xDD)
	if err := txn.Truncate(2 * testPageSize); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if got := commit(t, txn); got != rev {
		t.Fatalf("no-net-change Commit after a shrink = revision %d, want the pinned %d", got, rev)
	}

	// A rewrite with one real change commits only that change.
	txn = beginReserved(t, s)
	writePages(t, txn, 0xAA, 0xBB)
	writePages(t, txn, 0x01, 0x09)
	next := commit(t, txn)
	if next == rev {
		t.Fatal("a commit with a changed page wrote no revision")
	}
	check := begin(t, s)
	defer func() { _ = check.Rollback(context.Background()) }()
	if got := readAll(t, check); !bytes.Equal(got, pagesOf(0x01, 0x09)) {
		t.Fatalf("file after the rewrite commit is not pages 0x01, 0x09")
	}
}

func TestTxnIOEdgeCases(t *testing.T) {
	s := newTestStore(t, newTestBackend(t), "app.db")

	empty := begin(t, s)
	if err := empty.Truncate(testPageSize); !errors.Is(err, ErrUnalignedIO) {
		t.Fatalf("Truncate of an empty file to one page err = %v, want ErrUnalignedIO", err)
	}
	if err := empty.Truncate(0); err != nil || empty.Size() != 0 {
		t.Fatalf("Truncate(0) of an empty file = %v, size %d", err, empty.Size())
	}

	rev := seed(t, s, 0x01, 0x02)
	txn := begin(t, s)
	size := txn.Size()
	if n, err := txn.ReadAt(nil, 0); n != 0 || err != nil {
		t.Fatalf("empty ReadAt = %d, %v, want 0, nil", n, err)
	}
	if n, err := txn.WriteAt(nil, 0); n != 0 || err != nil {
		t.Fatalf("empty WriteAt = %d, %v, want 0, nil", n, err)
	}
	if n, err := txn.ReadAt(make([]byte, 1), -1); n != 0 || err == nil {
		t.Fatalf("ReadAt at -1 = %d, %v, want an error", n, err)
	}
	if n, err := txn.WriteAt(pageOf(0x09), -testPageSize); n != 0 || err == nil {
		t.Fatalf("WriteAt at -%d = %d, %v, want an error", testPageSize, n, err)
	}
	if err := txn.Truncate(-testPageSize); err == nil {
		t.Fatal("Truncate to a negative size succeeded")
	}
	pastLast := int64(math.MaxUint32) * testPageSize
	if _, err := txn.WriteAt(pageOf(0x09), pastLast); !errors.Is(err, ErrInvalidPageNumber) {
		t.Fatalf("WriteAt past the last page err = %v, want ErrInvalidPageNumber", err)
	}
	if err := txn.Truncate(pastLast + testPageSize); !errors.Is(err, ErrInvalidPageNumber) {
		t.Fatalf("Truncate past the last page err = %v, want ErrInvalidPageNumber", err)
	}
	if txn.Size() != size {
		t.Fatalf("refused calls changed the size to %d, want %d", txn.Size(), size)
	}
	if got := commit(t, txn); got != rev {
		t.Fatalf("a transaction with only refused writes committed at %d, want the pinned %d", got, rev)
	}
}

func TestPublishCountMismatchWritesNothing(t *testing.T) {
	b := newTestBackend(t)
	if _, err := b.Commit(t.Context(), nil, map[string][]byte{"src/1": []byte("a")}, nil); err != nil {
		t.Fatal(err)
	}
	before := revOf(t, b)
	_, err := b.Publish(t.Context(), []byte("src/"), []byte("dst/"), 2, nil, map[string][]byte{"other": []byte("x")}, nil)
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
			_, err := b.Publish(t.Context(), []byte(tc.src), []byte(tc.dst), tc.expected, nil, nil, nil)
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

// metaRaceBackend runs race once after each meta read while armed: a commit
// that lands between a reader's read and its write.
type metaRaceBackend struct {
	*MVCCBackend
	metaKey []byte
	armed   int
	race    func()
}

func (b *metaRaceBackend) GetAt(ctx context.Context, key []byte, rev int64) ([]byte, bool, error) {
	value, found, err := b.MVCCBackend.GetAt(ctx, key, rev)
	if b.armed > 0 && bytes.Equal(key, b.metaKey) {
		b.armed--
		b.race()
	}
	return value, found, err
}

// A commit between the bump's meta read and its write survives: the bump's
// read check refuses the stale write, and the retry bumps the new record.
func TestBumpGenerationKeepsAConcurrentCommit(t *testing.T) {
	b := newTestBackend(t)
	writer := newTestStore(t, b, "app.db")
	seed(t, writer, 0x11)
	before := begin(t, newTestStore(t, b, "app.db")).base

	var raced Meta
	rb := &metaRaceBackend{MVCCBackend: b, metaKey: writer.metaKey, armed: 1}
	rb.race = func() {
		seed(t, writer, 0x21, 0x22, 0x23)
		raced = begin(t, newTestStore(t, b, "app.db")).base
	}
	bumper := newTestStore(t, rb, "app.db")
	if _, err := bumper.BumpGeneration(t.Context()); err != nil {
		t.Fatalf("BumpGeneration: %v", err)
	}
	after := begin(t, newTestStore(t, b, "app.db")).base
	if after.PageCount != raced.PageCount || after.ChangeCounter != raced.ChangeCounter {
		t.Fatalf("after the bump meta = %+v, want the concurrent commit's %d pages, change counter %d", after, raced.PageCount, raced.ChangeCounter)
	}
	if after.Generation <= before.Generation {
		t.Fatalf("generation %d after the bump, want past %d", after.Generation, before.Generation)
	}
}

// A database that moves on every attempt is ErrSnapshotChanged, and the
// last concurrent commit stands.
func TestBumpGenerationGivesUpOnAMovingDatabase(t *testing.T) {
	b := newTestBackend(t)
	writer := newTestStore(t, b, "app.db")
	seed(t, writer, 0x11)
	fill := byte(0x30)
	rb := &metaRaceBackend{MVCCBackend: b, metaKey: writer.metaKey, armed: restoreBumpAttempts}
	rb.race = func() {
		fill++
		seed(t, writer, fill)
	}
	if _, err := newTestStore(t, rb, "app.db").BumpGeneration(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("BumpGeneration on a database moving every attempt = %v, want ErrSnapshotChanged", err)
	}
	if got := begin(t, newTestStore(t, b, "app.db")).base.ChangeCounter; got != uint64(restoreBumpAttempts)+1 {
		t.Fatalf("change counter %d, want %d: a concurrent commit was overwritten", got, restoreBumpAttempts+1)
	}
}

// requestRecorder records the requests a Store sends: the count of commits
// and publishes, the largest encoded estimate of a staging commit and of any
// request, and the largest request as the JSON codec encodes it.
type requestRecorder struct {
	*MVCCBackend
	commits, publishes int
	maxCommitEst       int
	maxEst, maxJSON    int
}

func (b *requestRecorder) record(est int, v any) {
	b.maxEst = max(b.maxEst, est)
	if enc, err := json.Marshal(v); err == nil {
		b.maxJSON = max(b.maxJSON, len(enc))
	}
}

func (b *requestRecorder) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	b.commits++
	est := encodedRequestBytes(reads, puts, deletes)
	b.maxCommitEst = max(b.maxCommitEst, est)
	b.record(est, struct {
		Reads   []ReadCheck
		Puts    map[string][]byte
		Deletes [][]byte
	}{reads, puts, deletes})
	return b.MVCCBackend.Commit(ctx, reads, puts, deletes)
}

func (b *requestRecorder) Publish(ctx context.Context, src, dst []byte, expectedKeys int64, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	b.publishes++
	b.record(encodedRequestBytes(reads, puts, deletes, src, dst), struct {
		Src, Dst []byte
		Expected int64
		Reads    []ReadCheck
		Puts     map[string][]byte
		Deletes  [][]byte
	}{src, dst, expectedKeys, reads, puts, deletes})
	return b.MVCCBackend.Publish(ctx, src, dst, expectedKeys, reads, puts, deletes)
}

// commitUnderCap commits eight pages on a Store whose page thresholds never
// stage, with transport request cap limit, and checks the pages read back.
func commitUnderCap(t *testing.T, limit int) *requestRecorder {
	t.Helper()
	b := newTestBackend(t)
	rec := &requestRecorder{MVCCBackend: b}
	s, err := NewStore(rec, "app.db", WithStageThreshold(1<<30), WithStageChunkBytes(1<<30))
	if err != nil {
		t.Fatal(err)
	}
	s.requestBytes = limit
	fills := []byte{0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0x48}
	seed(t, s, fills...)
	if rec.maxEst >= limit || rec.maxJSON >= limit {
		t.Fatalf("cap %d: a request of %d bytes (JSON %d) reached it", limit, rec.maxEst, rec.maxJSON)
	}
	txn := begin(t, newTestStore(t, b, "app.db"))
	if txn.Size() != int64(len(fills))*testPageSize {
		t.Fatalf("cap %d: size %d after the commit, want %d", limit, txn.Size(), len(fills)*testPageSize)
	}
	page := make([]byte, testPageSize)
	for i, fill := range fills {
		if _, err := txn.ReadAt(page, int64(i)*testPageSize); err != nil || page[0] != fill || page[testPageSize-1] != fill {
			t.Fatalf("cap %d: page %d = %#x..%#x, %v, want %#x", limit, i+1, page[0], page[testPageSize-1], err, fill)
		}
	}
	return rec
}

// A direct commit whose encoded request (pages, keys, read checks and
// framing) would reach the transport cap stages instead, even under the
// page-byte threshold; one just under it stays one commit.
func TestCommitStagesBeforeTheEncodedRequestReachesTheCap(t *testing.T) {
	direct := commitUnderCap(t, 1<<30)
	if direct.commits != 1 || direct.publishes != 0 {
		t.Fatalf("uncapped: %d commits, %d publishes, want one direct commit", direct.commits, direct.publishes)
	}
	request := direct.maxEst
	if direct.maxJSON >= request {
		t.Fatalf("JSON request of %d bytes is not under its estimate %d", direct.maxJSON, request)
	}

	under := commitUnderCap(t, request+1)
	if under.commits != 1 || under.publishes != 0 {
		t.Fatalf("cap %d: %d commits, %d publishes, want one direct commit", request+1, under.commits, under.publishes)
	}

	at := commitUnderCap(t, request)
	if at.publishes != 1 || at.commits < 1 {
		t.Fatalf("cap %d (the request): %d commits, %d publishes, want staging and one publish", request, at.commits, at.publishes)
	}
	if at.maxEst >= request || at.maxJSON >= request {
		t.Fatalf("cap %d: a request of %d bytes (JSON %d) reached it", request, at.maxEst, at.maxJSON)
	}
}

// truncPages is a database large enough that deleting all but its first page
// takes several requests under truncCap.
const (
	truncPages = 512
	truncCap   = 3 * testPageSize
)

// seedTruncatable seeds truncPages pages on a Store over backend whose page
// thresholds never stage, then lowers its request cap to truncCap.
func seedTruncatable(t *testing.T, backend Backend) *Store {
	t.Helper()
	s, err := NewStore(backend, "app.db", WithStageThreshold(1<<30), WithStageChunkBytes(1<<30))
	if err != nil {
		t.Fatal(err)
	}
	fills := make([]byte, truncPages)
	for i := range fills {
		fills[i] = byte(i%250 + 1)
	}
	seed(t, s, fills...)
	s.requestBytes = truncCap
	return s
}

// pageKeyCount is the number of live page keys of app.db.
func pageKeyCount(t *testing.T, b *MVCCBackend) int {
	t.Helper()
	prefix, err := PagePrefix("app.db")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := b.Revision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := b.Keys(t.Context(), prefix, rev)
	if err != nil {
		t.Fatal(err)
	}
	return len(keys)
}

// A commit that truncates more pages than one request can carry deletes
// them in requests under the transport cap, direct and staged, and leaves
// only the kept page.
func TestTruncateChunksPageDeletesUnderTheCap(t *testing.T) {
	for _, staged := range []bool{false, true} {
		name := "direct"
		if staged {
			name = "staged"
		}
		t.Run(name, func(t *testing.T) {
			b := newTestBackend(t)
			rec := &requestRecorder{MVCCBackend: b}
			s := seedTruncatable(t, rec)
			*rec = requestRecorder{MVCCBackend: b}
			want := byte(1)
			txn := beginReserved(t, s)
			if err := txn.Truncate(testPageSize); err != nil {
				t.Fatal(err)
			}
			if staged {
				s.stageThreshold = 0
				want = 0x7f
				writePages(t, txn, want)
			}
			commit(t, txn)
			if rec.maxEst >= truncCap || rec.maxJSON >= truncCap {
				t.Fatalf("a request of %d bytes (JSON %d) reached the cap %d", rec.maxEst, rec.maxJSON, truncCap)
			}
			if staged && rec.publishes != 1 {
				t.Fatalf("%d publishes, want 1", rec.publishes)
			}
			if rec.commits+rec.publishes < 3 {
				t.Fatalf("%d commits, %d publishes: the deletes were not split", rec.commits, rec.publishes)
			}
			if n := pageKeyCount(t, b); n != 1 {
				t.Fatalf("%d page keys after the truncate, want 1", n)
			}
			r := begin(t, newTestStore(t, b, "app.db"))
			if r.Size() != testPageSize {
				t.Fatalf("size %d, want %d", r.Size(), testPageSize)
			}
			page := make([]byte, testPageSize)
			if _, err := r.ReadAt(page, 0); err != nil || page[0] != want {
				t.Fatalf("page 1 = %#x, %v, want %#x", page[0], err, want)
			}
		})
	}
}

// trimRefuser refuses every delete-only commit guarded by one read check, as
// if each trimAfter chunk found the meta record moved.
type trimRefuser struct{ *MVCCBackend }

func (b trimRefuser) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if len(puts) == 0 && len(deletes) > 0 && len(reads) == 1 {
		return 0, ErrSnapshotChanged
	}
	return b.MVCCBackend.Commit(ctx, reads, puts, deletes)
}

// Page keys a refused trimAfter leaves behind are past the page count, and a
// later commit that grows over them without writing them deletes them with
// its commit: they read as zeros, never as the truncated pages.
func TestRefusedTrimLeavesNoReachablePage(t *testing.T) {
	b := newTestBackend(t)
	s := seedTruncatable(t, trimRefuser{b})
	txn := beginReserved(t, s)
	if err := txn.Truncate(testPageSize); err != nil {
		t.Fatal(err)
	}
	commit(t, txn)
	if n := pageKeyCount(t, b); n <= 1 {
		t.Fatalf("%d page keys after refused trims, want leftovers past page 1", n)
	}
	s.requestBytes = transportRequestBytes
	txn = beginReserved(t, s)
	if _, err := txn.WriteAt(pageOf(0x7f), int64(truncPages-1)*testPageSize); err != nil {
		t.Fatal(err)
	}
	commit(t, txn)
	if n := pageKeyCount(t, b); n != 2 {
		t.Fatalf("%d page keys after the grow, want 2", n)
	}
	r := begin(t, newTestStore(t, b, "app.db"))
	page := make([]byte, testPageSize)
	for pgno := 2; pgno < truncPages; pgno++ {
		if _, err := r.ReadAt(page, int64(pgno-1)*testPageSize); err != nil || !bytes.Equal(page, make([]byte, testPageSize)) {
			t.Fatalf("hole page %d = %#x.., %v, want zeros", pgno, page[0], err)
		}
	}
}

// A staging commit is flushed before its encoded request would reach the
// transport cap: a chunk exactly one byte under the cap is sent, and at the
// cap the same pages go in smaller chunks.
func TestStageChunkFlushesBeforeTheEncodedRequestReachesTheCap(t *testing.T) {
	direct := commitUnderCap(t, 1<<30)
	half := commitUnderCap(t, direct.maxEst/2)
	if half.publishes != 1 || half.commits < 2 {
		t.Fatalf("cap %d: %d staging commits, %d publishes, want at least two chunks and one publish", direct.maxEst/2, half.commits, half.publishes)
	}
	chunk := half.maxCommitEst

	fits := commitUnderCap(t, chunk+1)
	if fits.maxCommitEst != chunk {
		t.Fatalf("cap %d: largest chunk %d, want the %d-byte chunk sent whole", chunk+1, fits.maxCommitEst, chunk)
	}
	split := commitUnderCap(t, chunk)
	if split.maxCommitEst >= chunk || split.commits <= half.commits {
		t.Fatalf("cap %d: largest chunk %d over %d commits, want smaller chunks than the %d of cap %d",
			chunk, split.maxCommitEst, split.commits, half.commits, direct.maxEst/2)
	}
}

// metaRecordAt returns the raw meta record of db at revision rev.
func metaRecordAt(t *testing.T, b Backend, db string, rev int64) ([]byte, Meta) {
	t.Helper()
	key, err := MetaKey(db)
	if err != nil {
		t.Fatal(err)
	}
	value, found, err := b.GetAt(t.Context(), key, rev)
	if err != nil || !found {
		t.Fatalf("meta of %q at %d: found=%v err=%v", db, rev, found, err)
	}
	var m Meta
	if err := m.UnmarshalBinary(value); err != nil {
		t.Fatal(err)
	}
	return value, m
}

// restoreMeta writes db's meta record of revision rev back as the live one,
// the meta half of a restore to rev, and returns the revision before it.
// seedFresh seeds db through a Store of its own, so the restoring Store's
// generation pin is not the writer's.
func seedFresh(t *testing.T, b Backend, db string, fills ...byte) int64 {
	t.Helper()
	return seed(t, newTestStore(t, b, db), fills...)
}

// imageAt returns db's file bytes at revision rev, read through a fresh
// Store pinned there.
func imageAt(t *testing.T, b Backend, db string, rev int64) []byte {
	t.Helper()
	txn, err := newTestStore(t, b, db).BeginAt(t.Context(), rev)
	if err != nil {
		t.Fatalf("BeginAt(%d): %v", rev, err)
	}
	defer func() { _ = txn.Rollback(t.Context()) }()
	buf := make([]byte, txn.Size())
	if _, err := txn.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt at %d: %v", rev, err)
	}
	return buf
}

func TestStoreRestoreToRevision_OneRevision(t *testing.T) {
	b := newTestBackend(t)
	restorer := newTestStore(t, b, "app.db")
	r := seedFresh(t, b, "app.db", 0x01, 0x02)
	seedFresh(t, b, "app.db", 0x03, 0x04, 0x05)

	seen := []uint64{0}
	for round := range 3 {
		before := revOf(t, b)
		rev, err := restorer.RestoreToRevision(t.Context(), r)
		if err != nil {
			t.Fatalf("round %d: RestoreToRevision: %v", round, err)
		}
		if rev != before+1 || revOf(t, b) != rev {
			t.Fatalf("round %d: restore revision %d, backend at %d, want %d", round, rev, revOf(t, b), before+1)
		}
		if got, want := imageAt(t, b, "app.db", rev), imageAt(t, b, "app.db", r); !bytes.Equal(got, want) {
			t.Fatalf("round %d: image at %d is %d bytes, not the %d bytes at %d", round, rev, len(got), len(want), r)
		}
		_, m := metaRecordAt(t, b, "app.db", rev)
		for _, g := range seen {
			if m.Generation <= g {
				t.Fatalf("round %d: generation %d is not past the earlier %d (seen %v)", round, m.Generation, g, seen)
			}
		}
		seen = append(seen, m.Generation)
		if _, prev := metaRecordAt(t, b, "app.db", before); m.ChangeCounter != prev.ChangeCounter+1 {
			t.Fatalf("round %d: change counter %d, want %d", round, m.ChangeCounter, prev.ChangeCounter+1)
		}
		// The next round restores over a write made after this one.
		txn := beginReserved(t, newTestStore(t, b, "app.db"))
		writePages(t, txn, 0x07)
		commit(t, txn)
	}
}

func TestStoreRestoreToRevision_StagedAboveThreshold(t *testing.T) {
	sb := newStagingBackend(t)
	r := seedFresh(t, sb, "app.db", 0x01, 0x02, 0x03)
	seedFresh(t, sb, "app.db", 0x04, 0x05, 0x06)
	s, err := NewStore(sb, "app.db", WithStageThreshold(testPageSize))
	if err != nil {
		t.Fatal(err)
	}
	publishes := sb.publishes
	rev, err := s.RestoreToRevision(t.Context(), r)
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if sb.publishes != publishes+1 {
		t.Fatalf("publishes = %d, want %d", sb.publishes, publishes+1)
	}
	if rev != revOf(t, sb) {
		t.Fatalf("restore revision %d, backend at %d", rev, revOf(t, sb))
	}
	assertNoStageKeyLive(t, sb)
	if got, want := imageAt(t, sb, "app.db", rev), imageAt(t, sb, "app.db", r); !bytes.Equal(got, want) {
		t.Fatalf("image at %d is not the image at %d", rev, r)
	}
	_, target := metaRecordAt(t, sb, "app.db", r)
	if _, m := metaRecordAt(t, sb, "app.db", rev); m.Generation != target.Generation+1 {
		t.Fatalf("generation %d, want %d", m.Generation, target.Generation+1)
	}
}

func TestStoreRestoreToRevision_OpenTxnIsSnapshotChanged(t *testing.T) {
	b := newTestBackend(t)
	r := seedFresh(t, b, "app.db", 0x01)
	seedFresh(t, b, "app.db", 0x02)
	open := beginReserved(t, newTestStore(t, b, "app.db"))
	writePages(t, open, 0x03, 0x04)
	if _, err := newTestStore(t, b, "app.db").RestoreToRevision(t.Context(), r); err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if _, err := open.Commit(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("Commit of a transaction begun before the restore = %v, want ErrSnapshotChanged", err)
	}
}

func TestStoreRestoreToRevision_RemovesDatabaseAbsentAtRev(t *testing.T) {
	b := newTestBackend(t)
	if _, err := b.Commit(t.Context(), nil, map[string][]byte{"other/1": []byte("x")}, nil); err != nil {
		t.Fatal(err)
	}
	r := revOf(t, b)
	seedFresh(t, b, "app.db", 0x01, 0x02)
	open := beginReserved(t, newTestStore(t, b, "app.db"))
	writePages(t, open, 0x03)
	before := revOf(t, b)
	rev, err := newTestStore(t, b, "app.db").RestoreToRevision(t.Context(), r)
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if rev != before+1 || revOf(t, b) != rev {
		t.Fatalf("restore revision %d, backend at %d, want %d", rev, revOf(t, b), before+1)
	}
	keys, err := b.Keys(t.Context(), []byte(keyRoot+"app.db/"), rev)
	if err != nil || len(keys) != 0 {
		t.Fatalf("keys of app.db after its removal = %q, %v", keys, err)
	}
	if _, err := open.Commit(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("Commit into a removed database = %v, want ErrSnapshotChanged", err)
	}
}

func TestStoreRestoreToRevision_RecreatesDatabase(t *testing.T) {
	b := newTestBackend(t)
	r := seedFresh(t, b, "app.db", 0x01, 0x02)
	metaKey, _ := MetaKey("app.db")
	page1, _ := PageKey("app.db", 1)
	page2, _ := PageKey("app.db", 2)
	if _, err := b.Commit(t.Context(), nil, nil, [][]byte{metaKey, page1, page2}); err != nil {
		t.Fatal(err)
	}
	before := revOf(t, b)
	rev, err := newTestStore(t, b, "app.db").RestoreToRevision(t.Context(), r)
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if rev != before+1 {
		t.Fatalf("restore revision %d, want %d", rev, before+1)
	}
	if got, want := imageAt(t, b, "app.db", rev), imageAt(t, b, "app.db", r); !bytes.Equal(got, want) || len(got) != 2*testPageSize {
		t.Fatalf("recreated image is %d bytes, want the %d bytes at %d", len(got), len(want), r)
	}
	_, target := metaRecordAt(t, b, "app.db", r)
	if _, m := metaRecordAt(t, b, "app.db", rev); m.Generation <= target.Generation || m.Generation < uint64(before) {
		t.Fatalf("generation %d, want past %d and at least %d", m.Generation, target.Generation, before)
	}
}

func TestStoreRestoreToRevision_NoChangeWritesNothing(t *testing.T) {
	b := newTestBackend(t)
	if _, err := b.Commit(t.Context(), nil, map[string][]byte{"other/1": []byte("x")}, nil); err != nil {
		t.Fatal(err)
	}
	empty := revOf(t, b)
	s := newTestStore(t, b, "app.db")
	// Absent at the revision and now.
	rev, err := s.RestoreToRevision(t.Context(), empty)
	if err != nil || rev != empty || revOf(t, b) != empty {
		t.Fatalf("RestoreToRevision of an absent database = %d, %v; backend at %d, want %d", rev, err, revOf(t, b), empty)
	}
	r := seedFresh(t, b, "app.db", 0x01, 0x02)
	rev, err = s.RestoreToRevision(t.Context(), r)
	if err != nil || rev != r || revOf(t, b) != r {
		t.Fatalf("RestoreToRevision to the current image = %d, %v; backend at %d, want %d", rev, err, revOf(t, b), r)
	}
}

func TestDatabaseOfKey(t *testing.T) {
	meta, err := MetaKey("app")
	if err != nil {
		t.Fatal(err)
	}
	stage, err := StageKey("app", "0123456789abcdef0123456789abcdef", 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range [][]byte{meta, stage} {
		if db, ok := DatabaseOfKey(key); !ok || db != "app" {
			t.Fatalf("DatabaseOfKey(%q) = %q, %v, want app, true", key, db, ok)
		}
	}
	for _, key := range []string{"", "sqlite/", "sqlite//meta", "sqlite/app", "other/app/meta", "sqlitex/app/meta"} {
		if db, ok := DatabaseOfKey([]byte(key)); ok {
			t.Fatalf("DatabaseOfKey(%q) = %q, true, want false", key, db)
		}
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
	if txn.base.Generation != open.base.Generation+1 {
		t.Fatalf("generation after one bump = %d, want %d", txn.base.Generation, open.base.Generation+1)
	}
	writePages(t, txn, 0x51)
	commit(t, txn)
	if got := begin(t, s).base.Generation; got != open.base.Generation+1 {
		t.Fatalf("a commit did not carry the generation forward: %d", got)
	}

	fresh := begin(t, newTestStore(t, b, "app.db"))
	if got := readAll(t, fresh); !bytes.Equal(got, pagesOf(0x51, 0x42, 0x43)) {
		t.Fatal("a Store that first pins after the bump reads wrong bytes")
	}
}

func TestStoreRecreatedDatabaseRefusesStaleTxn(t *testing.T) {
	for name, opts := range map[string][]Option{"default": nil, "optimistic": {WithOptimistic()}} {
		t.Run(name, func(t *testing.T) {
			b := newTestBackend(t)
			s, err := NewStore(b, "app.db", opts...)
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			before := revOf(t, b)
			seed(t, s, 0x01, 0x02)
			stale := begin(t, s)
			writePages(t, stale, 0x31, 0x32)

			// A restore removes the database and a writer creates it
			// again with the same page count and change counter.
			if _, err := newTestStore(t, b, "app.db").RestoreToRevision(t.Context(), before); err != nil {
				t.Fatalf("RestoreToRevision: %v", err)
			}
			if _, found, err := s.readMeta(t.Context(), revOf(t, b)); err != nil || found {
				t.Fatalf("meta record after the restore: found %v, %v; want none", found, err)
			}
			seed(t, newTestStore(t, b, "app.db"), 0x41, 0x42)
			recreated := revOf(t, b)

			if _, err := stale.Commit(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
				t.Fatalf("Commit of a transaction on the removed database err = %v, want ErrSnapshotChanged", err)
			}
			if revOf(t, b) != recreated {
				t.Fatalf("refused Commit wrote: revision %d, want %d", revOf(t, b), recreated)
			}
			if _, err := s.Begin(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
				t.Fatalf("first Begin on the recreated database err = %v, want ErrSnapshotChanged", err)
			}
			if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x41, 0x42)) {
				t.Fatal("the recreated database does not read its own pages")
			}
		})
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

func TestWriterLeaseLostDuringStagingFailsPublish(t *testing.T) {
	b := newStagingBackend(t)
	locker := newMemLocker()
	sa := newLockedStore(t, b, locker, "a")
	sb := newLockedStore(t, b, locker, "b")
	seeded := beginReserved(t, sa)
	writePages(t, seeded, 0x01)
	before := commit(t, seeded)

	txn := beginReserved(t, sa)
	writePages(t, txn, 0x07, 0x08, 0x09)
	b.onStage = func() { locker.expire(sa.lockKey) }
	if _, err := txn.Commit(t.Context()); !errors.Is(err, ErrBusy) {
		t.Fatalf("Commit after the lease expired during staging err = %v, want ErrBusy", err)
	}
	b.onStage = nil
	if b.stageCommits == 0 {
		t.Fatal("the commit never staged, so the lease never expired during staging")
	}
	if b.publishes != 0 {
		t.Fatalf("a commit whose lease expired during staging published %d times", b.publishes)
	}
	assertNoStageKeyLive(t, b)
	if got := readAll(t, begin(t, sb)); !bytes.Equal(got, pageOf(0x01)) {
		t.Fatal("a commit whose lease expired during staging changed the pages")
	}
	if revOf(t, b) < before {
		t.Fatalf("revision %d went below the seeded %d", revOf(t, b), before)
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
	if _, err := b.MVCCBackend.Commit(t.Context(), nil, leftovers, nil); err != nil {
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
	// Without the lock and without WithOptimistic, the second of two
	// writers on one snapshot finds the database moved and writes nothing.
	one, two := begin(t, s), begin(t, s)
	writePages(t, one, 0x03)
	writePages(t, two, 0x04)
	commit(t, one)
	if _, err := two.Commit(ctx); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("stale unlocked Commit = %v, want ErrSnapshotChanged", err)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pageOf(0x03)) {
		t.Fatal("the first unlocked commit is not the one visible")
	}
	if got := changeCounter(t, s); got != 3 {
		t.Fatalf("change counter = %d, want 3", got)
	}
}

// racingBackend runs before once, ahead of the first Commit it forwards.
type racingBackend struct {
	*MVCCBackend
	before func()
}

func (r *racingBackend) Commit(ctx context.Context, reads []ReadCheck, puts map[string][]byte, deletes [][]byte) (int64, error) {
	if hook := r.before; hook != nil {
		r.before = nil
		hook()
	}
	return r.MVCCBackend.Commit(ctx, reads, puts, deletes)
}

func TestStoreWriterLockCommitRefusesUnlockedWriter(t *testing.T) {
	ctx := t.Context()
	b := newTestBackend(t)
	unlocked := newTestStore(t, b, "app.db")
	seed(t, unlocked, 0x01)
	race := &racingBackend{MVCCBackend: b}
	locked, err := NewStore(race, "app.db", WithWriterLock(newWriter(t, newMemLocker(), "a")))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	txn := beginReserved(t, locked)
	writePages(t, txn, 0x02)
	race.before = func() {
		other := begin(t, unlocked)
		writePages(t, other, 0x03)
		commit(t, other)
	}
	if _, err := txn.Commit(ctx); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("locked Commit racing an unlocked one = %v, want ErrSnapshotChanged", err)
	}
	if got := readAll(t, begin(t, unlocked)); !bytes.Equal(got, pageOf(0x03)) {
		t.Fatal("the newer unlocked page did not survive")
	}
	if got := changeCounter(t, unlocked); got != 2 {
		t.Fatalf("change counter = %d, want 2", got)
	}
}

func TestStoreRestoreToRevisionFromRefusesNewerCommit(t *testing.T) {
	ctx := t.Context()
	b := newTestBackend(t)
	s := newTestStore(t, b, "app.db")
	seed(t, s, 0x01)
	target := revOf(t, b)
	txn := begin(t, s)
	writePages(t, txn, 0x02)
	commit(t, txn)
	observed := revOf(t, b)
	txn = begin(t, s)
	writePages(t, txn, 0x03)
	commit(t, txn)
	before := revOf(t, b)
	if _, err := s.RestoreToRevisionFrom(ctx, target, observed); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("RestoreToRevisionFrom past a newer commit = %v, want ErrSnapshotChanged", err)
	}
	if revOf(t, b) != before {
		t.Fatal("a refused restore wrote a revision")
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pageOf(0x03)) {
		t.Fatal("the newer commit did not survive a refused restore")
	}
	if _, err := s.RestoreToRevisionFrom(ctx, target, revOf(t, b)); err != nil {
		t.Fatalf("RestoreToRevisionFrom at the current revision: %v", err)
	}
	if _, err := s.Begin(ctx); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("Begin after a restore = %v, want ErrSnapshotChanged", err)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pageOf(0x01)) {
		t.Fatal("the restore from the current revision did not restore the image")
	}
}

func TestMVCCBackendKeys(t *testing.T) {
	b := newTestBackend(t)
	if keys, err := b.Keys(t.Context(), []byte("p/"), 0); err != nil || keys != nil {
		t.Fatalf("Keys at revision 0 = %q, %v", keys, err)
	}
	rev1, err := b.Commit(t.Context(), nil, map[string][]byte{"p/a": []byte("1"), "q/a": []byte("x")}, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rev2, err := b.Commit(t.Context(), nil, map[string][]byte{"p/b": []byte("2")}, nil)
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	rev3, err := b.Commit(t.Context(), nil, nil, [][]byte{[]byte("p/a")})
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

func newOptimisticStore(t *testing.T, b Backend) *Store {
	t.Helper()
	s, err := NewStore(b, "app.db", WithOptimistic(), WithStageThreshold(2*testPageSize), WithStageChunkBytes(testPageSize))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// readPage reads page pgno through txn, which records it in an optimistic
// transaction's read set.
func readPage(t *testing.T, txn *Txn, pgno int64) []byte {
	t.Helper()
	p := make([]byte, testPageSize)
	if n, err := txn.ReadAt(p, (pgno-1)*testPageSize); err != nil || n != testPageSize {
		t.Fatalf("ReadAt page %d = %d, %v", pgno, n, err)
	}
	return p
}

// writePage writes one page at pgno through txn.
func writePage(t *testing.T, txn *Txn, pgno int64, fill byte) {
	t.Helper()
	if n, err := txn.WriteAt(pageOf(fill), (pgno-1)*testPageSize); err != nil || n != testPageSize {
		t.Fatalf("WriteAt page %d = %d, %v", pgno, n, err)
	}
}

func TestOptimistic_DisjointPagesBothCommit(t *testing.T) {
	b := newTestBackend(t)
	s := newOptimisticStore(t, b)
	seed(t, s, 0x01, 0x02)
	one, two := begin(t, s), begin(t, s)
	readPage(t, one, 1)
	writePage(t, one, 1, 0x11)
	readPage(t, two, 2)
	writePage(t, two, 2, 0x22)
	commit(t, one)
	commit(t, two)
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x11, 0x22)) {
		t.Fatal("two disjoint optimistic commits do not both read back")
	}
	if got := changeCounter(t, s); got != 3 {
		t.Fatalf("change counter = %d, want 3", got)
	}
}

func TestOptimistic_OverlappingReadConflicts(t *testing.T) {
	b := newTestBackend(t)
	s := newOptimisticStore(t, b)
	seed(t, s, 0x01, 0x02)
	reader, writer := begin(t, s), begin(t, s)
	if got := readPage(t, reader, 1); !bytes.Equal(got, pageOf(0x01)) {
		t.Fatal("page 1 does not read the seeded bytes")
	}
	writePage(t, reader, 2, 0x22)
	writePage(t, writer, 1, 0x11)
	commit(t, writer)
	rev := revOf(t, b)
	if _, err := reader.Commit(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("Commit over a changed read = %v, want ErrSnapshotChanged", err)
	}
	if revOf(t, b) != rev {
		t.Fatalf("a refused optimistic commit wrote: revision %d, want %d", revOf(t, b), rev)
	}
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x11, 0x02)) {
		t.Fatal("a refused optimistic commit changed the pages")
	}
}

func TestOptimistic_AbsentPageCreatedConflicts(t *testing.T) {
	b := newTestBackend(t)
	s := newOptimisticStore(t, b)
	// Only page 3 is written, so pages 1 and 2 are absent keys.
	seeded := begin(t, s)
	writePage(t, seeded, 3, 0x03)
	commit(t, seeded)
	reader, writer := begin(t, s), begin(t, s)
	if got := readPage(t, reader, 2); !bytes.Equal(got, pageOf(0x00)) {
		t.Fatal("an absent page does not read as zeros")
	}
	writePage(t, reader, 1, 0x11)
	writePage(t, writer, 2, 0x22)
	commit(t, writer)
	rev := revOf(t, b)
	if _, err := reader.Commit(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("Commit over a page read as absent and since created = %v, want ErrSnapshotChanged", err)
	}
	if revOf(t, b) != rev {
		t.Fatalf("a refused optimistic commit wrote: revision %d, want %d", revOf(t, b), rev)
	}
}

func TestOptimistic_StagedCommitConflictsAtPublish(t *testing.T) {
	b := newStagingBackend(t)
	s := newOptimisticStore(t, b)
	seed(t, s, 0x01, 0x02)
	reader, writer := begin(t, s), begin(t, s)
	readPage(t, reader, 1)
	writePages(t, reader, 0x11, 0x12, 0x13)
	writePage(t, writer, 1, 0x21)
	commit(t, writer)
	if _, err := reader.Commit(t.Context()); !errors.Is(err, ErrSnapshotChanged) {
		t.Fatalf("staged Commit over a changed read = %v, want ErrSnapshotChanged", err)
	}
	if b.stageCommits == 0 || b.publishes != 1 {
		t.Fatalf("conflicting staged commit: %d stage commits, %d publishes, want some and 1", b.stageCommits, b.publishes)
	}
	assertNoStageKeyLive(t, b)
	if got := readAll(t, begin(t, s)); !bytes.Equal(got, pagesOf(0x21, 0x02)) {
		t.Fatal("a refused staged commit changed the pages")
	}
}

func TestOptimistic_RefusesWriterLock(t *testing.T) {
	b := newTestBackend(t)
	_, err := NewStore(b, "app.db", WithOptimistic(), WithWriterLock(newWriter(t, newMemLocker(), "a")))
	if !errors.Is(err, ErrOptimisticWithWriterLock) || !errors.Is(err, ErrInvalidOption) {
		t.Fatalf("NewStore(WithOptimistic, WithWriterLock) = %v, want ErrOptimisticWithWriterLock", err)
	}
}
