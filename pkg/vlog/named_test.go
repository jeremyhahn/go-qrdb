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
	"strconv"
	"testing"

	"github.com/transparency-dev/merkle/rfc6962"
	"github.com/transparency-dev/merkle/testonly"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// testLog is the log most tests below use.
var testLog = LogID{TenantID: "acme", Database: "ledger", Log: "certs"}

// testDescriptor returns a valid descriptor, allowing ExpireBodies when
// expire is true.
func testDescriptor(expire bool) Descriptor {
	return Descriptor{Origin: "db.example.com/acme/certs", Hash: HashSHA256, VerifierKeys: []string{"key-1"}, AllowExpireBodies: expire}
}

// entry returns the i-th test entry's bytes.
func entry(i int) []byte {
	return []byte("entry-" + strconv.Itoa(i))
}

// entries returns test entries [from, from+n).
func entries(from, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = entry(from + i)
	}
	return out
}

// batchOp is one write a batch has staged.
type batchOp struct {
	key, value []byte
	del        bool
}

// batch is an apply batch the way a state machine stages one: its writes
// reach the storage it commits into only at commit, so a reader of that
// storage does not see them while the Update call that staged them runs.
// It is the case Checkpoint's read-through of staged nodes exists for.
type batch struct {
	ops []batchOp
}

func (b *batch) Set(key, value []byte) error {
	b.ops = append(b.ops, batchOp{key: bytes.Clone(key), value: bytes.Clone(value)})
	return nil
}

func (b *batch) Delete(key []byte) error {
	b.ops = append(b.ops, batchOp{key: bytes.Clone(key), del: true})
	return nil
}

// commit applies b's writes to stg in the order they were staged.
func (b *batch) commit(t *testing.T, stg storage.Storage) {
	t.Helper()
	for _, op := range b.ops {
		var err error
		if op.del {
			err = stg.Delete(op.key)
		} else {
			err = stg.Set(op.key, op.value)
		}
		if err != nil {
			t.Fatalf("commit %q: %v", op.key, err)
		}
	}
	b.ops = nil
}

// dump returns every record in stg, in key order, as one string.
func dump(t *testing.T, stg storage.Reader) string {
	t.Helper()
	var buf bytes.Buffer
	it := stg.NewIterator(&storage.IteratorOptions{LowerBound: []byte{0x00}, UpperBound: []byte{0xff}})
	defer func() { _ = it.Close() }()
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		fmt.Fprintf(&buf, "%q=%x\n", it.Key(), it.Value())
	}
	if err := it.Error(); err != nil {
		t.Fatalf("dump: %v", err)
	}
	return buf.String()
}

// mustCreate creates testLog in a fresh Chain over a fresh storage.
func mustCreate(t *testing.T, expire bool) (*Chain, storage.Storage) {
	t.Helper()
	stg := memory.NewStorage()
	c := NewChain()
	if err := c.Create(stg, testLog, testDescriptor(expire)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return c, stg
}

// mustAppend appends n test entries to testLog, one Append per entry.
func mustAppend(t *testing.T, c *Chain, w Writer, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		if _, err := c.AppendEntries(w, testLog, [][]byte{entry(i)}); err != nil {
			t.Fatalf("AppendEntries(%d): %v", i, err)
		}
	}
}

// referenceTree returns the merkle library's in-memory reference tree over
// test entries [0, n).
func referenceTree(n int) *testonly.Tree {
	ref := testonly.New(rfc6962.DefaultHasher)
	ref.AppendData(entries(0, n)...)
	return ref
}

func TestLogID_Validate(t *testing.T) {
	for name, tc := range map[string]struct {
		id    LogID
		field string
	}{
		"valid":                  {id: testLog},
		"empty tenant is valid":  {id: LogID{Database: "db", Log: "l"}},
		"slash in tenant":        {id: LogID{TenantID: "acme/evil", Database: "db", Log: "l"}, field: "tenant"},
		"empty database":         {id: LogID{TenantID: "acme", Log: "l"}, field: "database"},
		"slash in database":      {id: LogID{TenantID: "acme", Database: "a/b", Log: "l"}, field: "database"},
		"empty log":              {id: LogID{TenantID: "acme", Database: "db"}, field: "log"},
		"slash in log":           {id: LogID{TenantID: "acme", Database: "db", Log: "x/"}, field: "log"},
		"slash alone as the log": {id: LogID{TenantID: "acme", Database: "db", Log: "/"}, field: "log"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.id.Validate()
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var ine *InvalidLogNameError
			if !errors.As(err, &ine) || ine.Field != tc.field {
				t.Fatalf("Validate() = %v, want *InvalidLogNameError for %s", err, tc.field)
			}
		})
	}
}

