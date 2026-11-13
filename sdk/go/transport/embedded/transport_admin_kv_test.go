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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// mapWriteMode / mapConsistencyLevel
// ===========================================================================

func TestMapWriteMode(t *testing.T) {
	cases := []struct {
		in   transport.WriteMode
		want writemode.Mode
	}{
		{transport.WriteModeSync, writemode.Sync},
		{transport.WriteModeWALDurable, writemode.WALDurable},
		{transport.WriteModeBatched, writemode.Batched},
		{transport.WriteMode(99), writemode.Sync},
	}
	for _, c := range cases {
		if got := mapWriteMode(c.in); got != c.want {
			t.Errorf("mapWriteMode(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestMapConsistencyLevel(t *testing.T) {
	cases := []struct {
		in   transport.ConsistencyLevel
		want consistency.Level
	}{
		{transport.ConsistencyStrong, consistency.Strong},
		{transport.ConsistencyLinearizable, consistency.Linearizable},
		{transport.ConsistencyEventual, consistency.Eventual},
		{transport.ConsistencyLevel(99), consistency.Linearizable},
	}
	for _, c := range cases {
		if got := mapConsistencyLevel(c.in); got != c.want {
			t.Errorf("mapConsistencyLevel(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// ===========================================================================
// Scan + Exists
// ===========================================================================

func TestScan_Default(t *testing.T) {
	kv := newMockKVService()
	_ = kv.Put(context.Background(), "p/a", []byte("1"))
	_ = kv.Put(context.Background(), "p/b", []byte("2"))
	_ = kv.Put(context.Background(), "q/x", []byte("3"))
	tr, _ := New(kv, &mockAdminService{})
	out, err := tr.Scan(context.Background(), "p/")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len = %d, want 2", len(out))
	}
}

func TestScan_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()
	_, err := tr.Scan(context.Background(), "p")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestExists_WithConsistency(t *testing.T) {
	kv := newMockKVService()
	_ = kv.Put(context.Background(), "k", []byte("v"))
	tr, _ := New(kv, &mockAdminService{})
	ok, err := tr.Exists(context.Background(), "k", transport.WithConsistency(transport.ConsistencyStrong))
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("expected true")
	}
}

func TestExists_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()
	_, err := tr.Exists(context.Background(), "k")
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Capabilities / StartShard / StopShard / GetShardState / GetTopology
// ===========================================================================

func TestCapabilities_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	caps, err := tr.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !caps.KV || !caps.MVCC || !caps.Index {
		t.Errorf("caps = %+v", caps)
	}
}

func TestCapabilities_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()
	_, err := tr.Capabilities(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestStartShard_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	if err := tr.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 2,
		Members:   map[uint64]string{1: "n1"},
	}); err != nil {
		t.Fatalf("StartShard: %v", err)
	}
}

func TestStartShard_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()
	err := tr.StartShard(context.Background(), &transport.StartShardRequest{ShardID: 1})
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestStopShard_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	if err := tr.StopShard(context.Background(), 1); err != nil {
		t.Fatalf("StopShard: %v", err)
	}
}

func TestStopShard_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()
	err := tr.StopShard(context.Background(), 1)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGetShardState_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	resp, err := tr.GetShardState(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetShardState: %v", err)
	}
	if resp.ShardID != 1 || !resp.HasLeader {
		t.Errorf("resp = %+v", resp)
	}
}

func TestGetShardState_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()
	_, err := tr.GetShardState(context.Background(), 1)
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestGetTopology_Success(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	resp, err := tr.GetTopology(context.Background())
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if resp.NodeID != "test-node" {
		t.Errorf("NodeID = %q", resp.NodeID)
	}
}

func TestGetTopology_AfterClose(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	_ = tr.Close()
	_, err := tr.GetTopology(context.Background())
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// Dispatch handlers exercised through Request for capabilities/topology
// ===========================================================================

func TestDispatch_Capabilities(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	var resp *transport.CapabilitiesResponse
	if err := tr.Request(context.Background(), "Capabilities", nil, &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp == nil || !resp.KV {
		t.Errorf("resp = %+v", resp)
	}
}

func TestDispatch_StartShard(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	if err := tr.Request(context.Background(), "StartShard", &transport.StartShardRequest{ShardID: 1}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestDispatch_StartShard_BadReq(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	err := tr.Request(context.Background(), "StartShard", "bad", nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T", err)
	}
}

func TestDispatch_StopShard(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	if err := tr.Request(context.Background(), "StopShard", &transport.StopShardRequest{ShardID: 1}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestDispatch_StopShard_BadReq(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	err := tr.Request(context.Background(), "StopShard", "bad", nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T", err)
	}
}

func TestDispatch_GetShardState(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	var resp *transport.ShardStateResponse
	if err := tr.Request(context.Background(), "GetShardState", uint64(1), &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
}

func TestDispatch_GetShardState_BadReq(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	err := tr.Request(context.Background(), "GetShardState", "bad", nil)
	var invErr *InvalidRequestError
	if !errors.As(err, &invErr) {
		t.Fatalf("expected InvalidRequestError, got %T", err)
	}
}

func TestDispatch_GetTopology(t *testing.T) {
	tr, _ := New(newMockKVService(), &mockAdminService{})
	var resp *transport.TopologyResponse
	if err := tr.Request(context.Background(), "GetTopology", nil, &resp); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if resp == nil {
		t.Fatal("nil resp")
	}
}

// ===========================================================================
// Functional Option setters that don't have dedicated coverage yet.
// We can pass nil services because the Option closures unconditionally
// assign the provided value.
// ===========================================================================

func TestWithWatchService_Assigns(t *testing.T) {
	tr := newExtTransport(t, WithWatchService(nil))
	if tr.watchSvc != nil {
		t.Errorf("watchSvc = %v, want nil", tr.watchSvc)
	}
}

func TestWithVectorService_Assigns(t *testing.T) {
	tr := newExtTransport(t, WithVectorService(nil))
	if tr.vectorSvc != nil {
		t.Errorf("vectorSvc = %v, want nil", tr.vectorSvc)
	}
}

func TestWithCollectionService_Assigns(t *testing.T) {
	tr := newExtTransport(t, WithCollectionService(nil))
	if tr.collectionSvc != nil {
		t.Errorf("collectionSvc = %v, want nil", tr.collectionSvc)
	}
}

func TestWithDatabaseService_Assigns(t *testing.T) {
	tr := newExtTransport(t, WithDatabaseService(nil))
	if tr.dbSvc != nil {
		t.Errorf("dbSvc = %v, want nil", tr.dbSvc)
	}
}
