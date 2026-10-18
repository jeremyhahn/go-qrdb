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

package embedded

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// An embedded follower forwards enable-encryption, refused not-leader by its
// migrator, to the named leader as the configured caller, with the
// passphrase intact.
func TestTransport_BarrierEnableEncryption_FollowerForwardsToTheLeader(t *testing.T) {
	body, err := proto.Marshal(&pb.BarrierEnableEncryptionResponse{Status: "leader-answered", Scope: "system"})
	if err != nil {
		t.Fatal(err)
	}
	fwd := &recordingForwarder{status: wire.StatusOK, body: body}
	mig := &stubEmbeddedMigrator{
		ch:  make(chan embeddedMigrateCall, 1),
		err: &service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: "raft-leader"},
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(mig), WithReplicaForwarding(fwd, fixedCaller{id: alice}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Passphrase: []byte("correct horse"),
	})
	if err != nil {
		t.Fatalf("BarrierEnableEncryption = %v; want the leader's answer", err)
	}
	if resp.Status != "leader-answered" {
		t.Fatalf("Status = %q; want the leader's", resp.Status)
	}
	if len(fwd.leaderCalls) != 1 {
		t.Fatalf("leaderCalls = %+v; want exactly one", fwd.leaderCalls)
	}
	c := fwd.leaderCalls[0]
	if c.leaderAddr != "raft-leader" || c.caller != alice {
		t.Fatalf("leaderCalls[0] = %+v; want raft-leader as alice", c)
	}
	var sent pb.BarrierEnableEncryptionRequest
	if err := proto.Unmarshal(c.payload, &sent); err != nil {
		t.Fatal(err)
	}
	if string(sent.GetPassphrase()) != "correct horse" {
		t.Fatalf("leader received passphrase %q; want the caller's", sent.GetPassphrase())
	}
}

// Without forwarding configured, the refusal is the node's own not-leader
// error, naming the leader.
func TestTransport_BarrierEnableEncryption_NoForwardingAnswersTheRefusal(t *testing.T) {
	mig := &stubEmbeddedMigrator{
		ch:  make(chan embeddedMigrateCall, 1),
		err: &service.NotLeaderError{ShardID: 1, LeaderID: 3, LeaderAddress: "raft-leader"},
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithEncryptionMigrator(mig))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{
		Strategy: "passphrase", Passphrase: []byte("pw"),
	})
	leader := ""
	var tnle *transport.NotLeaderError
	var snle *service.NotLeaderError
	switch {
	case errors.As(err, &tnle):
		leader = tnle.LeaderAddress
	case errors.As(err, &snle):
		leader = snle.LeaderAddress
	}
	if leader != "raft-leader" {
		t.Fatalf("err = %v; want the not-leader refusal naming raft-leader", err)
	}
}