// TestNamedLog_InvalidNameRefusedEverywhere covers every entry point
// refusing an invalid name before it builds a key.
func TestNamedLog_InvalidNameRefusedEverywhere(t *testing.T) {
	bad := LogID{TenantID: "acme", Database: "ledger", Log: "a/b"}
	c, stg := mustCreate(t, true)
	calls := map[string]func() error{
		"Create":         func() error { return c.Create(stg, bad, testDescriptor(false)) },
		"AppendEntries":  func() error { _, err := c.AppendEntries(stg, bad, entries(0, 1)); return err },
		"Checkpoint":     func() error { return c.Checkpoint(stg, stg, bad, 0, nil, []byte("x")) },
		"ExpireBodies":   func() error { _, err := c.ExpireBodies(stg, bad, 1); return err },
		"ReadHead":       func() error { _, err := ReadHead(stg, bad); return err },
		"ReadCheckpoint": func() error { _, err := ReadCheckpoint(stg, bad, 0); return err },
		"Get":            func() error { _, err := Get(stg, bad, 0, 1); return err },
		"Inclusion":      func() error { _, err := Inclusion(stg, bad, 0, 1); return err },
		"Consistency":    func() error { _, err := Consistency(stg, bad, 0, 1); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			var ine *InvalidLogNameError
			if err := call(); !errors.As(err, &ine) {
				t.Fatalf("%s() = %v, want *InvalidLogNameError", name, err)
			}
		})
	}
}

func TestChain_Create_RefusesInvalidDescriptor(t *testing.T) {
	for name, desc := range map[string]Descriptor{
		"empty origin":         {Hash: HashSHA256},
		"newline in origin":    {Origin: "a\nb", Hash: HashSHA256},
		"invalid UTF-8 origin": {Origin: "a\xffb", Hash: HashSHA256},
		"unsupported hash":     {Origin: "o", Hash: "sha512"},
		"missing hash":         {Origin: "o"},
	} {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			c := NewChain()
			var ide *InvalidDescriptorError
			if err := c.Create(stg, testLog, desc); !errors.As(err, &ide) {
				t.Fatalf("Create() = %v, want *InvalidDescriptorError", err)
			}
			if got := dump(t, stg); got != "" {
				t.Fatalf("a refused Create staged records:\n%s", got)
			}
		})
	}
}

func TestChain_Create_StagesDescriptorAndEmptyHead(t *testing.T) {
	_, stg := mustCreate(t, false)
	head, err := ReadHead(stg, testLog)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if head.Size != 0 || !bytes.Equal(head.Root, rfc6962.DefaultHasher.EmptyRoot()) || head.Checkpoint != nil {
		t.Fatalf("head of a new log = %+v, want size 0, the empty root and no checkpoint", head)
	}
	raw, err := stg.Get(descriptorKey(testLog))
	if err != nil || !bytes.Contains(raw, []byte(`"origin":"db.example.com/acme/certs"`)) {
		t.Fatalf("descriptor record = %q, %v", raw, err)
	}
}

func TestChain_Create_RefusesExistingLog(t *testing.T) {
	c, stg := mustCreate(t, false)
	var lee *LogExistsError
	if err := c.Create(stg, testLog, testDescriptor(true)); !errors.As(err, &lee) || lee.ID != testLog {
		t.Fatalf("second Create() = %v, want *LogExistsError", err)
	}
}

