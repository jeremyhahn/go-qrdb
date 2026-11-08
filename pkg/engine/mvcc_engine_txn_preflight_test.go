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

// preflightStaged runs ops through the staged puts as txnReadRefusal does,
// stopping once the total passes the bound, and returns the staged puts.
func preflightStaged(ops []protocol.DecodedOp) (*txnStagedPuts, int64) {
	var staged txnStagedPuts
	total := int64(0)
	for _, op := range ops {
		if op.Type != protocol.OpMVCCRange {
			staged.stage(op)
			continue
		}
		if total = staged.charge(op, total); total > service.MaxReplyBytes {
			break
		}
	}
	return &staged, total
}

func decodedPut(key string, value []byte) protocol.DecodedOp {
	return protocol.DecodedOp{Type: protocol.OpMVCCPut, Key: []byte(key), Value: value}
}

func decodedRead(key, end string) protocol.DecodedOp {
	op := protocol.DecodedOp{Type: protocol.OpMVCCRange, Key: []byte(key)}
	if end != "" {
		op.RangeEnd = []byte(end)
	}
	return op
}

func TestTxnStagedPuts_GetsOfAnAbsentKeyVisitNothing(t *testing.T) {
	ops := make([]protocol.DecodedOp, 100_000)
	for i := range ops {
		ops[i] = decodedRead("absent", "")
	}
	if staged, _ := preflightStaged(ops); staged.visited != 0 {
		t.Fatalf("visited %d staged entries, want 0", staged.visited)
	}
}

func TestTxnStagedPuts_GetsOfAnotherKeyDoNotScanThePuts(t *testing.T) {
	ops := make([]protocol.DecodedOp, 0, 100_000)
	for range 50_000 {
		ops = append(ops, decodedPut("a", []byte("v")))
	}
	for range 50_000 {
		ops = append(ops, decodedRead("b", ""))
	}
	staged, _ := preflightStaged(ops)
	if staged.visited != 0 || staged.puts.Len() != 1 {
		t.Fatalf("visited %d entries over %d staged keys, want 0 over 1", staged.visited, staged.puts.Len())
	}
}

func TestTxnStagedPuts_AGetIsChargedTheLatestPutOnce(t *testing.T) {
	ops := []protocol.DecodedOp{decodedPut("k", []byte("long")), decodedPut("k", []byte("v")), decodedRead("k", "")}
	staged, total := preflightStaged(ops)
	if want := int64(service.TxnReplyKVBytes + 2); staged.visited != 1 || total != want {
		t.Fatalf("visited %d, charged %d; want 1 entry of %d bytes", staged.visited, total, want)
	}
}

func TestTxnStagedPuts_ADeletedPutIsNotCharged(t *testing.T) {
	ops := []protocol.DecodedOp{
		decodedPut("k", []byte("v")),
		{Type: protocol.OpMVCCDelete, Key: []byte("k")},
		decodedRead("k", ""),
	}
	if staged, total := preflightStaged(ops); staged.visited != 0 || total != 0 {
		t.Fatalf("visited %d, charged %d; want the deleted put uncharged", staged.visited, total)
	}
}

func TestTxnStagedPuts_ARangeVisitsOnlyThePutsInIt(t *testing.T) {
	ops := []protocol.DecodedOp{
		decodedPut("a", nil), decodedPut("b", nil), decodedPut("c", nil), decodedPut("d", nil),
		decodedRead("b", "d"),
		decodedRead("c", "\x00"),
	}
	if staged, _ := preflightStaged(ops); staged.visited != 4 {
		t.Fatalf("visited %d staged entries, want b, c, then c, d", staged.visited)
	}
}

func TestTxnStagedPuts_RangesOverManyPutsStopAtTheBound(t *testing.T) {
	ops := make([]protocol.DecodedOp, 0, 101_000)
	for i := range 1000 {
		ops = append(ops, decodedPut(string([]byte{byte(i >> 8), byte(i)}), nil))
	}
	for range 100_000 {
		ops = append(ops, decodedRead("\x00", "\x00"))
	}
	staged, total := preflightStaged(ops)
	if total <= service.MaxReplyBytes {
		t.Fatalf("charged %d bytes, want the bound %d crossed", total, service.MaxReplyBytes)
	}
	if limit := service.MaxReplyBytes/service.TxnReplyKVBytes + 1; staged.visited > limit {
		t.Fatalf("visited %d staged entries, want at most %d: every visit is charged", staged.visited, limit)
	}
}

func TestMVCCEngine_TransactionRead_ManyGetsOfAnAbsentKeyApply(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	ops := append([][]byte{txnPutOp("other", []byte("o"))}, txnGets("absent", 100_000)...)
	cmd := mustEncode(protocol.EncodeTransaction(nil, ops, nil))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := eng.Store().Get([]byte("other")); err != nil {
		t.Fatalf("other: %v, want the branch's write applied", err)
	}
}

func TestMVCCEngine_TransactionRead_ARangeOverStagedPutsIsRefused(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	value := make([]byte, txnBoundValueSize)
	var ops [][]byte
	for i := range 17 {
		ops = append(ops, txnPutOp(string([]byte{'a' + byte(i)}), value))
	}
	ops = append(ops, mustEncode(protocol.EncodeOp(protocol.OpMVCCRange, []byte("a"), nil, []byte("z"), 0)))
	assertTxnReadRefused(t, updateTxn(t, eng, 1, ops))
	assertNotWritten(t, eng, "a")
}
