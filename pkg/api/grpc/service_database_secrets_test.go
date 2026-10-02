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
	"context"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dbwire/dbwiretest"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// wireJSON renders a response as JSON so the shared assertion can search it.
// Every set field is rendered, so a secret in any field is found.
func wireJSON(t *testing.T, m proto.Message) []byte {
	t.Helper()
	out, err := protojson.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A database's discovery secrets are write-only: no read of the database
// returns them, only that each one is set. The Unix socket serves this same
// service implementation; sdk/go/transport/unix asserts it over the socket.
func TestGRPC_DatabaseSecrets_NeverReadBack(t *testing.T) {
	svc := newRealDatabaseService(t)
	dbwiretest.Seed(t, svc)
	client, cleanup := startTestServerFull(t, WithDatabaseService(svc))
	defer cleanup()
	ctx := context.Background()

	got, err := client.DatabaseGet(ctx, &pb.DatabaseGetRequest{Name: dbwiretest.Name})
	if err != nil {
		t.Fatalf("DatabaseGet: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "gRPC DatabaseGet", wireJSON(t, got))

	list, err := client.DatabaseList(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("DatabaseList: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "gRPC DatabaseList", wireJSON(t, list))

	updated, err := client.DatabaseUpdateSQL(ctx, &pb.DatabaseUpdateSQLRequest{
		Name: dbwiretest.Name, Sql: &pb.DatabaseSQLConfig{Concurrency: "optimistic"},
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateSQL: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "gRPC DatabaseUpdateSQL", wireJSON(t, updated))

	// The update that sets the secrets does not echo them either.
	raft, err := client.DatabaseUpdateRaft(ctx, &pb.DatabaseUpdateRaftRequest{
		Name: dbwiretest.Name,
		Raft: &pb.DatabaseRaftConfig{
			ClusterMode: "distributed",
			ReplicaId:   1,
			Discovery:   "static",
			Static:      &pb.DatabaseStaticDiscovery{Peers: []string{"db.example.com:63001"}, SharedSecret: dbwiretest.StaticSecret},
			Multicast:   &pb.DatabaseMulticastDiscovery{SharedSecret: dbwiretest.MulticastSecret, MulticastGroup: "239.255.0.1:63100"},
			Token:       &pb.DatabaseTokenDiscovery{Endpoint: "https://db.example.com/discovery", Token: dbwiretest.DiscoveryToken, ClusterSize: 3},
		},
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateRaft: %v", err)
	}
	dbwiretest.AssertNoSecrets(t, "gRPC DatabaseUpdateRaft", wireJSON(t, raft))
}