func TestChain_Create_TableWriteFault(t *testing.T) {
	for name, failSet := range map[string][]byte{
		"descriptor": descriptorKey(testLog),
		"head":       logHeadKey(testLog),
	} {
		t.Run(name, func(t *testing.T) {
			c := NewChain()
			w := failingWriter{Writer: memory.NewStorage(), failSet: failSet}
			var twe *TableWriteError
			if err := c.Create(w, testLog, testDescriptor(false)); !errors.As(err, &twe) {
				t.Fatalf("Create() = %v, want *TableWriteError", err)
			}
			if _, err := c.AppendEntries(memory.NewStorage(), testLog, entries(0, 1)); err == nil {
				t.Fatal("a log whose Create failed accepted an append")
			}
		})
	}
}

func TestChain_AppendEntries_RefusesBadBatch(t *testing.T) {
	c, stg := mustCreate(t, false)
	for name, batch := range map[string][][]byte{
		"empty":    nil,
		"too many": make([][]byte, MaxAppendPerCommand+1),
	} {
		t.Run(name, func(t *testing.T) {
			var abe *AppendBatchError
			if _, err := c.AppendEntries(stg, testLog, batch); !errors.As(err, &abe) || abe.Count != len(batch) {
				t.Fatalf("AppendEntries() = %v, want *AppendBatchError", err)
			}
		})
	}
}

func TestChain_AppendEntries_RefusesMissingLog(t *testing.T) {
	c := NewChain()
	var lnf *LogNotFoundError
	if _, err := c.AppendEntries(memory.NewStorage(), testLog, entries(0, 1)); !errors.As(err, &lnf) {
		t.Fatalf("AppendEntries() = %v, want *LogNotFoundError", err)
	}
}

func TestChain_AppendEntries_AssignsIndicesAndMatchesReference(t *testing.T) {
	c, stg := mustCreate(t, false)
	for _, n := range []int{1, 3, 4, 9} {
		head, _ := ReadHead(stg, testLog)
		first, err := c.AppendEntries(stg, testLog, entries(int(head.Size), n))
		if err != nil {
			t.Fatalf("AppendEntries(%d): %v", n, err)
		}
		if first != head.Size {
			t.Fatalf("first index = %d, want %d", first, head.Size)
		}
	}
	head, err := ReadHead(stg, testLog)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	ref := referenceTree(17)
	if head.Size != 17 || !bytes.Equal(head.Root, ref.Hash()) {
		t.Fatalf("head = size %d root %x, want 17 and %x", head.Size, head.Root, ref.Hash())
	}
}

// TestChain_AppendEntries_KnownAnswers checks the tree against the merkle
// library's RFC 6962 known-answer vectors, root by root.
func TestChain_AppendEntries_KnownAnswers(t *testing.T) {
	c, stg := mustCreate(t, false)
	// roots[size] is the root of the tree over the first size leaves.
	roots := testonly.RootHashes()
	if head, err := ReadHead(stg, testLog); err != nil || !bytes.Equal(head.Root, roots[0]) {
		t.Fatalf("empty root = %v, %v, want %x", head, err, roots[0])
	}
	for i, leaf := range testonly.LeafInputs() {
		if _, err := c.AppendEntries(stg, testLog, [][]byte{leaf}); err != nil {
			t.Fatalf("AppendEntries(%d): %v", i, err)
		}
		head, err := ReadHead(stg, testLog)
		if err != nil {
			t.Fatalf("ReadHead: %v", err)
		}
		if !bytes.Equal(head.Root, roots[i+1]) {
			t.Fatalf("root at size %d = %x, want %x", i+1, head.Root, roots[i+1])
		}
	}
}

// TestChain_AppendEntries_TableWriteFault covers every staging write of an
// append: a fault is a *TableWriteError and the in-memory head does not
// move, so the next append re-assigns the same index.
func TestChain_AppendEntries_TableWriteFault(t *testing.T) {
	for name, failSet := range map[string][]byte{
		"entry":     entryPrefix(testLog),
		"leaf node": append(logPrefix(testLog), 'n', '/', 0),
		"head":      logHeadKey(testLog),
	} {
		t.Run(name, func(t *testing.T) {
			c, stg := mustCreate(t, false)
			mustAppend(t, c, stg, 0, 3)
			var twe *TableWriteError
			if _, err := c.AppendEntries(failingWriter{Writer: stg, failSet: failSet}, testLog, entries(3, 1)); !errors.As(err, &twe) || !errors.Is(err, errStorageFault) {
				t.Fatalf("AppendEntries() = %v, want *TableWriteError wrapping the fault", err)
			}
			if first, err := c.AppendEntries(stg, testLog, entries(3, 1)); err != nil || first != 3 {
				t.Fatalf("append after the fault = %d, %v, want index 3", first, err)
			}
		})
	}
}

