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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

func TestGRPC_DatabaseList_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithDatabaseService(newMockDatabaseService()))
	defer cleanup()

	resp, err := client.DatabaseList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("DatabaseList failed: %v", err)
	}
	if len(resp.GetDatabases()) != 1 {
		t.Fatalf("expected 1 database, got %d", len(resp.GetDatabases()))
	}
}

func TestGRPC_DatabaseDrop_Success(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithDatabaseService(newMockDatabaseService()))
	defer cleanup()

	if _, err := client.DatabaseDrop(context.Background(), &pb.DatabaseDropRequest{Name: "testdb"}); err != nil {
		t.Fatalf("DatabaseDrop failed: %v", err)
	}
}

// TestGRPC_DatabaseUpdate_ReturnsDatabase checks that each per-section
// update handler answers with the updated database's info.
func TestGRPC_DatabaseUpdate_ReturnsDatabase(t *testing.T) {
	calls := map[string]func(pb.QRDBServiceClient) (*pb.DatabaseInfoResponse, error){
		"DatabaseUpdateProtocol": func(c pb.QRDBServiceClient) (*pb.DatabaseInfoResponse, error) {
			return c.DatabaseUpdateProtocol(context.Background(), &pb.DatabaseUpdateProtocolRequest{Name: "testdb"})
		},
		"DatabaseUpdateEtcd": func(c pb.QRDBServiceClient) (*pb.DatabaseInfoResponse, error) {
			return c.DatabaseUpdateEtcd(context.Background(), &pb.DatabaseUpdateEtcdRequest{
				Name: "testdb", ListenAddr: "127.0.0.1:2379",
			})
		},
		"DatabaseUpdateRaft": func(c pb.QRDBServiceClient) (*pb.DatabaseInfoResponse, error) {
			return c.DatabaseUpdateRaft(context.Background(), &pb.DatabaseUpdateRaftRequest{Name: "testdb"})
		},
		"DatabaseUpdateConnection": func(c pb.QRDBServiceClient) (*pb.DatabaseInfoResponse, error) {
			return c.DatabaseUpdateConnection(context.Background(), &pb.DatabaseUpdateConnectionRequest{Name: "testdb"})
		},
	}
	client, cleanup := startTestServerFull(t, WithDatabaseService(newMockDatabaseService()))
	defer cleanup()
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			resp, err := call(client)
			if err != nil {
				t.Fatalf("%s failed: %v", name, err)
			}
			if resp.GetName() != "testdb" {
				t.Fatalf("expected name 'testdb', got %q", resp.GetName())
			}
		})
	}
}

// TestGRPC_Database_NilService checks that every database catalog RPC
// answers Unimplemented when the server has no database service.
func TestGRPC_Database_NilService(t *testing.T) {
	calls := map[string]func(pb.QRDBServiceClient) error{
		"DatabaseCreate": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{Name: "db"})
			return err
		},
		"DatabaseGet": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseGet(context.Background(), &pb.DatabaseGetRequest{Name: "db"})
			return err
		},
		"DatabaseList": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseList(context.Background(), &emptypb.Empty{})
			return err
		},
		"DatabaseDrop": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseDrop(context.Background(), &pb.DatabaseDropRequest{Name: "db"})
			return err
		},
		"DatabaseUpdateProtocol": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateProtocol(context.Background(), &pb.DatabaseUpdateProtocolRequest{Name: "db"})
			return err
		},
		"DatabaseUpdateEtcd": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateEtcd(context.Background(), &pb.DatabaseUpdateEtcdRequest{Name: "db"})
			return err
		},
		"DatabaseUpdateRaft": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateRaft(context.Background(), &pb.DatabaseUpdateRaftRequest{Name: "db"})
			return err
		},
		"DatabaseUpdateBarrier": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateBarrier(context.Background(), &pb.DatabaseUpdateBarrierRequest{Name: "db"})
			return err
		},
		"DatabaseUpdateConnection": func(c pb.QRDBServiceClient) error {
			_, err := c.DatabaseUpdateConnection(context.Background(), &pb.DatabaseUpdateConnectionRequest{Name: "db"})
			return err
		},
	}
	client, cleanup := startTestServerFull(t)
	defer cleanup()
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			requireUnimplemented(t, call(client))
		})
	}
}

