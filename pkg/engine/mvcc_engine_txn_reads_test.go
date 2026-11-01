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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// txnGetOp is a single-key read in a transaction branch.
func txnGetOp(key string) []byte {
	return mustEncode(protocol.EncodeOp(protocol.OpMVCCRange, []byte(key), nil, nil, 0))
}

// txnVersionCompare is a compare that key's version equals version.
func txnVersionCompare(key string, version int64) []byte {
	return mustEncode(protocol.EncodeCompare([]byte(key), byte(service.CompareVersion), byte(service.CompareEqual), nil, version, 0, 0))
}

// applyTxn applies one transaction at index and decodes its result.
func applyTxn(t *testing.T, eng *MVCCEngine, index uint64, compares, successOps, failureOps [][]byte) (sm.Result, bool, []protocol.TxnReadResult) {
	t.Helper()
	cmd := mustEncode(protocol.EncodeTransaction(compares, successOps, failureOps))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: index, Cmd: cmd}}, results); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	succeeded, reads, err := protocol.DecodeTransactionResult(results[0].Data)
	if err != nil {
		t.Fatalf("decode transaction result: %v", err)
	}
	return results[0], succeeded, reads
}

func mvccRevision(t *testing.T, eng *MVCCEngine) int64 {
	t.Helper()
	got, err := eng.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	return got.(int64)
}

func TestMVCCEngine_TransactionRead_GetInSuccessBranch(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("k"), []byte("v2"), 0),
	})

	_, succeeded, reads := applyTxn(t, eng, 3, [][]byte{txnVersionCompare("k", 2)}, [][]byte{txnGetOp("k")}, nil)
	if !succeeded {
		t.Fatal("compare on version 2 failed")
	}
	if len(reads) != 1 || reads[0].Count != 1 || len(reads[0].Kvs) != 1 {
		t.Fatalf("reads = %+v, want one answer of one key", reads)
	}
	got := reads[0].Kvs[0]
	if string(got.Key) != "k" || string(got.Value) != "v2" || got.Version != 2 || got.ModRevision <= got.CreateRevision {
		t.Fatalf("read = %+v, want k=v2 at version 2 with mod after create", got)
	}
}

func TestMVCCEngine_TransactionRead_GetInFailureBranch(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("k"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("k"), []byte("v2"), 0),
	})

	// The compare names a stale version, so the failure branch reads the
	// current value back for the caller to retry against.
	_, succeeded, reads := applyTxn(t, eng, 3, [][]byte{txnVersionCompare("k", 1)},
		[][]byte{mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("k"), []byte("lost"), nil, 0))},
		[][]byte{txnGetOp("k")})
	if succeeded {
		t.Fatal("compare on a stale version succeeded")
	}
	if len(reads) != 1 || len(reads[0].Kvs) != 1 || string(reads[0].Kvs[0].Value) != "v2" || reads[0].Kvs[0].Version != 2 {
		t.Fatalf("reads = %+v, want k=v2 at version 2", reads)
	}
}

func TestMVCCEngine_TransactionRead_MixedBranchReadsItsOwnWrites(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v1"), 0)})

	res, succeeded, reads := applyTxn(t, eng, 2, nil, [][]byte{
		txnGetOp("k"),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("k"), []byte("v2"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("other"), []byte("o"), nil, 0)),
		txnGetOp("k"),
	}, nil)
	if !succeeded {
		t.Fatal("transaction without compares failed")
	}
	if len(reads) != 4 {
		t.Fatalf("got %d answers, want one per op", len(reads))
	}
	if string(reads[0].Kvs[0].Value) != "v1" || reads[0].Kvs[0].Version != 1 {
		t.Fatalf("read before the put = %+v, want v1 at version 1", reads[0].Kvs[0])
	}
	if len(reads[1].Kvs) != 0 || len(reads[2].Kvs) != 0 {
		t.Fatalf("write answers carry key-values: %+v %+v", reads[1], reads[2])
	}
	after := reads[3].Kvs[0]
	if string(after.Value) != "v2" || after.Version != 2 || after.ModRevision != int64(res.Value) {
		t.Fatalf("read after the put = %+v, want v2 at version 2, mod revision %d", after, res.Value)
	}
	got, err := eng.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("other")})
	if err != nil || string(got.(*service.KeyValue).Value) != "o" {
		t.Fatalf("other = %v, %v; want the branch's write applied", got, err)
	}
	if rev := mvccRevision(t, eng); rev != int64(res.Value) {
		t.Fatalf("revision = %d, want the transaction's %d", rev, res.Value)
	}
}

func TestMVCCEngine_TransactionRead_RangeAnswersEveryKeyInIt(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{
		mvccPutEntry(1, []byte("r/a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("r/b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("s/c"), []byte("3"), 0),
	})

	rangeOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCRange, []byte("r/"), nil, []byte("r0"), 0))
	_, _, reads := applyTxn(t, eng, 4, nil, [][]byte{rangeOp}, nil)
	if len(reads) != 1 || reads[0].Count != 2 || len(reads[0].Kvs) != 2 {
		t.Fatalf("reads = %+v, want the two keys under r/", reads)
	}
	if string(reads[0].Kvs[0].Key) != "r/a" || string(reads[0].Kvs[1].Key) != "r/b" {
		t.Fatalf("range keys = %q, %q", reads[0].Kvs[0].Key, reads[0].Kvs[1].Key)
	}
}