// TestChain_AppendEntries_InteriorNodeWriteFault covers a fault on a node
// above the leaf, which the visitor reports after the leaf succeeded.
func TestChain_AppendEntries_InteriorNodeWriteFault(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 1)
	w := failingWriter{Writer: stg, failSet: append(logPrefix(testLog), 'n', '/', 1)}
	var twe *TableWriteError
	if _, err := c.AppendEntries(w, testLog, entries(1, 1)); !errors.As(err, &twe) {
		t.Fatalf("AppendEntries() = %v, want *TableWriteError", err)
	}
}

// namedOp is one command of the determinism scenario below, applied to c
// with r as the storage the Update call reads and w as its batch.
type namedOp func(c *Chain, r storage.Reader, w Writer) (string, error)

// namedScenario is a mix of every named-log mutation, including a
// checkpoint at a size whose nodes were staged by earlier commands, and an
// ExpireBodies that deletes entries appended earlier.
func namedScenario() []namedOp {
	ref := referenceTree(40)
	other := LogID{TenantID: "acme", Database: "ledger", Log: "other"}
	appendOp := func(id LogID, from, n int) namedOp {
		return func(c *Chain, _ storage.Reader, w Writer) (string, error) {
			first, err := c.AppendEntries(w, id, entries(from, n))
			return strconv.FormatUint(first, 10), err
		}
	}
	checkpointOp := func(size uint64) namedOp {
		return func(c *Chain, r storage.Reader, w Writer) (string, error) {
			return "", c.Checkpoint(r, w, testLog, size, ref.HashAt(size), []byte("signed@"+strconv.FormatUint(size, 10)))
		}
	}
	return []namedOp{
		func(c *Chain, _ storage.Reader, w Writer) (string, error) {
			return "", c.Create(w, testLog, testDescriptor(true))
		},
		func(c *Chain, _ storage.Reader, w Writer) (string, error) {
			return "", c.Create(w, other, testDescriptor(false))
		},
		appendOp(testLog, 0, 5),
		appendOp(other, 0, 2),
		appendOp(testLog, 5, 1),
		checkpointOp(3),
		appendOp(testLog, 6, 11),
		checkpointOp(17),
		func(c *Chain, _ storage.Reader, w Writer) (string, error) {
			n, err := c.ExpireBodies(w, testLog, 4)
			return strconv.Itoa(n), err
		},
		appendOp(testLog, 17, 23),
		checkpointOp(9),
		appendOp(other, 2, 3),
	}
}

// runScenario applies ops to a fresh Chain, committing a batch every
// perCall commands, and returns the storage and every command's result.
func runScenario(t *testing.T, ops []namedOp, perCall int) (storage.Storage, []string) {
	t.Helper()
	stg := memory.NewStorage()
	c := NewChain()
	if err := c.Restore(stg); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	b := &batch{}
	results := make([]string, 0, len(ops))
	for i, op := range ops {
		if i%perCall == 0 {
			c.BeginUpdate()
		}
		res, err := op(c, stg, b)
		if err != nil {
			t.Fatalf("op %d: %v", i, err)
		}
		results = append(results, res)
		if (i+1)%perCall == 0 || i == len(ops)-1 {
			b.commit(t, stg)
		}
	}
	return stg, results
}

// TestNamedLog_DeterministicAcrossUpdatePacking is the audit chain's
// determinism test over named logs and the tree: the same committed
// commands applied one per Update call, several per call, and all in one
// call -- where no command's reads see an earlier command's writes -- leave
// byte-identical tables and answer identically.
func TestNamedLog_DeterministicAcrossUpdatePacking(t *testing.T) {
	ops := namedScenario()
	wantStg, wantRes := runScenario(t, ops, 1)
	want := dump(t, wantStg)
	for _, perCall := range []int{2, 5, len(ops)} {
		t.Run(strconv.Itoa(perCall)+" per call", func(t *testing.T) {
			stg, res := runScenario(t, ops, perCall)
			if got := dump(t, stg); got != want {
				t.Fatalf("table differs from one-per-call:\n got %s\nwant %s", got, want)
			}
			if fmt.Sprint(res) != fmt.Sprint(wantRes) {
				t.Fatalf("results = %v, want %v", res, wantRes)
			}
		})
	}
}

