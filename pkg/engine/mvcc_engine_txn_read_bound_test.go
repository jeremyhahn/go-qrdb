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

package engine

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// txnBoundValueSize is the value size the bound tests read: with a one-byte
// key, a branch of one put and sixteen gets of it charges exactly
// service.MaxReplyBytes of reply.
const txnBoundValueSize = (service.MaxReplyBytes-service.TxnReplyHeaderBytes-17*service.TxnReplyOpBytes)/16 -
	service.TxnReplyKVBytes - 1

// txnPutOp is a put op of a transaction branch.
func txnPutOp(key string, value []byte) []byte {
	return mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte(key), value, nil, 0))
}

// txnGets is n gets of key.
func txnGets(key string, n int) [][]byte {
	ops := make([][]byte, n)
	for i := range ops {
		ops[i] = txnGetOp(key)
	}
	return ops
}

// updateTxn applies one transaction entry and returns its result, failing
// the test when Apply halts.
func updateTxn(t *testing.T, eng *MVCCEngine, index uint64, successOps [][]byte) sm.Result {
	t.Helper()
	cmd := mustEncode(protocol.EncodeTransaction(nil, successOps, nil))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: index, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update halted on a transaction it must refuse or apply: %v", err)
	}
	return results[0]
}

// assertTxnReadRefused fails the test unless r is the refusal of a
// transaction whose reply crossed service.MaxReplyBytes, and returns the
// reply bytes charged when it was crossed.
func assertTxnReadRefused(t *testing.T, r sm.Result) int64 {
	t.Helper()
	limit, read, refused := protocol.DecodeTxnReadRefusal(r.Data)
	if !refused {
		t.Fatalf("result of %d bytes (%q...) is not a transaction-reply refusal", len(r.Data), r.Data[:min(len(r.Data), 16)])
	}
	if limit != service.MaxReplyBytes || read <= limit {
		t.Fatalf("refusal limit=%d read=%d, want limit %d crossed", limit, read, service.MaxReplyBytes)
	}
	return read
}

// assertNotWritten fails the test when key holds a value.
func assertNotWritten(t *testing.T, eng *MVCCEngine, key string) {
	t.Helper()
	if got, err := eng.Store().Get([]byte(key)); err == nil {
		t.Fatalf("%s = %v, want the refused transaction's write not applied", key, got)
	}
}

func TestMVCCEngine_TransactionRead_CrossingTheBoundRefusesTheWholeBranch(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), make([]byte, txnBoundValueSize), 0)})
	before := mvccRevision(t, eng)

	ops := append([][]byte{txnPutOp("other", []byte("o"))}, txnGets("k", 17)...)
	assertTxnReadRefused(t, updateTxn(t, eng, 2, ops))
	assertNotWritten(t, eng, "other")
	if after := mvccRevision(t, eng); after != before {
		t.Fatalf("revision moved from %d to %d on a refused transaction", before, after)
	}

	// The refusal is a result, not a halt: the group keeps applying.
	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(3, []byte("next"), []byte("n"), 0)})
	if _, err := eng.Store().Get([]byte("next")); err != nil {
		t.Fatalf("the entry after the refusal was not applied: %v", err)
	}
}

func TestMVCCEngine_TransactionRead_AtTheBoundSucceeds(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), make([]byte, txnBoundValueSize), 0)})

	ops := append([][]byte{txnPutOp("other", []byte("o"))}, txnGets("k", 16)...)
	_, succeeded, reads := applyTxn(t, eng, 2, nil, ops, nil)
	if !succeeded {
		t.Fatal("transaction without compares failed")
	}
	for i, r := range reads[1:] {
		if len(r.Kvs) != 1 || len(r.Kvs[0].Value) != txnBoundValueSize {
			t.Fatalf("read %d = %+v, want the whole value", i, r)
		}
	}
	if _, err := eng.Store().Get([]byte("other")); err != nil {
		t.Fatalf("other: %v, want the branch's write applied", err)
	}
}

func TestMVCCEngine_TransactionRead_JustUnderTheBoundSucceeds(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), make([]byte, txnBoundValueSize-1), 0)})

	ops := append([][]byte{txnPutOp("other", []byte("o"))}, txnGets("k", 16)...)
	_, succeeded, reads := applyTxn(t, eng, 2, nil, ops, nil)
	if !succeeded || len(reads) != 17 {
		t.Fatalf("succeeded=%v with %d reads, want 17 answered ops", succeeded, len(reads))
	}
}

