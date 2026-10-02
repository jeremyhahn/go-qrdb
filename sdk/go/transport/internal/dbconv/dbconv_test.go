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

package dbconv

import (
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

func TestInfo_FullPayload(t *testing.T) {
	out := Info(&pb.DatabaseInfoResponse{
		TenantId:     "t1",
		Name:         "db1",
		Engine:       "kv",
		Backend:      "raft",
		Status:       "ready",
		DataDir:      "/data",
		ShardId:      17,
		Mode:         "embedded",
		StorageCodec: "msgpack",
		Replicas:     3,
		DedicatedWal: true,
		Protocol: &pb.DatabaseProtocolConfig{
			Mysql: true,
			Etcd: &pb.DatabaseEtcdConfig{
				Enabled:             true,
				ListenAddr:          ":2379",
				AdvertiseClientUrls: []string{"https://db.example.com:2379"},
			},
		},
		Raft: &pb.DatabaseRaftInfo{
			ClusterMode:    "distributed",
			ReplicaId:      2,
			RaftAddress:    "db.example.com:63001",
			Peers:          []string{"db.example.com:63001"},
			LocalNodeCount: 1,
			Discovery:      "static",
			Static:         &pb.DatabaseStaticDiscoveryInfo{Peers: []string{"db.example.com:63001"}, SharedSecretSet: true},
			Multicast: &pb.DatabaseMulticastDiscoveryInfo{
				SharedSecretSet: true, MulticastGroup: "239.255.0.1:63100", Iface: "eth0",
				MinPeers: 2, AnnounceInterval: "1s", DiscoverTimeout: "10s",
			},
			Dns:   &pb.DatabaseDNSDiscovery{Service: "qrdb", Proto: "tcp", Domain: "example.com", LookupTimeout: "5s"},
			Token: &pb.DatabaseTokenDiscoveryInfo{Endpoint: "https://db.example.com/join", TokenSet: true, ClusterSize: 3, Timeout: "30s"},
		},
		Barrier:    &pb.DatabaseBarrierInfo{Enabled: true, Mode: "shamir", SharesRequired: 2, SharesTotal: 3},
		Connection: &pb.DatabaseConnectionConfig{Transport: "grpc", RemoteAddress: "db.example.com:63000"},
		Sql:        &pb.DatabaseSQLConfig{Concurrency: "pessimistic"},
	})
	if out.TenantID != "t1" || out.Name != "db1" || out.ShardID != 17 || out.StorageCodec != "msgpack" || out.Replicas != 3 || !out.DedicatedWAL {
		t.Errorf("identity mismatch: %+v", out)
	}
	if !out.Protocol.MySQL || out.Protocol.Etcd == nil || out.Protocol.Etcd.ListenAddr != ":2379" {
		t.Errorf("protocol mismatch: %+v", out.Protocol)
	}
	r := out.Raft
	if r == nil || r.ClusterMode != "distributed" || r.ReplicaID != 2 || r.Discovery != "static" {
		t.Fatalf("raft mismatch: %+v", r)
	}
	if r.Static == nil || !r.Static.SharedSecretSet || len(r.Static.Peers) != 1 {
		t.Errorf("static mismatch: %+v", r.Static)
	}
	if m := r.Multicast; m == nil || !m.SharedSecretSet || m.Interface != "eth0" || m.MinPeers != 2 || m.DiscoverTimeout != "10s" {
		t.Errorf("multicast mismatch: %+v", r.Multicast)
	}
	if d := r.DNS; d == nil || d.Domain != "example.com" || d.LookupTimeout != "5s" {
		t.Errorf("dns mismatch: %+v", r.DNS)
	}
	if tk := r.Token; tk == nil || !tk.TokenSet || tk.ClusterSize != 3 || tk.Endpoint != "https://db.example.com/join" {
		t.Errorf("token mismatch: %+v", r.Token)
	}
	if b := out.Barrier; b == nil || !b.Enabled || b.Mode != "shamir" || b.SharesTotal != 3 {
		t.Errorf("barrier mismatch: %+v", out.Barrier)
	}
	if out.Connection == nil || out.Connection.Address != "db.example.com:63000" {
		t.Errorf("connection mismatch: %+v", out.Connection)
	}
	if out.SQL == nil || out.SQL.Concurrency != "pessimistic" {
		t.Errorf("sql mismatch: %+v", out.SQL)
	}
}

func TestInfo_MinimalPayload(t *testing.T) {
	out := Info(&pb.DatabaseInfoResponse{Name: "db"})
	if out.Name != "db" || out.Raft != nil || out.Barrier != nil || out.Connection != nil || out.SQL != nil {
		t.Errorf("minimal payload mapped to %+v", out)
	}
}

func TestInfo_RaftWithoutDiscovery(t *testing.T) {
	out := Info(&pb.DatabaseInfoResponse{Raft: &pb.DatabaseRaftInfo{ClusterMode: "local"}})
	if out.Raft == nil || out.Raft.Static != nil || out.Raft.Multicast != nil || out.Raft.DNS != nil || out.Raft.Token != nil {
		t.Errorf("raft without discovery mapped to %+v", out.Raft)
	}
}

func TestInfos(t *testing.T) {
	if got := Infos(nil); got == nil || len(got) != 0 {
		t.Errorf("Infos(nil) = %#v, want an empty, non-nil list", got)
	}
	got := Infos([]*pb.DatabaseInfoResponse{{Name: "a"}, {Name: "b"}})
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("Infos = %+v", got)
	}
}
