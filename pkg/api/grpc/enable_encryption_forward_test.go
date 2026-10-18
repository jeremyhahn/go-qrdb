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
	"bytes"
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// A follower forwards enable-encryption to the system-shard leader with the
// passphrase intact, and wipes its own copy once the call returns.
func TestBarrierEnableEncryption_FollowerForwardsTheIntactPassphrase(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startTarget(t, ca, "node-1", "raft-leader")
	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: leader.addr}},
	}
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: 5 * time.Second,
	}, ca.TLS(t, "node-a"))
	t.Cleanup(fwd.Close)

	svc := &qrdbServiceServer{
		encMigrator: &captureGRPCMigrator{
			ch:  make(chan grpcMigrateCall, 1),
			err: &service.NotLeaderError{ShardID: 1, LeaderID: 1, LeaderAddress: "raft-leader"},
		},
		replicaForwarder: fwd,
	}
	req := &pb.BarrierEnableEncryptionRequest{Strategy: "passphrase", Passphrase: []byte("correct horse")}
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice"})
	resp, err := svc.BarrierEnableEncryption(ctx, req)
	if err != nil {
		t.Fatalf("BarrierEnableEncryption = %v; want the leader's answer", err)
	}
	if resp.GetStatus() != "leader-answered" {
		t.Fatalf("Status = %q; want the leader's", resp.GetStatus())
	}
	if got := leader.forwardedPassphrase(); !bytes.Equal(got, []byte("correct horse")) {
		t.Fatalf("leader received passphrase %q; want the caller's", got)
	}
	if !bytes.Equal(req.GetPassphrase(), make([]byte, len("correct horse"))) {
		t.Fatal("the follower's copy of the passphrase was not wiped")
	}
}

// Without a forwarder the refusal is Unavailable, never a 500, and carries no
// leader address that would let the interceptor replay the wiped request.
func TestBarrierEnableEncryption_FollowerWithoutForwarderIsUnavailable(t *testing.T) {
	svc := &qrdbServiceServer{
		encMigrator: &captureGRPCMigrator{
			ch:  make(chan grpcMigrateCall, 1),
			err: &service.NotLeaderError{ShardID: 1, LeaderID: 2, LeaderAddress: "raft-leader"},
		},
	}
	_, err := svc.BarrierEnableEncryption(context.Background(), &pb.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code = %s; want Unavailable (err=%v)", status.Code(err), err)
	}
	if _, md, ok := refusalFromStatus(status.Convert(err)); ok && replicaforward.LeaderRaftAddr(md) != "" {
		t.Fatalf("refusal names leader %q; the interceptor would replay a wiped passphrase", replicaforward.LeaderRaftAddr(md))
	}
}