func TestProtoDatabaseCreateToConfig_WithProtocol(t *testing.T) {
	req := &pb.DatabaseCreateRequest{
		Name:    "db",
		Backend: "pebble",
		Protocol: &pb.DatabaseProtocolConfig{
			Mysql:      true,
			Postgresql: false,
			Etcd: &pb.DatabaseEtcdConfig{
				Enabled:    true,
				ListenAddr: "127.0.0.1:2379",
			},
		},
	}
	cfg := protoDatabaseCreateToConfig(req)
	if cfg.Name != "db" {
		t.Fatalf("expected name 'db', got %q", cfg.Name)
	}
	if !cfg.Protocol.MySQL {
		t.Fatal("expected MySQL=true")
	}
	if cfg.Protocol.Etcd == nil || cfg.Protocol.Etcd.ListenAddr != "127.0.0.1:2379" {
		t.Fatalf("expected the etcd listen address forwarded, got %+v", cfg.Protocol.Etcd)
	}
}

func TestProtoDatabaseCreateToConfig_WithRaft(t *testing.T) {
	req := &pb.DatabaseCreateRequest{
		Name: "db",
		Raft: &pb.DatabaseRaftConfig{
			ReplicaId:   1,
			RaftAddress: "127.0.0.1:63001",
			Static: &pb.DatabaseStaticDiscovery{
				Peers:        []string{"127.0.0.1:63001"},
				SharedSecret: "secret",
			},
		},
	}
	cfg := protoDatabaseCreateToConfig(req)
	if cfg.Raft == nil {
		t.Fatal("expected non-nil Raft config")
	}
	if cfg.Raft.ReplicaID != 1 {
		t.Fatalf("expected replica_id=1, got %d", cfg.Raft.ReplicaID)
	}
	// The wire carries no shard ID: the cluster issues it at create.
	if cfg.Raft.ShardID != 0 {
		t.Fatalf("a create request produced shard ID %d", cfg.Raft.ShardID)
	}
	if cfg.Raft.Static == nil {
		t.Fatal("expected non-nil Static discovery config")
	}
}

// TestProtoConfigToDomain_NilInputIsNil checks that an absent wire section
// converts to an absent domain section, so an update leaves it unset.
func TestProtoConfigToDomain_NilInputIsNil(t *testing.T) {
	if got := protoRaftConfigToDomain(nil); got != nil {
		t.Errorf("protoRaftConfigToDomain(nil) = %+v, want nil", got)
	}
	if got := protoBarrierConfigToDomain(nil); got != nil {
		t.Errorf("protoBarrierConfigToDomain(nil) = %+v, want nil", got)
	}
	if got := protoConnectionConfigToDomain(nil); got != nil {
		t.Errorf("protoConnectionConfigToDomain(nil) = %+v, want nil", got)
	}
}

func TestProtoRaftConfigToDomain_WithMulticast(t *testing.T) {
	result := protoRaftConfigToDomain(&pb.DatabaseRaftConfig{
		Multicast: &pb.DatabaseMulticastDiscovery{
			SharedSecret:   "secret",
			MulticastGroup: "224.0.0.1",
			MinPeers:       3,
		},
	})
	if result.Multicast == nil {
		t.Fatal("expected non-nil Multicast config")
	}
	if result.Multicast.MulticastGroup != "224.0.0.1" {
		t.Fatalf("expected MulticastGroup '224.0.0.1', got %q", result.Multicast.MulticastGroup)
	}
}

func TestProtoRaftConfigToDomain_WithDNS(t *testing.T) {
	result := protoRaftConfigToDomain(&pb.DatabaseRaftConfig{
		Dns: &pb.DatabaseDNSDiscovery{
			Service: "_raft",
			Domain:  "example.com",
		},
	})
	if result.DNS == nil {
		t.Fatal("expected non-nil DNS config")
	}
	if result.DNS.Domain != "example.com" {
		t.Fatalf("expected Domain 'example.com', got %q", result.DNS.Domain)
	}
}

func TestProtoRaftConfigToDomain_WithToken(t *testing.T) {
	result := protoRaftConfigToDomain(&pb.DatabaseRaftConfig{
		Token: &pb.DatabaseTokenDiscovery{
			Endpoint:    "https://db.example.com/discovery",
			Token:       "tok",
			ClusterSize: 3,
		},
	})
	if result.Token == nil {
		t.Fatal("expected non-nil Token config")
	}
	if result.Token.ClusterSize != 3 {
		t.Fatalf("expected ClusterSize=3, got %d", result.Token.ClusterSize)
	}
}

func TestProtoConnectionConfigToDomain_WithValues(t *testing.T) {
	result := protoConnectionConfigToDomain(&pb.DatabaseConnectionConfig{
		Transport:     "grpc",
		RemoteAddress: "127.0.0.1:9000",
		CaFile:        "/etc/ca.pem",
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Transport != "grpc" {
		t.Fatalf("expected Transport 'grpc', got %q", result.Transport)
	}
	if result.Address != "127.0.0.1:9000" {
		t.Fatalf("expected Address '127.0.0.1:9000', got %q", result.Address)
	}
}
