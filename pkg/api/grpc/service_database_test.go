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
