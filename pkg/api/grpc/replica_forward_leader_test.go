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
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// followerService refuses every membership change the way a follower of the
// system shard does: *service.NotLeaderError naming the leader's Raft address.
type followerService struct {
	pb.UnimplementedQRDBServiceServer
	leaderAddr string
	calls      atomic.Int32
}

func (f *followerService) RemoveMember(context.Context, *pb.RemoveMemberRequest) (*emptypb.Empty, error) {
	f.calls.Add(1)
	return nil, mapServiceError(&service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: f.leaderAddr})
}

// newFollowerIngress serves a follower of the system shard behind the same
// interceptor chain a node runs -- authentication admitting alice/acme, the
// served-by marker, then the forwarding interceptor -- with leader as the one
// member it can resolve. It returns a client of the follower.
func newFollowerIngress(t *testing.T, ca *replicaforwardtest.CA, leader *fakeTarget, leaderAddr string) (pb.QRDBServiceClient, *followerService) {
	t.Helper()
	members := replicaforwardtest.Members{
		{NodeID: 3, Address: leader.raftAddr, APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: leader.addr}},
	}
	ingressTLS := ca.TLS(t, "node-a")
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: 5 * time.Second,
	}, ingressTLS)
	t.Cleanup(fwd.Close)
	authn := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(auth.WithIdentity(ctx, &auth.Identity{Subject: "alice", TenantID: "acme"}), req)
	}
	follower := &followerService{leaderAddr: leaderAddr}
	addr := serveGRPC(t, ingressTLS, follower, authn, servedByUnary("node-a"), fwd.UnaryInterceptor())
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(ca.TLS(t, ""))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return pb.NewQRDBServiceClient(cc), follower
}

func TestReplicaForwardGRPC_NotLeaderForwardsToTheLeaderAsTheCaller(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startTarget(t, ca, "node-3", "raft-leader")
	client, follower := newFollowerIngress(t, ca, leader, "raft-leader")

	if _, err := client.RemoveMember(context.Background(), &pb.RemoveMemberRequest{ShardId: 1, ReplicaId: 7}); err != nil {
		t.Fatalf("RemoveMember on a follower = %v; want the leader's answer", err)
	}
	if n := follower.calls.Load(); n != 1 {
		t.Fatalf("follower handler ran %d times; want 1", n)
	}
	seen := leader.lastSeen(t)
	if seen.verifyErr != nil || seen.subject != "alice" || seen.tenant != "acme" {
		t.Fatalf("leader saw %+v; want a verified assertion for alice/acme", seen)
	}
}

func TestReplicaForwardGRPC_NotLeaderRelaysTheLeadersOwnError(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startTarget(t, ca, "node-3", "raft-leader")
	leader.answer = func(context.Context) error {
		return status.Error(codes.FailedPrecondition, "replica 7 is not a member")
	}
	client, _ := newFollowerIngress(t, ca, leader, "raft-leader")

	_, err := client.RemoveMember(context.Background(), &pb.RemoveMemberRequest{ShardId: 1, ReplicaId: 7})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("RemoveMember on a follower = %v; want the leader's FailedPrecondition relayed", err)
	}
}

func TestReplicaForwardGRPC_AForwardedNotLeaderIsNeverForwardedAgain(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startTarget(t, ca, "node-3", "raft-leader")
	client, _ := newFollowerIngress(t, ca, leader, "raft-leader")

	ctx := metadata.AppendToOutgoingContext(context.Background(), forward.MetadataAssertion, "a", forward.MetadataSignature, "s")
	_, err := client.RemoveMember(ctx, &pb.RemoveMemberRequest{ShardId: 1, ReplicaId: 7})
	if reason, _, ok := refusalFromStatus(status.Convert(err)); !ok || reason != replicaforward.ReasonNotLeader {
		t.Fatalf("a forwarded request = %v; want the follower's NotLeader refusal", err)
	}
	if n := leader.calls.Load(); n != 0 {
		t.Fatalf("a forwarded request was forwarded again (%d leader calls)", n)
	}
}

func TestReplicaForwardGRPC_NotLeaderNamingAnUnknownLeaderIsTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startTarget(t, ca, "node-3", "raft-leader")

	for _, leaderAddr := range []string{"raft-unknown", ""} {
		client, _ := newFollowerIngress(t, ca, leader, leaderAddr)
		_, err := client.RemoveMember(context.Background(), &pb.RemoveMemberRequest{ShardId: 1, ReplicaId: 7})
		if reason, _, ok := refusalFromStatus(status.Convert(err)); !ok || reason != replicaforward.ReasonNotLeader {
			t.Fatalf("leader %q: RemoveMember = %v; want the follower's NotLeader refusal", leaderAddr, err)
		}
	}
	if n := leader.calls.Load(); n != 0 {
		t.Fatalf("an unresolvable leader was forwarded to (%d leader calls)", n)
	}
}