// TestNamedLog_RestoreOnOpen covers a Chain restored from a table another
// Chain wrote: it continues every log exactly where the writer would have.
func TestNamedLog_RestoreOnOpen(t *testing.T) {
	ops := namedScenario()
	full, _ := runScenario(t, ops, 1)

	split := 7
	stg, _ := runScenario(t, ops[:split], 1)
	reopened := NewChain()
	if err := reopened.Restore(stg); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for i, op := range ops[split:] {
		reopened.BeginUpdate()
		b := &batch{}
		if _, err := op(reopened, stg, b); err != nil {
			t.Fatalf("op %d after restore: %v", split+i, err)
		}
		b.commit(t, stg)
	}
	if got, want := dump(t, stg), dump(t, full); got != want {
		t.Fatalf("restored chain diverged:\n got %s\nwant %s", got, want)
	}
}

// TestNamedLog_RestoreAfterFailedApply covers the resync path: an append
// that fails part-way leaves the batch discarded and the Chain restored from
// the committed table, after which the log continues as if the failed
// command had never been proposed.
func TestNamedLog_RestoreAfterFailedApply(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 5)

	// A batch whose node writes fail part-way through a multi-entry append;
	// the state machine discards it and resyncs.
	c.BeginUpdate()
	failing := failingWriter{Writer: &batch{}, failSet: append(logPrefix(testLog), 'n', '/', 2)}
	if _, err := c.AppendEntries(failing, testLog, entries(5, 3)); err == nil {
		t.Fatal("append through a failing batch succeeded")
	}
	if err := c.Restore(stg); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(c.staged) != 0 {
		t.Fatalf("Restore kept %d staged records of the failed call", len(c.staged))
	}
	mustAppend(t, c, stg, 5, 3)

	want, ref := memory.NewStorage(), NewChain()
	if err := ref.Create(want, testLog, testDescriptor(false)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustAppend(t, ref, want, 0, 8)
	if got, w := dump(t, stg), dump(t, want); got != w {
		t.Fatalf("table after resync differs from a clean run:\n got %s\nwant %s", got, w)
	}
}

func TestNamedLog_Restore_Faults(t *testing.T) {
	_, good := mustCreate(t, false)
	descRaw, _ := good.Get(descriptorKey(testLog))
	headRaw, _ := good.Get(logHeadKey(testLog))
	for name, records := range map[string]map[string][]byte{
		"key outside any log": {LogTablePrefix + "acme/ledger": {1}},
		"empty database name": {LogTablePrefix + "acme//certs/d": {1}},
		"missing descriptor":  {string(logHeadKey(testLog)): headRaw},
		"malformed descriptor": {
			string(descriptorKey(testLog)): []byte("{"),
			string(logHeadKey(testLog)):    headRaw,
		},
		"missing head":   {string(descriptorKey(testLog)): descRaw},
		"malformed head": {string(descriptorKey(testLog)): descRaw, string(logHeadKey(testLog)): {1, 2}},
	} {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			for k, v := range records {
				if err := stg.Set([]byte(k), v); err != nil {
					t.Fatalf("Set: %v", err)
				}
			}
			var cte *CorruptTableError
			if err := NewChain().Restore(stg); !errors.As(err, &cte) {
				t.Fatalf("Restore() = %v, want *CorruptTableError", err)
			}
		})
	}
}

// getFailReader fails Get for keys with a prefix and iterates normally.
type getFailReader struct {
	storage.Reader
	prefix []byte
}

func (r getFailReader) Get(k []byte) ([]byte, error) {
	if bytes.HasPrefix(k, r.prefix) {
		return nil, errStorageFault
	}
	return r.Reader.Get(k)
}

