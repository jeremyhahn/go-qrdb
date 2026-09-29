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
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// KV: kv.Put / kv.Get / kv.Delete / kv.List / kv.Scan / Exists
// ---------------------------------------------------------------------------

func TestE2E_Put_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Put", func(req []byte) (uint16, []byte) {
		var r pb.PutRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		if r.GetKey() != "k" || string(r.GetValue()) != "v" {
			return wire.StatusError, []byte("bad args")
		}
		return wire.StatusOK, nil
	})
	if err := tr.Put(context.Background(), "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

func TestE2E_Get_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Get", func(req []byte) (uint16, []byte) {
		var r pb.GetRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		return wire.StatusOK, mustMarshal(&pb.GetResponse{Value: []byte("hello")})
	})
	v, err := tr.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(v) != "hello" {
		t.Fatalf("value = %q, want hello", string(v))
	}
}

// TestE2E_Get_KeyNotFound. The double answers wire.StatusNotFound because that
// is what a real server answers for service.KeyNotFoundError since
// go-qrdb#437; before it, the wire had no not-found status, this double
// returned StatusError, and the client recovered absence by finding the words
// "key not found" in the message. Both halves of that arrangement are gone.
func TestE2E_Get_KeyNotFound(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusNotFound, []byte("service: key not found: missing")
	})
	_, err := tr.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	var knf *transport.KeyNotFoundError
	if !errorAs(err, &knf) {
		t.Fatalf("expected *KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestE2E_Delete_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Delete", func(req []byte) (uint16, []byte) {
		var r pb.DeleteRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		if r.GetKey() != "k" {
			return wire.StatusError, []byte("bad key")
		}
		return wire.StatusOK, nil
	})
	if err := tr.Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestE2E_List_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.List", func(req []byte) (uint16, []byte) {
		var r pb.ListRequest
		if err := proto.Unmarshal(req, &r); err != nil {
			return wire.StatusError, []byte(err.Error())
		}
		return wire.StatusOK, mustMarshal(&pb.ListResponse{Keys: []string{"a", "b"}})
	})
	keys, err := tr.List(context.Background(), "p")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("keys = %v, want [a b]", keys)
	}
}

func TestE2E_Scan_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Scan", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.ScanResponse{
			Pairs: map[string][]byte{"a": []byte("1"), "b": []byte("2")},
		})
	})
	pairs, err := tr.Scan(context.Background(), "p")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(pairs) != 2 || string(pairs["a"]) != "1" {
		t.Fatalf("pairs = %v", pairs)
	}
}

func TestE2E_Exists_True(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.GetResponse{Value: []byte("x")})
	})
	ok, err := tr.Exists(context.Background(), "k")
	if err != nil || !ok {
		t.Fatalf("Exists = %v, %v, want true, nil", ok, err)
	}
}

// TestE2E_Exists_False: Exists is absence-or-error, so it turns on exactly the
// classification go-qrdb#437 made possible. The double sends the frame a real
// server sends.
func TestE2E_Exists_False(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusNotFound, []byte("service: key not found: k")
	})
	ok, err := tr.Exists(context.Background(), "k")
	if err != nil || ok {
		t.Fatalf("Exists = %v, %v, want false, nil", ok, err)
	}
}

// ---------------------------------------------------------------------------
// Admin: Health / Status / Peers / Members / Snapshot / Drain / Capabilities
// ---------------------------------------------------------------------------

func TestE2E_Status_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Status", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.StatusResponse{
			NodeId:      "n1",
			RaftAddress: "1.2.3.4:1",
			IsLeader:    true,
			LeaderId:    5,
			ShardCount:  3,
			IsDraining:  false,
		})
	})
	resp, err := tr.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.NodeID != "n1" || !resp.IsLeader || resp.ShardCount != 3 {
		t.Fatalf("Status = %+v", resp)
	}
}

