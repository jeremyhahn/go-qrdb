// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package integration_test

// Replica forwarding over gRPC on real qrdbd nodes: a node that holds no
// replica of a database forwards a request for it to the database's leader,
// as the caller, and relays the answer.

import (
	"context"
	"crypto/tls"
	"fmt"
	"strconv"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// grpcFwdClient is a gRPC client of node i authenticated by cert/key.
func (c *fwdCluster) grpcFwdClient(t *testing.T, i int, certPath, keyPath string) pb.QRDBServiceClient {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", c.nodes[i].grpc), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs: c.ca.Pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13,
	})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return pb.NewQRDBServiceClient(cc)
}

// dbCtx scopes a call to database db.
func dbCtx(t *testing.T, db string) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	return metadata.AppendToOutgoingContext(ctx, "x-qrdb-database", db), cancel
}

// grpcPut writes key=value through client and returns the replica the answer
// says served it ("" when the node served it itself).
func grpcPut(t *testing.T, client pb.QRDBServiceClient, db, key, value string) (string, error) {
	t.Helper()
	ctx, cancel := dbCtx(t, db)
	defer cancel()
	var header metadata.MD
	_, err := client.Put(ctx, &pb.PutRequest{Key: key, Value: []byte(value)}, grpc.Header(&header))
	if v := header.Get("x-qrdb-forwarded-to"); len(v) == 1 {
		return v[0], err
	}
	return "", err
}

// grpcGet reads key through client.
func grpcGet(t *testing.T, client pb.QRDBServiceClient, db, key string) (string, error) {
	t.Helper()
	ctx, cancel := dbCtx(t, db)
	defer cancel()
	resp, err := client.Get(ctx, &pb.GetRequest{Key: key})
	return string(resp.GetValue()), err
}

// notAReplica returns the replica-set refusal err carries, if it is one.
func notAReplica(err error) (*errdetails.ErrorInfo, bool) {
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetReason() == "NOT_A_REPLICA" {
			return info, true
		}
	}
	return nil, false
}

// TestReplicaForwarding_GRPC: three nodes, a single-replica database, so two
// nodes hold no replica of it.
func TestReplicaForwarding_GRPC(t *testing.T) {
	c := startFwdCluster(t, 3)
	const db = "fwd-grpc"
	shard := c.createDatabase(t, db, 1)
	p := c.placementOf(t, shard, 1)
	replica := p.leader
	others := c.nonReplicas(p)
	t.Logf("database %s: shard %d on node %d (replica %d); forwarding through nodes %v", db, shard, replica+1, p.leaderRID, others)

	writer := make([]pb.QRDBServiceClient, len(c.nodes))
	for i := range c.nodes {
		writer[i] = c.grpcFwdClient(t, i, c.writerCert, c.writerKey)
	}

	t.Run("writes and reads through a non-replica reach the replica", func(t *testing.T) {
		for _, i := range others {
			key := "k-via-" + strconv.Itoa(i+1)
			servedBy, err := grpcPut(t, writer[i], db, key, "v"+strconv.Itoa(i+1))
			if err != nil {
				t.Fatalf("Put through non-replica node %d = %v", i+1, err)
			}
			if servedBy != strconv.FormatUint(p.leaderRID, 10) {
				t.Fatalf("Put through node %d: x-qrdb-forwarded-to = %q; want replica %d", i+1, servedBy, p.leaderRID)
			}
		}
		for _, i := range c.live() {
			for _, j := range others {
				got, err := grpcGet(t, writer[i], db, "k-via-"+strconv.Itoa(j+1))
				if err != nil || got != "v"+strconv.Itoa(j+1) {
					t.Fatalf("Get through node %d of the write via node %d = %q, %v", i+1, j+1, got, err)
				}
			}
		}
		if servedBy, err := grpcPut(t, writer[replica], db, "k-direct", "v"); err != nil || servedBy != "" {
			t.Fatalf("Put through the replica = %q, %v; want served directly", servedBy, err)
		}
	})

	t.Run("the caller's own authorization applies", func(t *testing.T) {
		noKV := c.grpcFwdClient(t, others[0], c.noKVCert, c.noKVKey)
		if _, err := grpcPut(t, noKV, db, "k-denied", "v"); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("Put by a caller without kv:put = %v; want PermissionDenied", err)
		}
		other := c.grpcFwdClient(t, others[0], c.otherCert, c.otherKey)
		if _, err := grpcPut(t, other, db, "k-other", "v"); err == nil {
			t.Fatal("a caller from another tenant wrote through the non-replica")
		}
		if _, err := grpcGet(t, writer[replica], db, "k-denied"); status.Code(err) != codes.NotFound {
			t.Fatalf("Get of the refused write = %v; want NotFound", err)
		}
	})

	t.Run("a stream is refused with the replica set", func(t *testing.T) {
		ctx, cancel := dbCtx(t, db)
		defer cancel()
		stream, err := writer[others[0]].Watch(ctx, &pb.WatchRequest{Key: []byte("k")})
		if err == nil {
			_, err = stream.Recv()
		}
		info, ok := notAReplica(err)
		if !ok || status.Code(err) != codes.Unavailable {
			t.Fatalf("Watch through a non-replica = %v; want the NOT_A_REPLICA refusal", err)
		}
		if info.GetMetadata()["replicas"] != "1" || info.GetMetadata()["replica_0_raft_addr"] != c.nodes[replica].raftAddr {
			t.Fatalf("refusal metadata = %v; want the one replica on node %d", info.GetMetadata(), replica+1)
		}
	})

	t.Run("with the replica down the non-replica refuses with the replica set", func(t *testing.T) {
		c.stop(t, replica)
		_, err := grpcPut(t, writer[others[0]], db, "k-after-stop", "v")
		info, ok := notAReplica(err)
		if !ok || status.Code(err) != codes.Unavailable {
			t.Fatalf("Put with the replica down = %v; want the NOT_A_REPLICA refusal, never a local answer", err)
		}
		if info.GetMetadata()["replica_0_raft_addr"] != c.nodes[replica].raftAddr {
			t.Fatalf("refusal metadata = %v; want the stopped replica named", info.GetMetadata())
		}
	})
}

