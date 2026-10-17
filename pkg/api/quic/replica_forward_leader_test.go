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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// newQUICFollower serves a follower of the system shard: its admin service
// refuses every member removal with *service.NotLeaderError naming
// leaderAddr, and leader is the one member it can resolve.
func newQUICFollower(t *testing.T, ca *replicaforwardtest.CA, leader *quicTarget, leaderAddr string) *quicFwdCluster {
	t.Helper()
	members := replicaforwardtest.Members{
		{NodeID: 3, Address: leader.raftAddr, APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolQUIC: leader.addr}},
	}
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	mw := authmw.New(&noopAuthorizer{}, authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)))
	admin := &mockAdminService{removeMemberFn: func(context.Context, uint64, uint64) error {
		return &service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: leaderAddr}
	}}
	c := &quicFwdCluster{cache: replicaforward.NewLeaderCache()}
	c.srv = NewServer(&mockKVService{}, admin, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(mw),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, c.cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: 5 * time.Second}))
	if err := c.srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.srv.Stop)
	c.client = ca.TLS(t, "")
	c.client.NextProtos = []string{wire.ProtocolV3}
	return c
}

func removeMemberPayload(t *testing.T) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.RemoveMemberRequest{ShardId: 1, ReplicaId: 7})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReplicaForwardQUIC_NotLeaderForwardsToTheLeaderAsTheCaller(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startQUICTarget(t, ca, "node-3", "raft-leader")
	leader.setAnswer(func(context.Context) (uint16, []byte, bool) { return wire.StatusOK, nil, false })
	c := newQUICFollower(t, ca, leader, "raft-leader")

	if st, body := c.put(t, "admin.RemoveMember", removeMemberPayload(t)); st != wire.StatusOK {
		t.Fatalf("admin.RemoveMember on a follower = %s %s; want the leader's answer", wire.StatusName(st), body)
	}
	seen := leader.lastSeen(t)
	if seen.verifyErr != nil || seen.subject != "alice" {
		t.Fatalf("leader saw %+v; want a verified assertion for alice", seen)
	}
}

func TestReplicaForwardQUIC_NotLeaderRelaysTheLeadersOwnError(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startQUICTarget(t, ca, "node-3", "raft-leader")
	leader.setAnswer(func(context.Context) (uint16, []byte, bool) {
		return wire.StatusFailedPrecondition, []byte("replica 7 is not a member"), false
	})
	c := newQUICFollower(t, ca, leader, "raft-leader")

	if st, body := c.put(t, "admin.RemoveMember", removeMemberPayload(t)); st != wire.StatusFailedPrecondition {
		t.Fatalf("admin.RemoveMember on a follower = %s %s; want the leader's failed precondition relayed", wire.StatusName(st), body)
	}
}

func TestReplicaForwardQUIC_NotLeaderNamingAnUnknownLeaderIsTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startQUICTarget(t, ca, "node-3", "raft-leader")

	for _, leaderAddr := range []string{"raft-unknown", ""} {
		c := newQUICFollower(t, ca, leader, leaderAddr)
		if st, body := c.put(t, "admin.RemoveMember", removeMemberPayload(t)); st != wire.StatusUnavailable {
			t.Fatalf("leader %q: admin.RemoveMember = %s %s; want the follower's own NotLeader refusal", leaderAddr, wire.StatusName(st), body)
		}
	}
	if n := leader.calls.Load(); n != 0 {
		t.Fatalf("an unresolvable leader was forwarded to (%d leader calls)", n)
	}
}