func TestNamedLog_Restore_ReadFaults(t *testing.T) {
	_, stg := mustCreate(t, false)
	for name, r := range map[string]storage.Reader{
		"descriptor get": getFailReader{Reader: stg, prefix: descriptorKey(testLog)},
		"head get":       getFailReader{Reader: stg, prefix: logHeadKey(testLog)},
		"iterator":       logIterFailReader{Reader: stg},
	} {
		t.Run(name, func(t *testing.T) {
			var tre *TableReadError
			if err := NewChain().Restore(r); !errors.As(err, &tre) || !errors.Is(err, errStorageFault) {
				t.Fatalf("Restore() = %v, want *TableReadError wrapping the fault", err)
			}
		})
	}
}

// logIterFailReader serves the audit table's head scan and fails every
// iterator over the named logs' table.
type logIterFailReader struct {
	storage.Reader
}

func (r logIterFailReader) NewIterator(opts *storage.IteratorOptions) storage.Iterator {
	if bytes.HasPrefix(opts.LowerBound, []byte(LogTablePrefix)) {
		return errIterator{err: errStorageFault}
	}
	return r.Reader.NewIterator(opts)
}

func TestChain_Checkpoint_AcceptsTheRootAtEverySize(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 33)
	ref := referenceTree(33)
	for size := uint64(0); size <= 33; size++ {
		if err := c.Checkpoint(stg, stg, testLog, size, ref.HashAt(size), []byte("cp")); err != nil {
			t.Fatalf("Checkpoint(%d): %v", size, err)
		}
	}
	head, err := ReadHead(stg, testLog)
	if err != nil || head.CheckpointSize != 33 || string(head.Checkpoint) != "cp" {
		t.Fatalf("ReadHead = %+v, %v, want the checkpoint at 33", head, err)
	}
}

// TestChain_Checkpoint_ReadsThroughStagedNodes covers a checkpoint at an
// earlier size in the same Update call as the appends that completed its
// nodes, with a reader that sees none of them.
func TestChain_Checkpoint_ReadsThroughStagedNodes(t *testing.T) {
	committed := memory.NewStorage()
	c := NewChain()
	c.BeginUpdate()
	b := &batch{}
	if err := c.Create(b, testLog, testDescriptor(false)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	mustAppend(t, c, b, 0, 21)
	ref := referenceTree(21)
	if err := c.Checkpoint(committed, b, testLog, 13, ref.HashAt(13), []byte("cp13")); err != nil {
		t.Fatalf("Checkpoint(13) through staged nodes: %v", err)
	}
	var cee *CheckpointExistsError
	if err := c.Checkpoint(committed, b, testLog, 13, ref.HashAt(13), []byte("again")); !errors.As(err, &cee) {
		t.Fatalf("second Checkpoint(13) in the same call = %v, want *CheckpointExistsError", err)
	}
	b.commit(t, committed)

	// After the next BeginUpdate the staged nodes are gone and the reader
	// is the only source; the answer is the same.
	c.BeginUpdate()
	if err := c.Checkpoint(committed, committed, testLog, 7, ref.HashAt(7), []byte("cp7")); err != nil {
		t.Fatalf("Checkpoint(7) from committed nodes: %v", err)
	}
	// With the staged nodes gone and a reader that has none, the nodes are
	// missing, which is a corrupt table, never a silent acceptance.
	c.BeginUpdate()
	var cte *CorruptTableError
	if err := c.Checkpoint(memory.NewStorage(), committed, testLog, 5, ref.HashAt(5), []byte("cp5")); !errors.As(err, &cte) {
		t.Fatalf("Checkpoint with no nodes anywhere = %v, want *CorruptTableError", err)
	}
}

func TestChain_Checkpoint_Refusals(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 10)
	ref := referenceTree(10)
	if err := c.Checkpoint(stg, stg, testLog, 4, ref.HashAt(4), []byte("cp")); err != nil {
		t.Fatalf("Checkpoint(4): %v", err)
	}
	missing := LogID{TenantID: "acme", Database: "ledger", Log: "missing"}
	for name, tc := range map[string]struct {
		id     LogID
		size   uint64
		root   []byte
		signed []byte
		target any
	}{
		"size past the head":     {id: testLog, size: 11, root: ref.Hash(), signed: []byte("x"), target: new(*SizeOutOfRangeError)},
		"wrong root":             {id: testLog, size: 6, root: ref.HashAt(5), signed: []byte("x"), target: new(*CheckpointRootMismatchError)},
		"wrong root at the head": {id: testLog, size: 10, root: ref.HashAt(9), signed: []byte("x"), target: new(*CheckpointRootMismatchError)},
		"size already has one":   {id: testLog, size: 4, root: ref.HashAt(4), signed: []byte("x"), target: new(*CheckpointExistsError)},
		"empty signed bytes":     {id: testLog, size: 5, root: ref.HashAt(5), target: new(*EmptyCheckpointError)},
		"missing log":            {id: missing, size: 0, root: nil, signed: []byte("x"), target: new(*LogNotFoundError)},
	} {
		t.Run(name, func(t *testing.T) {
			before := dump(t, stg)
			err := c.Checkpoint(stg, stg, tc.id, tc.size, tc.root, tc.signed)
			if err == nil || !errors.As(err, tc.target) {
				t.Fatalf("Checkpoint() = %v, want %T", err, tc.target)
			}
			if after := dump(t, stg); after != before {
				t.Fatal("a refused checkpoint staged a record")
			}
		})
	}
}