func TestE2E_Peers_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Peers", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.PeersResponse{
			Shards: []*pb.ShardPeers{
				{
					ShardId:  1,
					LeaderId: 1,
					Peers: []*pb.PeerInfo{
						{ReplicaId: 1, Address: "a:1", IsLeader: true},
						{ReplicaId: 2, Address: "b:2"},
					},
				},
			},
		})
	})
	resp, err := tr.Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers: %v", err)
	}
	if len(resp.Shards) != 1 || len(resp.Shards[0].Peers) != 2 {
		t.Fatalf("Peers = %+v", resp)
	}
}

func TestE2E_AddMember_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.AddMember", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	identity := "spki:" + strings.Repeat("ab", 32)
	if err := tr.AddMember(context.Background(), 7, 8, "host:9", identity); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	// Read the marshaled request through the harness's mutex-protected recorder
	// rather than capturing it in the responder closure (which runs in the
	// server goroutine and would race with this read under -race).
	rec := srv.LastRequest("admin.AddMember")
	if rec == nil {
		t.Fatal("no recorded admin.AddMember request")
	}
	var r pb.AddMemberRequest
	if err := proto.Unmarshal(rec.Payload, &r); err != nil {
		t.Fatalf("unmarshal recorded request: %v", err)
	}
	if r.GetShardId() != 7 || r.GetReplicaId() != 8 || r.GetRaftAddress() != "host:9" || r.GetMemberIdentity() != identity {
		t.Fatalf("captured = shard:%d rep:%d addr:%q identity:%q", r.GetShardId(), r.GetReplicaId(), r.GetRaftAddress(), r.GetMemberIdentity())
	}
}

func TestE2E_RemoveMember_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.RemoveMember", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.RemoveMember(context.Background(), 1, 2); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
}

func TestE2E_RequestSnapshot_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.RequestSnapshot", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.RequestSnapshot(context.Background(), 1); err != nil {
		t.Fatalf("RequestSnapshot: %v", err)
	}
}

func TestE2E_Drain_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Drain", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

func TestE2E_Capabilities_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Capabilities", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.CapabilitiesResponse{
			Kv: true, Mvcc: true, Transaction: true, Batch: true,
			Watch: true, Lease: true, Index: true, TimeSeries: true, Vector: true,
		})
	})
	resp, err := tr.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !resp.KV || !resp.MVCC || !resp.Vector {
		t.Fatalf("Capabilities = %+v", resp)
	}
}

func TestE2E_StartShard_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.StartShard", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID: 1, ReplicaID: 1, Members: map[uint64]string{1: "h:1"}, Join: false,
	}); err != nil {
		t.Fatalf("StartShard: %v", err)
	}
}

func TestE2E_StopShard_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.StopShard", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, nil
	})
	if err := tr.StopShard(context.Background(), 1); err != nil {
		t.Fatalf("StopShard: %v", err)
	}
}

func TestE2E_GetShardState_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.GetShardState", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.ShardStateResponse{
			ShardId: 1, ConfigChangeIndex: 7, Term: 4, LeaderId: 3, HasLeader: true,
		})
	})
	resp, err := tr.GetShardState(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetShardState: %v", err)
	}
	if resp.ShardID != 1 || resp.Term != 4 || !resp.HasLeader {
		t.Fatalf("GetShardState = %+v", resp)
	}
}

func TestE2E_GetTopology_HappyPath(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.GetTopology", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, mustMarshal(&pb.TopologyResponse{
			NodeId: "n1", RaftAddress: "a:1", ShardCount: 1,
			Shards: []*pb.ShardInfoProto{
				{ShardId: 1, ReplicaId: 2, IsLeader: true, LeaderId: 2, Nodes: map[uint64]string{1: "n1"}},
			},
		})
	})
	resp, err := tr.GetTopology(context.Background())
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if resp.NodeID != "n1" || len(resp.Shards) != 1 {
		t.Fatalf("GetTopology = %+v", resp)
	}
}
