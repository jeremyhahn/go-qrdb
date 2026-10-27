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

package vlog

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"
	"github.com/transparency-dev/merkle/testonly"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// proofSweepSize is the largest tree the proof sweep below builds; every
// size up to it is checked.
const proofSweepSize = 2500

// sampleBelow returns a few distinct values in [0, n): both ends, the
// middle, the largest power of two below n and a value that is none of
// those.
func sampleBelow(n uint64) []uint64 {
	if n == 0 {
		return nil
	}
	seen := make(map[uint64]bool)
	var out []uint64
	pow := uint64(1)
	for pow*2 < n {
		pow *= 2
	}
	for _, v := range []uint64{0, n - 1, n / 2, pow - 1, pow, (n * 7) / 11} {
		if v < n && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// verifyAllProofs checks, at every tree size up to n, inclusion proofs for
// sampled indices and consistency proofs from sampled earlier sizes, each
// with the merkle library's verifier against ref's roots -- an independent
// implementation of the tree -- and byte for byte against ref's own proofs.
func verifyAllProofs(t *testing.T, r storage.Reader, n uint64, ref *testonly.Tree) {
	t.Helper()
	h := rfc6962.DefaultHasher
	for size := uint64(1); size <= n; size++ {
		root := ref.HashAt(size)
		for _, index := range sampleBelow(size) {
			got, err := Inclusion(r, testLog, index, size)
			if err != nil {
				t.Fatalf("Inclusion(%d, %d): %v", index, size, err)
			}
			if err := proof.VerifyInclusion(h, index, size, ref.LeafHash(index), got, root); err != nil {
				t.Fatalf("VerifyInclusion(%d, %d): %v", index, size, err)
			}
			want, err := ref.InclusionProof(index, size)
			if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("Inclusion(%d, %d) differs from the reference proof", index, size)
			}
		}
		for _, size1 := range append(sampleBelow(size), size) {
			got, err := Consistency(r, testLog, size1, size)
			if err != nil {
				t.Fatalf("Consistency(%d, %d): %v", size1, size, err)
			}
			if err := proof.VerifyConsistency(h, size1, size, got, ref.HashAt(size1), root); err != nil {
				t.Fatalf("VerifyConsistency(%d, %d): %v", size1, size, err)
			}
			want, err := ref.ConsistencyProof(size1, size)
			if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("Consistency(%d, %d) differs from the reference proof", size1, size)
			}
		}
	}
}

// TestProofs_VerifyAtEverySize builds a log one entry at a time and checks
// its root against the reference after every append, then every size's
// proofs with the library's verifier.
func TestProofs_VerifyAtEverySize(t *testing.T) {
	c, stg := mustCreate(t, false)
	ref := testonly.New(rfc6962.DefaultHasher)
	for i := range proofSweepSize {
		if _, err := c.AppendEntries(stg, testLog, [][]byte{entry(i)}); err != nil {
			t.Fatalf("AppendEntries(%d): %v", i, err)
		}
		ref.AppendData(entry(i))
		head, err := ReadHead(stg, testLog)
		if err != nil {
			t.Fatalf("ReadHead: %v", err)
		}
		if !bytes.Equal(head.Root, ref.Hash()) {
			t.Fatalf("root at size %d = %x, want %x", i+1, head.Root, ref.Hash())
		}
	}
	verifyAllProofs(t, stg, proofSweepSize, ref)
}