// TestReplicaForwarding_GRPC_KillTheLeader: four nodes, a three-replica
// database, so one node holds no replica. The database's leader is killed;
// once the group elects another, the next request forwarded through the
// non-replica reaches the new leader: it is served by a surviving replica --
// never the dead one, even when the leader cache still names it -- and the new
// leader holds the write.
//
// A follower serves a write by proposing it to its leader, so the replica
// that answers a forward need not be the leader itself; what reaches the new
// leader is the write.
func TestReplicaForwarding_GRPC_KillTheLeader(t *testing.T) {
	c := startFwdCluster(t, 4)
	const db = "fwd-grpc-ha"
	shard := c.createDatabase(t, db, 3)
	p := c.placementOf(t, shard, 3)
	others := c.nonReplicas(p)
	if len(others) != 1 {
		t.Fatalf("non-replicas = %v; want one", others)
	}
	via := c.grpcFwdClient(t, others[0], c.writerCert, c.writerKey)

	cached, err := grpcPut(t, via, db, "k-before", "v1")
	if err != nil || cached == "" {
		t.Fatalf("Put through the non-replica = %q, %v; want it forwarded", cached, err)
	}
	killed := strconv.FormatUint(p.leaderRID, 10)
	t.Logf("leader node %d (replica %s); the forward cached replica %s", p.leader+1, killed, cached)

	c.stop(t, p.leader)
	after := c.placementOf(t, shard, 2)
	t.Logf("new leader node %d (replica %d)", after.leader+1, after.leaderRID)

	servedBy, err := grpcPut(t, via, db, "k-after", "v2")
	if err != nil {
		t.Fatalf("Put through the non-replica after the leader died = %v", err)
	}
	if servedBy == "" || servedBy == killed {
		t.Fatalf("x-qrdb-forwarded-to = %q after replica %s died; want a surviving replica", servedBy, killed)
	}
	leader := c.grpcFwdClient(t, after.leader, c.writerCert, c.writerKey)
	for key, want := range map[string]string{"k-before": "v1", "k-after": "v2"} {
		if got, err := grpcGet(t, leader, db, key); err != nil || got != want {
			t.Fatalf("Get %s from the new leader = %q, %v; want %q", key, got, err, want)
		}
	}
}
