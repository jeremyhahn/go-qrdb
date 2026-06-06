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

package quic

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// MVCC
// ---------------------------------------------------------------------------

func TestE2E_PutWithLease_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.PutWithLease", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.PutWithLeaseResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Value: []byte("v"), Version: 1, LeaseId: 99},
		})
	})
	kv, err := tr.PutWithLease(context.Background(), "k", []byte("v"), 99)
	if err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
	if kv.LeaseID != 99 {
		t.Fatalf("LeaseID = %d, want 99", kv.LeaseID)
	}
}

func TestE2E_GetRevision_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.GetRevision", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.GetRevisionResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Value: []byte("v"), ModRevision: 3},
		})
	})
	kv, err := tr.GetRevision(context.Background(), "k")
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if kv.ModRevision != 3 {
		t.Fatalf("ModRevision = %d, want 3", kv.ModRevision)
	}
}

func TestE2E_GetAtRevision_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.GetAtRevision", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.GetAtRevisionResponse{
			Kv: &pb.KeyValue{Key: []byte("k"), Value: []byte("v"), Version: 5},
		})
	})
	kv, err := tr.GetAtRevision(context.Background(), "k", 5)
	if err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
	if kv.Version != 5 {
		t.Fatalf("Version = %d, want 5", kv.Version)
	}
}

func TestE2E_Range_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.Range", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.RangeResponse{
			Kvs: []*pb.KeyValue{
				{Key: []byte("a"), Value: []byte("1")},
				{Key: []byte("b"), Value: []byte("2")},
			},
			Count: 2,
		})
	})
	resp, err := tr.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{
		Limit:      10,
		Revision:   0,
		SortOrder:  transport.SortAscend,
		SortTarget: transport.SortByKey,
	})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if resp.Count != 2 || len(resp.Kvs) != 2 {
		t.Fatalf("Range = %+v", resp)
	}
}

func TestE2E_Compact_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.Compact", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.Compact(context.Background(), 7); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestE2E_CurrentRevision_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("mvcc.CurrentRevision", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CurrentRevisionResponse{Revision: 42})
	})
	rev, err := tr.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if rev != 42 {
		t.Fatalf("rev = %d, want 42", rev)
	}
}

// ---------------------------------------------------------------------------
// Transaction
// ---------------------------------------------------------------------------

func TestE2E_ExecuteTransaction_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tx.Transaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TransactionResponse{
			Succeeded: true,
			Revision:  9,
			Responses: []*pb.OpResponse{
				{Succeeded: true, Count: 1, Kvs: []*pb.KeyValue{{Key: []byte("a"), Value: []byte("1")}}},
			},
		})
	})
	req := &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("k"), Target: transport.CompareVersion, Result: transport.CompareEqual, Version: 1},
		},
		SuccessOps: []transport.Op{{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")}},
		FailureOps: []transport.Op{{Type: transport.OpGet, Key: []byte("k")}},
	}
	resp, err := tr.ExecuteTransaction(context.Background(), req)
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if !resp.Succeeded || resp.Revision != 9 {
		t.Fatalf("Resp = %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// Batch
// ---------------------------------------------------------------------------

func TestE2E_ExecuteBatch_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("batch.Batch", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.BatchResponse{
			Responses: []*pb.OpResponse{
				{Succeeded: true, Count: 1},
			},
		})
	})
	ops := []transport.Op{
		{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v")},
		{Type: transport.OpDelete, Key: []byte("k2")},
	}
	resp, err := tr.ExecuteBatch(context.Background(), ops)
	if err != nil {
		t.Fatalf("ExecuteBatch: %v", err)
	}
	if len(resp.Responses) != 1 {
		t.Fatalf("Responses = %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// Lease
// ---------------------------------------------------------------------------

func TestE2E_LeaseGrant_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.Grant", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.LeaseGrantResponse{
			Lease: &pb.Lease{Id: 1, Ttl: 60, GrantedTtl: 60},
		})
	})
	info, err := tr.LeaseGrant(context.Background(), 60)
	if err != nil {
		t.Fatalf("LeaseGrant: %v", err)
	}
	if info.ID != 1 || info.TTL != 60 {
		t.Fatalf("info = %+v", info)
	}
}

func TestE2E_LeaseRevoke_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.Revoke", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.LeaseRevoke(context.Background(), 1); err != nil {
		t.Fatalf("LeaseRevoke: %v", err)
	}
}

func TestE2E_LeaseKeepAlive_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.KeepAlive", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.LeaseKeepAliveResponse{
			Lease: &pb.Lease{Id: 1, Ttl: 60, RemainingTtl: 55},
		})
	})
	info, err := tr.LeaseKeepAlive(context.Background(), 1)
	if err != nil {
		t.Fatalf("LeaseKeepAlive: %v", err)
	}
	if info.RemainingTTL != 55 {
		t.Fatalf("RemainingTTL = %d", info.RemainingTTL)
	}
}

func TestE2E_LeaseTimeToLive_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.TimeToLive", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.LeaseTimeToLiveResponse{
			Lease: &pb.Lease{Id: 1, Ttl: 60, Keys: [][]byte{[]byte("k")}},
		})
	})
	info, err := tr.LeaseTimeToLive(context.Background(), 1)
	if err != nil {
		t.Fatalf("LeaseTimeToLive: %v", err)
	}
	if len(info.Keys) != 1 {
		t.Fatalf("Keys = %v", info.Keys)
	}
}

func TestE2E_LeaseList_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lease.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.LeaseListResponse{
			Leases: []*pb.Lease{{Id: 1}, {Id: 2}},
		})
	})
	leases, err := tr.LeaseList(context.Background())
	if err != nil {
		t.Fatalf("LeaseList: %v", err)
	}
	if len(leases) != 2 {
		t.Fatalf("leases = %v", leases)
	}
}