// TestProofs_VerifierRejectsTampering guards the sweep above against
// passing vacuously: the verifier it relies on refuses a proof with one
// hash altered, a proof for the wrong leaf and a proof against the wrong
// root.
func TestProofs_VerifierRejectsTampering(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 13)
	ref := referenceTree(13)
	h := rfc6962.DefaultHasher

	incl, err := Inclusion(stg, testLog, 5, 13)
	if err != nil {
		t.Fatalf("Inclusion: %v", err)
	}
	tampered := make([][]byte, len(incl))
	for i := range incl {
		tampered[i] = bytes.Clone(incl[i])
	}
	tampered[1][0] ^= 1
	if proof.VerifyInclusion(h, 5, 13, ref.LeafHash(5), tampered, ref.Hash()) == nil {
		t.Fatal("verifier accepted a tampered inclusion proof")
	}
	if proof.VerifyInclusion(h, 5, 13, ref.LeafHash(6), incl, ref.Hash()) == nil {
		t.Fatal("verifier accepted an inclusion proof for the wrong leaf")
	}
	cons, err := Consistency(stg, testLog, 6, 13)
	if err != nil {
		t.Fatalf("Consistency: %v", err)
	}
	if proof.VerifyConsistency(h, 6, 13, cons, ref.HashAt(7), ref.Hash()) == nil {
		t.Fatal("verifier accepted a consistency proof against the wrong old root")
	}
}

func TestProofs_RangeErrors(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 8)
	for name, call := range map[string]func() (any, error){
		"inclusion size past the log": func() (any, error) { return Inclusion(stg, testLog, 0, 9) },
		"inclusion index at the size": func() (any, error) { return Inclusion(stg, testLog, 5, 5) },
		"consistency size2 past":      func() (any, error) { return Consistency(stg, testLog, 1, 9) },
		"consistency size1 > size2":   func() (any, error) { return Consistency(stg, testLog, 6, 5) },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := call()
			var sor *SizeOutOfRangeError
			var ior *IndexOutOfRangeError
			if !errors.As(err, &sor) && !errors.As(err, &ior) {
				t.Fatalf("got %v, want a range error", err)
			}
		})
	}
	var ior *IndexOutOfRangeError
	if _, err := Inclusion(stg, testLog, 0, 0); !errors.As(err, &ior) {
		t.Fatalf("Inclusion in the empty tree = %v, want *IndexOutOfRangeError", err)
	}
	if p, err := Consistency(stg, testLog, 0, 8); err != nil || len(p) != 0 {
		t.Fatalf("Consistency from the empty tree = %v, %v, want an empty proof", p, err)
	}
}

func TestReads_MissingLog(t *testing.T) {
	stg := memory.NewStorage()
	calls := map[string]func() error{
		"ReadHead":       func() error { _, err := ReadHead(stg, testLog); return err },
		"ReadCheckpoint": func() error { _, err := ReadCheckpoint(stg, testLog, 0); return err },
		"Get":            func() error { _, err := Get(stg, testLog, 0, 1); return err },
		"Inclusion":      func() error { _, err := Inclusion(stg, testLog, 0, 1); return err },
		"Consistency":    func() error { _, err := Consistency(stg, testLog, 0, 1); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			var lnf *LogNotFoundError
			if err := call(); !errors.As(err, &lnf) || lnf.ID != testLog {
				t.Fatalf("%s() = %v, want *LogNotFoundError", name, err)
			}
		})
	}
}

// TestProofs_CorruptOrUnreadableNodes covers a proof whose node is missing
// (a corrupt table, never a short proof) and one whose node read fails.
func TestProofs_CorruptOrUnreadableNodes(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 8)
	if err := stg.Delete(nodeKey(testLog, 0, 4)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var cte *CorruptTableError
	if _, err := Inclusion(stg, testLog, 5, 8); !errors.As(err, &cte) {
		t.Fatalf("Inclusion over a missing node = %v, want *CorruptTableError", err)
	}
	if err := stg.Set(nodeKey(testLog, 0, 4), []byte{1, 2, 3}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := Inclusion(stg, testLog, 5, 8); !errors.As(err, &cte) {
		t.Fatalf("Inclusion over a short node = %v, want *CorruptTableError", err)
	}
	var tre *TableReadError
	nodes := append(logPrefix(testLog), 'n', '/')
	if _, err := Consistency(getFailReader{Reader: stg, prefix: nodes}, testLog, 3, 8); !errors.As(err, &tre) {
		t.Fatalf("Consistency over an unreadable node = %v, want *TableReadError", err)
	}
	if _, err := Inclusion(getFailReader{Reader: stg, prefix: logHeadKey(testLog)}, testLog, 0, 1); !errors.As(err, &tre) {
		t.Fatalf("Inclusion over an unreadable head = %v, want *TableReadError", err)
	}
}

func TestReadHead_LatestCheckpoint(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 300)
	ref := referenceTree(300)
	for _, size := range []uint64{1, 256, 255, 2} {
		if err := c.Checkpoint(stg, stg, testLog, size, ref.HashAt(size), []byte(fmt.Sprint("cp", size))); err != nil {
			t.Fatalf("Checkpoint(%d): %v", size, err)
		}
	}
	head, err := ReadHead(stg, testLog)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if head.CheckpointSize != 256 || string(head.Checkpoint) != "cp256" {
		t.Fatalf("latest checkpoint = %d %q, want the largest size, 256", head.CheckpointSize, head.Checkpoint)
	}
	if head.Size != 300 || head.BodiesFrom != 0 || !bytes.Equal(head.Root, ref.Hash()) {
		t.Fatalf("head = %+v", head)
	}
}