func TestChain_Checkpoint_Faults(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 6)
	ref := referenceTree(6)
	c.BeginUpdate()
	var tre *TableReadError
	if err := c.Checkpoint(getFailReader{Reader: stg, prefix: checkpointPrefix(testLog)}, stg, testLog, 3, ref.HashAt(3), []byte("x")); !errors.As(err, &tre) {
		t.Fatalf("Checkpoint() with a failing existence read = %v, want *TableReadError", err)
	}
	nodes := append(logPrefix(testLog), 'n', '/')
	if err := c.Checkpoint(getFailReader{Reader: stg, prefix: nodes}, stg, testLog, 3, ref.HashAt(3), []byte("x")); !errors.As(err, &tre) {
		t.Fatalf("Checkpoint() with a failing node read = %v, want *TableReadError", err)
	}
	var twe *TableWriteError
	if err := c.Checkpoint(stg, failingWriter{Writer: stg, failSet: checkpointPrefix(testLog)}, testLog, 3, ref.HashAt(3), []byte("x")); !errors.As(err, &twe) {
		t.Fatalf("Checkpoint() with a failing write = %v, want *TableWriteError", err)
	}
	if err := c.Checkpoint(stg, stg, testLog, 3, ref.HashAt(3), []byte("x")); err != nil {
		t.Fatalf("Checkpoint() after the faults: %v", err)
	}
}

// TestChain_ExpireBodies_KeepsProofsValid covers retention: bodies below the
// cutoff are gone and read as expired, every leaf and node hash stays, and
// every inclusion and consistency proof still verifies.
func TestChain_ExpireBodies_KeepsProofsValid(t *testing.T) {
	const n = 70
	c, stg := mustCreate(t, true)
	mustAppend(t, c, stg, 0, n)
	before, err := ReadHead(stg, testLog)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	expired, err := c.ExpireBodies(stg, testLog, 50)
	if err != nil || expired != 50 {
		t.Fatalf("ExpireBodies = %d, %v, want 50", expired, err)
	}
	after, err := ReadHead(stg, testLog)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if after.Size != n || !bytes.Equal(after.Root, before.Root) || after.BodiesFrom != 50 {
		t.Fatalf("head after expiry = %+v, want size %d, the same root and BodiesFrom 50", after, n)
	}
	got, err := Get(stg, testLog, 48, 4)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := []Entry{{Index: 48, Expired: true}, {Index: 49, Expired: true}, {Index: 50, Body: entry(50)}, {Index: 51, Body: entry(51)}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Get across the cutoff = %v, want %v", got, want)
	}
	verifyAllProofs(t, stg, n, referenceTree(n))
}

func TestChain_ExpireBodies_BoundsAndNoOps(t *testing.T) {
	c, stg := mustCreate(t, true)
	mustAppend(t, c, stg, 0, 5)
	for _, tc := range []struct {
		cutoff uint64
		want   int
	}{
		{cutoff: 0, want: 0},
		{cutoff: 2, want: 2},
		{cutoff: 2, want: 0},
		{cutoff: 1, want: 0},
		{cutoff: 99, want: 3}, // clamped to the size: every body may expire.
		{cutoff: 99, want: 0},
	} {
		got, err := c.ExpireBodies(stg, testLog, tc.cutoff)
		if err != nil || got != tc.want {
			t.Fatalf("ExpireBodies(%d) = %d, %v, want %d", tc.cutoff, got, err, tc.want)
		}
	}
	if entries, err := Get(stg, testLog, 0, 0); err != nil || len(entries) != 5 || !entries[4].Expired {
		t.Fatalf("Get after expiring all = %v, %v", entries, err)
	}
}

