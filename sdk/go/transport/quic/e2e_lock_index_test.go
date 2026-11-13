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
// Lock
// ---------------------------------------------------------------------------

func TestE2E_LockAcquire_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lock.Acquire", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.LockAcquireResponse{
			Lock: &pb.LockInfo{Key: "k", FencingToken: 7, HolderId: "h", Ttl: 30},
		})
	})
	info, err := tr.LockAcquire(context.Background(), &transport.LockAcquireRequest{
		Key: "k", HolderID: "h", TTL: 30, Reentrant: false,
	})
	if err != nil {
		t.Fatalf("LockAcquire: %v", err)
	}
	if info.FencingToken != 7 {
		t.Fatalf("info = %+v", info)
	}
}

func TestE2E_LockList_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("lock.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.LockListResponse{
			Locks: []*pb.LockInfo{
				{Key: "a", FencingToken: 1},
				{Key: "b", FencingToken: 2},
			},
		})
	})
	resp, err := tr.LockList(context.Background(), "")
	if err != nil {
		t.Fatalf("LockList: %v", err)
	}
	if len(resp.Locks) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}

// ---------------------------------------------------------------------------
// Index
// ---------------------------------------------------------------------------

func TestE2E_IndexCreate_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("index.Create", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.IndexCreate(context.Background(), "idx", "field", transport.IndexConfig{Unique: true}); err != nil {
		t.Fatalf("IndexCreate: %v", err)
	}
}

func TestE2E_IndexDrop_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("index.Drop", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.IndexDrop(context.Background(), "idx"); err != nil {
		t.Fatalf("IndexDrop: %v", err)
	}
}

func TestE2E_IndexQuery_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("index.Query", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.IndexQueryResponse{
			Keys: [][]byte{[]byte("k1"), []byte("k2")},
		})
	})
	resp, err := tr.IndexQuery(context.Background(), "idx", "v")
	if err != nil {
		t.Fatalf("IndexQuery: %v", err)
	}
	if len(resp.Keys) != 2 {
		t.Fatalf("Keys = %v", resp.Keys)
	}
}

func TestE2E_IndexScan_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("index.Scan", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.IndexScanResponse{
			Entries: []*pb.IndexScanEntry{
				{Value: "v1", Keys: [][]byte{[]byte("k1")}},
			},
		})
	})
	resp, err := tr.IndexScan(context.Background(), "idx", spIdx("a"), spIdx("z"))
	if err != nil {
		t.Fatalf("IndexScan: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("Entries = %v", resp.Entries)
	}
}

func TestE2E_IndexList_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("index.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.IndexListResponse{Names: []string{"a", "b"}})
	})
	resp, err := tr.IndexList(context.Background())
	if err != nil {
		t.Fatalf("IndexList: %v", err)
	}
	if len(resp.Names) != 2 {
		t.Fatalf("Names = %v", resp.Names)
	}
}

func spIdx(s string) *string { return &s }