func TestReadHead_Faults(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 3)
	var cte *CorruptTableError
	bad := append(checkpointPrefix(testLog), 1, 2)
	if err := stg.Set(bad, []byte("x")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := ReadHead(stg, testLog); !errors.As(err, &cte) {
		t.Fatalf("ReadHead over a malformed checkpoint key = %v, want *CorruptTableError", err)
	}
	if err := stg.Delete(bad); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := stg.Set(logHeadKey(testLog), []byte{9}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := ReadHead(stg, testLog); !errors.As(err, &cte) {
		t.Fatalf("ReadHead over a malformed head = %v, want *CorruptTableError", err)
	}

	_, good := mustCreate(t, false)
	var tre *TableReadError
	if _, err := ReadHead(logIterFailReader{Reader: good}, testLog); !errors.As(err, &tre) {
		t.Fatalf("ReadHead over a failing iterator = %v, want *TableReadError", err)
	}
}

func TestReadCheckpoint(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 4)
	ref := referenceTree(4)
	if err := c.Checkpoint(stg, stg, testLog, 2, ref.HashAt(2), []byte("cp2")); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if got, err := ReadCheckpoint(stg, testLog, 2); err != nil || string(got) != "cp2" {
		t.Fatalf("ReadCheckpoint(2) = %q, %v", got, err)
	}
	if got, err := ReadCheckpoint(stg, testLog, 3); err != nil || got != nil {
		t.Fatalf("ReadCheckpoint(3) = %q, %v, want nil", got, err)
	}
	var tre *TableReadError
	if _, err := ReadCheckpoint(getFailReader{Reader: stg, prefix: checkpointPrefix(testLog)}, testLog, 2); !errors.As(err, &tre) {
		t.Fatalf("ReadCheckpoint over a failing read = %v, want *TableReadError", err)
	}
}

func TestGet_PagesAndClamps(t *testing.T) {
	c, stg := mustCreate(t, false)
	const n = MaxEntriesPerGet + 5
	if _, err := c.AppendEntries(stg, testLog, entries(0, n)); err != nil {
		t.Fatalf("AppendEntries: %v", err)
	}
	for _, tc := range []struct {
		from     uint64
		limit    int
		wantLen  int
		wantLast uint64
	}{
		{from: 0, limit: 3, wantLen: 3, wantLast: 2},
		{from: n - 2, limit: 10, wantLen: 2, wantLast: n - 1},
		{from: 0, limit: 0, wantLen: MaxEntriesPerGet, wantLast: MaxEntriesPerGet - 1},
		{from: 1, limit: MaxEntriesPerGet + 1, wantLen: MaxEntriesPerGet, wantLast: MaxEntriesPerGet},
		{from: 2, limit: -1, wantLen: MaxEntriesPerGet, wantLast: MaxEntriesPerGet + 1},
	} {
		got, err := Get(stg, testLog, tc.from, tc.limit)
		if err != nil {
			t.Fatalf("Get(%d, %d): %v", tc.from, tc.limit, err)
		}
		if len(got) != tc.wantLen || got[len(got)-1].Index != tc.wantLast {
			t.Fatalf("Get(%d, %d) = %d entries ending at %d, want %d ending at %d", tc.from, tc.limit, len(got), got[len(got)-1].Index, tc.wantLen, tc.wantLast)
		}
		for i, e := range got {
			if e.Index != tc.from+uint64(i) || !bytes.Equal(e.Body, entry(int(e.Index))) || e.Expired {
				t.Fatalf("Get(%d, %d)[%d] = %+v", tc.from, tc.limit, i, e)
			}
		}
	}
	if got, err := Get(stg, testLog, n, 5); err != nil || len(got) != 0 {
		t.Fatalf("Get at the size = %v, %v, want no entries", got, err)
	}
	var ior *IndexOutOfRangeError
	if _, err := Get(stg, testLog, n+1, 5); !errors.As(err, &ior) {
		t.Fatalf("Get past the size = %v, want *IndexOutOfRangeError", err)
	}
}

