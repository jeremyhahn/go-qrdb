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

package grpc

import (
	"bytes"
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
)

// txnStateMachineHost is a service.Host whose SyncPropose applies the
// command to a REAL MVCC state machine, one log index after another.
type txnStateMachineHost struct {
	service.Host
	sm *store.MVCCStateMachine

	mu    sync.Mutex
	index uint64
}

func (h *txnStateMachineHost) SyncPropose(ctx context.Context, _ uint64, cmd []byte) (sm.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.index++
	results := make([]sm.Result, 1)
	if err := h.sm.Update(ctx, []sm.Entry{{Index: h.index, Cmd: cmd}}, results); err != nil {
		return sm.Result{}, err
	}
	return results[0], nil
}

// Transaction refuses as every data method of the ingress node does.
func (r *refusingService) Transaction(context.Context, *pb.TransactionRequest) (*pb.TransactionResponse, error) {
	r.calls.Add(1)
	return nil, mapServiceError(r.refusal)
}

// TestReplicaForwardGRPC_ATransactionWhoseReplyCrossesTheBoundIsRefusedBeforeItsWrites
// is the Codex round-3 P1 of #1211: the reads of the branch answer no more
// than service.MaxReplyBytes of key and value bytes, but the whole encoded
// reply -- with the framing of every op and entry -- does not fit the
// forwarder's receive limit. The replica must refuse the transaction before
// its put is staged, rather than commit it and then fail to deliver the
// reply through the forwarder.
func TestReplicaForwardGRPC_ATransactionWhoseReplyCrossesTheBoundIsRefusedBeforeItsWrites(t *testing.T) {
	ctx := context.Background()
	machine := store.NewMVCCStateMachine(nil)
	if _, err := machine.Open(ctx, t.TempDir(), make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = machine.Close(ctx) })
	txSvc := service.NewTransactionService(&txnStateMachineHost{sm: machine}, 1)

	// Sixteen gets of k answer exactly MaxReplyBytes of key and value bytes.
	value := make([]byte, service.MaxReplyBytes/16-1)
	put := func(key string, v []byte) *pb.Op { return &pb.Op{Type: pb.OpType_OP_PUT, Key: []byte(key), Value: v} }
	get := func(key string) *pb.Op { return &pb.Op{Type: pb.OpType_OP_GET, Key: []byte(key)} }
	if _, err := txSvc.Execute(ctx, nil, []service.Op{protoToOp(put("k", value))}, nil); err != nil {
		t.Fatalf("seeding k: %v", err)
	}

	ca := replicaforwardtest.NewCA(t)
	b := &fakeTarget{raftAddr: "raft-b"}
	b.addr = serveGRPC(t, ca.TLS(t, "node-1"), &qrdbServiceServer{txSvc: txSvc}, b.record, servedByUnary("node-1"))
	c := newFwdCluster(t, ca, 30*time.Second, b)

	ops := []*pb.Op{put("other", []byte("o"))}
	for range 16 {
		ops = append(ops, get("k"))
	}
	if _, err := c.client.Transaction(callerCtx(), &pb.TransactionRequest{SuccessOps: ops}); err == nil {
		t.Fatal("a transaction whose reply exceeds the forwarder's limit succeeded")
	}
	if n := b.calls.Load(); n != 1 {
		t.Fatalf("replica calls = %d; want the transaction forwarded once", n)
	}
	res, err := txSvc.Execute(ctx, nil, []service.Op{protoToOp(get("other"))}, nil)
	if err != nil {
		t.Fatalf("reading other: %v", err)
	}
	if len(res.Responses) != 1 || len(res.Responses[0].Kvs) != 0 {
		t.Fatalf("other = %+v; want the refused transaction's put not applied", res.Responses)
	}
}

// txnReplyEstimate is the reply size the transaction guard charges.
func txnReplyEstimate(resp *pb.TransactionResponse) int {
	n := service.TxnReplyHeaderBytes + len(resp.GetResponses())*service.TxnReplyOpBytes
	for _, r := range resp.GetResponses() {
		for _, kv := range r.GetKvs() {
			n += service.TxnReplyKVBytes + len(kv.GetKey()) + len(kv.GetValue())
		}
	}
	return n
}

// TestTxnReplyEstimate_BoundsTheWorstCaseEncoding checks the overhead
// constants against the encoding: every int64 is negative (a ten-byte
// varint) and every bool true, so no field encodes larger than here.
func TestTxnReplyEstimate_BoundsTheWorstCaseEncoding(t *testing.T) {
	kv := func(key, value int) *pb.KeyValue {
		return &pb.KeyValue{Key: bytes.Repeat([]byte("k"), key), Value: bytes.Repeat([]byte("v"), value),
			CreateRevision: math.MinInt64, ModRevision: -1, Version: math.MinInt64, LeaseId: -1}
	}
	op := func(kvs ...*pb.KeyValue) *pb.OpResponse {
		return &pb.OpResponse{Kvs: kvs, Count: math.MinInt64, Succeeded: true}
	}
	many := func(n int, f func() *pb.KeyValue) []*pb.KeyValue {
		kvs := make([]*pb.KeyValue, n)
		for i := range kvs {
			kvs[i] = f()
		}
		return kvs
	}
	for name, responses := range map[string][]*pb.OpResponse{
		"no ops":       nil,
		"one empty op": {op()},
		"many empty ops": func() []*pb.OpResponse {
			r := make([]*pb.OpResponse, 1000)
			for i := range r {
				r[i] = op()
			}
			return r
		}(),
		"one large entry":  {op(kv(1, service.MaxReplyBytes-200))},
		"many small":       {op(many(10000, func() *pb.KeyValue { return kv(1, 0) })...)},
		"entries of 200 B": {op(many(1000, func() *pb.KeyValue { return kv(100, 100) })...), op(kv(0, 0))},
	} {
		resp := &pb.TransactionResponse{Succeeded: true, Revision: math.MinInt64, Responses: responses}
		if size, est := proto.Size(resp), txnReplyEstimate(resp); size > est {
			t.Errorf("%s: encoded %d bytes, estimated %d", name, size, est)
		}
	}
}
