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

// A follower's enable-encryption, refused not-leader by the migrator, reaches
// the system-shard leader as the caller with the passphrase intact: the
// handler wipes only its decoded copy, never the frame it forwards.
func TestReplicaForwardQUIC_EnableEncryptionForwardsTheIntactPassphrase(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startQUICTarget(t, ca, "node-3", "raft-leader")
	leader.setAnswer(func(context.Context) (uint16, []byte, bool) { return wire.StatusOK, nil, false })

	members := replicaforwardtest.Members{
		{NodeID: 3, Address: leader.raftAddr, APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolQUIC: leader.addr}},
	}
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	mw := authmw.New(&noopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)))
	mig := &captureQUICMigrator{
		ch:  make(chan quicMigrateCall, 1),
		err: &service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: "raft-leader"},
	}
	c := &quicFwdCluster{cache: replicaforward.NewLeaderCache()}
	c.srv = NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(mw),
		WithEncryptionMigrator(mig),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, c.cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: 5 * time.Second}))
	if err := c.srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.srv.Stop)
	c.client = ca.TLS(t, "")
	c.client.NextProtos = []string{wire.ProtocolV3}

	payload, err := proto.Marshal(&pb.BarrierEnableEncryptionRequest{Strategy: "passphrase", Passphrase: []byte("correct horse")})
	if err != nil {
		t.Fatal(err)
	}
	if st, body := c.put(t, "barrier.EnableEncryption", payload); st != wire.StatusOK {
		t.Fatalf("barrier.EnableEncryption on a follower = %s %s; want the leader's answer", wire.StatusName(st), body)
	}
	seen := leader.lastSeen(t)
	if seen.verifyErr != nil || seen.subject != "alice" || seen.method != "barrier.EnableEncryption" {
		t.Fatalf("leader saw %+v; want a verified barrier.EnableEncryption for alice", seen)
	}
	var got pb.BarrierEnableEncryptionRequest
	if err := proto.Unmarshal(seen.payload, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.GetPassphrase()) != "correct horse" {
		t.Fatalf("leader received passphrase %q; want the caller's", got.GetPassphrase())
	}
}