func TestGet_CorruptAndUnreadable(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 6)
	var cte *CorruptTableError

	stray := append(entryKey(testLog, 2), 'x')
	if err := stg.Set(stray, []byte("x")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := Get(stg, testLog, 0, 6); !errors.As(err, &cte) {
		t.Fatalf("Get over a stray entry key = %v, want *CorruptTableError", err)
	}
	if err := stg.Delete(stray); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if err := stg.Delete(entryKey(testLog, 5)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Get(stg, testLog, 0, 6); !errors.As(err, &cte) {
		t.Fatalf("Get over a missing entry = %v, want *CorruptTableError", err)
	}

	var tre *TableReadError
	if _, err := Get(entryIterFailReader{Reader: stg}, testLog, 0, 3); !errors.As(err, &tre) {
		t.Fatalf("Get over a failing iterator = %v, want *TableReadError", err)
	}
}

func TestList_NamesTheDatabasesLogsOnly(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 3)
	others := []LogID{
		{TenantID: testLog.TenantID, Database: testLog.Database, Log: "a-first"},
		{TenantID: testLog.TenantID, Database: testLog.Database + "x", Log: "other-db"},
		{TenantID: testLog.TenantID + "x", Database: testLog.Database, Log: "other-tenant"},
	}
	for _, id := range others {
		if err := c.Create(stg, id, testDescriptor(false)); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	got, err := Read(stg, ListRequest{TenantID: testLog.TenantID, Database: testLog.Database})
	if err != nil {
		t.Fatalf("Read(ListRequest): %v", err)
	}
	want := []LogID{others[0], testLog}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	empty, err := List(stg, testLog.TenantID, "none")
	if err != nil || len(empty) != 0 {
		t.Fatalf("List of an empty database = %v, %v", empty, err)
	}
}

func TestList_Refusals(t *testing.T) {
	var ine *InvalidLogNameError
	for _, scope := range [][2]string{{"a/b", "db"}, {"acme", ""}, {"acme", "d/b"}} {
		if _, err := List(memory.NewStorage(), scope[0], scope[1]); !errors.As(err, &ine) {
			t.Fatalf("List(%q, %q) = %v, want *InvalidLogNameError", scope[0], scope[1], err)
		}
	}
	var tre *TableReadError
	if _, err := List(errReader{errStorageFault}, "acme", "db"); !errors.As(err, &tre) {
		t.Fatalf("List over a failing reader = %v, want *TableReadError", err)
	}
	stg := memoryStorageWithRawKey(t, []byte(LogTablePrefix+"acme/db/no-terminator"), []byte("x"))
	var cte *CorruptTableError
	if _, err := List(stg, "acme", "db"); !errors.As(err, &cte) {
		t.Fatalf("List over a stray key = %v, want *CorruptTableError", err)
	}
}

// entryIterFailReader fails every iterator and serves point reads.
type entryIterFailReader struct {
	storage.Reader
}

func (entryIterFailReader) NewIterator(*storage.IteratorOptions) storage.Iterator {
	return errIterator{err: errStorageFault}
}