func TestMVCCEngine_TransactionRead_OneByteOverTheBoundIsRefused(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// One put and sixteen gets of a bound-sized value charge exactly the
	// bound; the last get's value is one byte longer.
	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("k"), make([]byte, txnBoundValueSize), 0),
		mvccPutEntry(2, []byte("j"), make([]byte, txnBoundValueSize+1), 0),
	})
	ops := append(txnGets("k", 15), txnGetOp("j"), txnPutOp("other", []byte("o")))
	if read := assertTxnReadRefused(t, updateTxn(t, eng, 3, ops)); read != service.MaxReplyBytes+1 {
		t.Fatalf("refused at %d bytes, want %d", read, service.MaxReplyBytes+1)
	}
	assertNotWritten(t, eng, "other")
}

func TestMVCCEngine_TransactionRead_EveryOpIsChargedItsResponse(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// The reads answer less than the bound, but one more op's response
	// carries the whole reply past it.
	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), make([]byte, txnBoundValueSize), 0)})
	ops := append([][]byte{txnPutOp("other", []byte("o")), txnPutOp("other2", []byte("o"))}, txnGets("k", 16)...)
	if read := assertTxnReadRefused(t, updateTxn(t, eng, 2, ops)); read != service.MaxReplyBytes+service.TxnReplyOpBytes {
		t.Fatalf("refused at %d bytes, want %d", read, service.MaxReplyBytes+service.TxnReplyOpBytes)
	}
	assertNotWritten(t, eng, "other")
}

func TestMVCCEngine_TransactionRead_ABranchPutIsChargedToTheReadsAfterIt(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// Nothing is stored before the branch: only the put makes the reads
	// large, so the bound must count what the branch writes.
	ops := append([][]byte{txnPutOp("k", make([]byte, txnBoundValueSize+1))}, txnGets("k", 16)...)
	assertTxnReadRefused(t, updateTxn(t, eng, 1, ops))
	assertNotWritten(t, eng, "k")
}

func TestMVCCEngine_TransactionRead_ARangeStopsAtTheBound(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	value := make([]byte, txnBoundValueSize)
	var entries []sm.Entry
	for i := range 20 {
		entries = append(entries, mvccPutEntry(uint64(i+1), []byte{'a' + byte(i)}, value, 0))
	}
	applyMVCCEntries(t, eng, entries)

	rangeOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCRange, []byte("a"), nil, []byte("z"), 0))
	read := assertTxnReadRefused(t, updateTxn(t, eng, 21, [][]byte{txnPutOp("other", []byte("o")), rangeOp}))
	if limit := int64(service.MaxReplyBytes + service.TxnReplyKVBytes + 1 + txnBoundValueSize); read > limit {
		t.Fatalf("range charged %d bytes before refusing, want it stopped at the key that crossed %d", read, service.MaxReplyBytes)
	}
	assertNotWritten(t, eng, "other")
}

func TestMVCCEngine_TransactionRead_ARangeWithNoBudgetLeftStopsAtItsFirstKey(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// The gets spend the whole bound (j's value is one op's response
	// shorter, for the range's own response); the range after them must
	// stop at its first key rather than scan all twenty.
	entries := []sm.Entry{
		mvccPutEntry(1, []byte("k"), make([]byte, txnBoundValueSize), 0),
		mvccPutEntry(2, []byte("j"), make([]byte, txnBoundValueSize-service.TxnReplyOpBytes), 0),
	}
	for i := range 20 {
		entries = append(entries, mvccPutEntry(uint64(i+3), []byte{'A' + byte(i)}, []byte("v"), 0))
	}
	applyMVCCEntries(t, eng, entries)

	rangeOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCRange, []byte("A"), nil, []byte("Z"), 0))
	ops := append(txnGets("k", 15), txnGetOp("j"), rangeOp, txnPutOp("other", []byte("o")))
	want := int64(service.MaxReplyBytes + service.TxnReplyKVBytes + 2)
	if read := assertTxnReadRefused(t, updateTxn(t, eng, 23, ops)); read != want {
		t.Fatalf("refused at %d bytes, want %d: the range read past its first key", read, want)
	}
	assertNotWritten(t, eng, "other")
}