func TestMVCCEngine_TransactionRead_AbsentKeyAnswersEmpty(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	_, succeeded, reads := applyTxn(t, eng, 1, nil, [][]byte{txnGetOp("missing")}, nil)
	if !succeeded || len(reads) != 1 || reads[0].Count != 0 || len(reads[0].Kvs) != 0 {
		t.Fatalf("got (%v, %+v), want one empty answer", succeeded, reads)
	}
}

func TestMVCCEngine_TransactionRead_ReadsAloneLeaveTheRevision(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v1"), 0)})
	before := mvccRevision(t, eng)
	applyTxn(t, eng, 2, nil, [][]byte{txnGetOp("k"), txnGetOp("k")}, nil)
	if after := mvccRevision(t, eng); after != before {
		t.Fatalf("revision moved from %d to %d on a read-only branch", before, after)
	}
}

// TestMVCCEngine_TransactionRead_ReadsAloneReportTheObservedRevision verifies
// that a read-only branch at revision R reports R -- the revision it
// observed, as an etcd response header does -- and not R+1, a revision no
// write ever commits at; the next write then commits at R+1.
func TestMVCCEngine_TransactionRead_ReadsAloneReportTheObservedRevision(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v1"), 0)})
	observed := mvccRevision(t, eng)
	res, _, reads := applyTxn(t, eng, 2, nil, [][]byte{txnGetOp("k")}, nil)
	if int64(res.Value) != observed {
		t.Fatalf("read-only transaction reported revision %d, want the observed %d", res.Value, observed)
	}
	if len(reads) != 1 || len(reads[0].Kvs) != 1 || reads[0].Kvs[0].ModRevision != observed {
		t.Fatalf("reads = %+v, want k at mod revision %d", reads, observed)
	}

	write, _, _ := applyTxn(t, eng, 3, nil, [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("k"), []byte("v2"), nil, 0)),
	}, nil)
	if int64(write.Value) != observed+1 {
		t.Fatalf("next write committed at revision %d, want %d", write.Value, observed+1)
	}
}

// TestMVCCEngine_TransactionRead_AFailedReadFailsTheTransaction verifies that
// a storage read fault on a get inside a mixed branch fails the whole
// transaction and commits none of its writes, rather than answering the get
// as if the key were absent.
func TestMVCCEngine_TransactionRead_AFailedReadFailsTheTransaction(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{mvccPutEntry(1, []byte("k"), []byte("v1"), 0)})
	before := mvccRevision(t, eng)
	corrupt := stg.Batch()
	if err := corrupt.Set(mvcc.EncodeDataKey([]byte("k"), before), []byte{0xff}); err != nil {
		t.Fatalf("corrupt the data entry: %v", err)
	}
	if err := corrupt.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	_ = corrupt.Close()

	cmd := mustEncode(protocol.EncodeTransaction(nil, [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("other"), []byte("o"), nil, 0)),
		txnGetOp("k"),
	}, nil))
	err := eng.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: cmd}}, make([]sm.Result, 1))
	var encErr *mvcc.EncodingError
	if !errors.As(err, &encErr) {
		t.Fatalf("Update = %v, want the read fault (an EncodingError) to fail the transaction", err)
	}
	if got, gerr := eng.Store().Get([]byte("other")); gerr == nil {
		t.Fatalf("other = %v, want the failed transaction's write not applied", got)
	}
	if after := mvccRevision(t, eng); after != before {
		t.Fatalf("revision moved from %d to %d on a failed transaction", before, after)
	}
}

func TestMVCCEngine_TransactionRead_WriteOnlyBranchKeepsOneByteResult(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	// The unselected branch reads; the executed one does not, so the result
	// keeps the shape every transaction had before reads.
	res, _, reads := applyTxn(t, eng, 1, nil,
		[][]byte{mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("k"), []byte("v"), nil, 0))},
		[][]byte{txnGetOp("k")})
	if len(res.Data) != 1 || reads != nil {
		t.Fatalf("result data = %v, want the one-byte flag", res.Data)
	}
}

func TestMVCCEngine_TransactionRead_BatchStillRefusesARead(t *testing.T) {
	if err := validateDecodedOps([]protocol.DecodedOp{{Type: protocol.OpMVCCRange, Key: []byte("k")}}); err == nil {
		t.Fatal("a batch accepted a read op it cannot execute")
	}
	if err := validateTxnOps([]protocol.DecodedOp{{Type: protocol.OpMVCCRange, Key: []byte("k")}}); err != nil {
		t.Fatalf("a transaction refused a read: %v", err)
	}
	if err := validateTxnOps([]protocol.DecodedOp{{Type: 0xEE, Key: []byte("k")}}); err == nil {
		t.Fatal("a transaction accepted an unknown op")
	}
}