func TestChain_ExpireBodies_CapsPerCommand(t *testing.T) {
	c, stg := mustCreate(t, true)
	const n = MaxPrunePerCommand + 7
	for from := 0; from < n; from += MaxAppendPerCommand {
		if _, err := c.AppendEntries(stg, testLog, entries(from, min(MaxAppendPerCommand, n-from))); err != nil {
			t.Fatalf("AppendEntries: %v", err)
		}
	}
	if got, err := c.ExpireBodies(stg, testLog, n); err != nil || got != MaxPrunePerCommand {
		t.Fatalf("first ExpireBodies = %d, %v, want the cap %d", got, err, MaxPrunePerCommand)
	}
	if got, err := c.ExpireBodies(stg, testLog, n); err != nil || got != 7 {
		t.Fatalf("second ExpireBodies = %d, %v, want 7", got, err)
	}
}

func TestChain_ExpireBodies_Refusals(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 3)
	var ena *ExpireNotAllowedError
	if _, err := c.ExpireBodies(stg, testLog, 2); !errors.As(err, &ena) {
		t.Fatalf("ExpireBodies on a log that does not allow it = %v, want *ExpireNotAllowedError", err)
	}
	var lnf *LogNotFoundError
	if _, err := c.ExpireBodies(stg, LogID{TenantID: "acme", Database: "ledger", Log: "none"}, 2); !errors.As(err, &lnf) {
		t.Fatalf("ExpireBodies on a missing log = %v, want *LogNotFoundError", err)
	}
}

func TestChain_ExpireBodies_TableWriteFault(t *testing.T) {
	for name, fault := range map[string]failingWriter{
		"entry delete": {failDelete: entryPrefix(testLog)},
		"head write":   {failSet: logHeadKey(testLog)},
	} {
		t.Run(name, func(t *testing.T) {
			c, stg := mustCreate(t, true)
			mustAppend(t, c, stg, 0, 4)
			fault.Writer = stg
			var twe *TableWriteError
			if _, err := c.ExpireBodies(fault, testLog, 3); !errors.As(err, &twe) {
				t.Fatalf("ExpireBodies() = %v, want *TableWriteError", err)
			}
			if got, err := c.ExpireBodies(stg, testLog, 3); err != nil || got != 3 {
				t.Fatalf("ExpireBodies after the fault = %d, %v, want 3: the watermark must not have moved", got, err)
			}
		})
	}
}

// TestNamedLog_AuditTableUnchanged covers the audit instance beside named
// logs: one Chain carries both, Restore restores both, and a named log
// writes nothing under the audit table's prefix.
func TestNamedLog_AuditTableUnchanged(t *testing.T) {
	c, stg := mustCreate(t, false)
	mustAppend(t, c, stg, 0, 3)
	if _, err := c.Append(stg, "acme", fixedRecord(0)); err != nil {
		t.Fatalf("audit Append: %v", err)
	}
	reopened := NewChain()
	if err := reopened.Restore(stg); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if seq, _, _ := reopened.Head("acme"); seq != 1 {
		t.Fatalf("audit head after restore = %d, want 1", seq)
	}
	if first, err := reopened.AppendEntries(stg, testLog, entries(3, 1)); err != nil || first != 3 {
		t.Fatalf("named append after restore = %d, %v, want 3", first, err)
	}
	it := stg.NewIterator(&storage.IteratorOptions{LowerBound: []byte(TablePrefix), UpperBound: prefixEnd([]byte(TablePrefix))})
	defer func() { _ = it.Close() }()
	count := 0
	for ok := it.SeekToFirst(); ok && it.Valid(); ok = it.Next() {
		count++
	}
	if count != 2 {
		t.Fatalf("audit table holds %d records, want its head and one event", count)
	}
}
