// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

// leader_transfer_cli_test.go drives a shard leadership transfer through
// both client surfaces -- the SDK's ClusterClient.TransferLeadership and the
// `qrdb admin member transfer-leadership` CLI command -- over every
// transport (REST, gRPC, QUIC, MCP, Unix), on one 3-node cluster.
//
// Every call is sent to the node that is neither the leader nor the target,
// so each one is forwarded to the leader: that is the node-parity path. A
// transfer to a running voter returns once the target leads, and the target's
// own /status says so the moment the call returns. A transfer to a voter
// whose process is stopped answers with the typed timeout -- errors.As
// matches *sdk.LeaderTransferTimeoutError on every transport, and the CLI
// prints its message -- and leaves the leader where it was.
package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// leaderTransferSurfaces are the two client surfaces each transport is
// driven through.
var leaderTransferSurfaces = []string{"sdk", "cli"}

func TestLeaderTransfer_PerProtocol(t *testing.T) {
	nodes, _ := startMultiProtocolCluster(t, 0)
	status := mtlsHTTPClient(t, nodes[0].ca, 5*time.Second)

	var replicaIDs [3]uint64
	for i, node := range nodes {
		replicaIDs[i], _, _ = shard1View(t, status, node.restPort)
		if replicaIDs[i] == 0 {
			t.Fatalf("node%d reports no replica of shard 1", i+1)
		}
	}
	live := []int{0, 1, 2}

	// Leadership moves on every call: each targets a follower and is sent
	// to the other follower.
	for _, proto := range multiProtocols {
		for _, surface := range leaderTransferSurfaces {
			t.Run("move/"+proto.name+"/"+surface, func(t *testing.T) {
				leader := agreedShard1Leader(t, status, nodes, replicaIDs, live, 30*time.Second)
				target, via := (leader+1)%3, (leader+2)%3

				if err := transferShard1Leadership(t, nodes[via], proto, surface, replicaIDs[target]); err != nil {
					t.Fatalf("[%s/%s] transfer to replica %d via node%d: %v", proto.name, surface, replicaIDs[target], via+1, err)
				}
				if _, _, isLeader := shard1View(t, status, nodes[target].restPort); !isLeader {
					t.Fatalf("[%s/%s] replica %d does not lead shard 1 after the transfer returned", proto.name, surface, replicaIDs[target])
				}
			})
		}
	}

	// A stopped voter never takes over: every call times out, typed.
	leader := agreedShard1Leader(t, status, nodes, replicaIDs, live, 30*time.Second)
	stopped := (leader + 1) % 3
	if err := nodes[stopped].proc.Process.Kill(); err != nil {
		t.Fatalf("stop node%d: %v", stopped+1, err)
	}
	_ = nodes[stopped].proc.Wait()
	live = []int{leader, (leader + 2) % 3}

	// Each call reads the leader it starts from rather than carrying one
	// over from an earlier call: an election between calls is the cluster
	// working, not the transfer under test. It is sent to the live follower.
	//
	// A leader whose transfer times out aborts it and keeps leading (Raft
	// thesis §3.10); the transfer itself never moves leadership. An election
	// can still run during the call: on a loaded host a heartbeat late past
	// the election timeout is the cluster working, and leadership then moves
	// in a newer term (run 56220: node1 won term 12 while the call was in
	// flight). So leadership that moved must have moved by election, in a
	// newer term, and at least one call must find its leader still leading
	// in the term it started from: a leader that stepped down when its
	// transfer timed out would leave no call that does.
	kept := 0
	for _, proto := range multiProtocols {
		for _, surface := range leaderTransferSurfaces {
			t.Run("stopped/"+proto.name+"/"+surface, func(t *testing.T) {
				leader := agreedShard1Leader(t, status, nodes, replicaIDs, live, 30*time.Second)
				term := shard1Term(t, status, nodes[leader].restPort)
				via := live[0]
				if via == leader {
					via = live[1]
				}
				err := transferShard1Leadership(t, nodes[via], proto, surface, replicaIDs[stopped])
				assertLeaderTransferTimeout(t, proto.name, surface, err, replicaIDs[leader], replicaIDs[stopped])
				got := agreedShard1Leader(t, status, nodes, replicaIDs, live, 30*time.Second)
				gotTerm := shard1Term(t, status, nodes[got].restPort)
				switch {
				case gotTerm == term && got != leader:
					t.Fatalf("[%s/%s] leader moved to node%d in term %d after a timed-out transfer, want node%d: leadership moved without an election", proto.name, surface, got+1, term, leader+1)
				case gotTerm == term:
					kept++
				case gotTerm < term:
					t.Fatalf("[%s/%s] shard 1's term went from %d to %d during a timed-out transfer, want it never to go back", proto.name, surface, term, gotTerm)
				default:
					t.Logf("[%s/%s] an election ran during the call (term %d to %d, node%d to node%d)", proto.name, surface, term, gotTerm, leader+1, got+1)
				}
			})
		}
	}
	if kept == 0 {
		t.Fatalf("every timed-out transfer was followed by a new term, want a leader whose transfer times out to keep leading")
	}
}

// transferShard1Leadership asks node, over proto, to hand shard 1's
// leadership to replicaID through surface: the SDK's ClusterClient, or the
// qrdb CLI, whose success output must be OK. A CLI failure is returned with
// its output as the error text.
func transferShard1Leadership(t *testing.T, node *multiProtoNode, proto multiProtocol, surface string, replicaID uint64) error {
	t.Helper()
	if surface == "cli" {
		out, err := runCLIProto(t, node, proto, "admin", "member", "transfer-leadership", strconv.FormatUint(replicaID, 10), "--shard-id", "1")
		if err != nil {
			return &cliOutputError{output: out, err: err}
		}
		if !strings.Contains(out, "OK") {
			t.Fatalf("[%s/cli] transfer-leadership output = %q, want OK", proto.name, out)
		}
		return nil
	}

	client := sdkClientForProtocolAs(t, node, proto, node.clientCert, node.clientKey)
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("[%s/sdk] connect: %v", proto.name, err)
	}
	return client.Cluster().TransferLeadership(ctx, 1, replicaID)
}

// cliOutputError is a failed qrdb CLI run, carrying what it printed.
type cliOutputError struct {
	output string
	err    error
}

func (e *cliOutputError) Error() string { return e.err.Error() + ": " + e.output }

func (e *cliOutputError) Unwrap() error { return e.err }

// assertLeaderTransferTimeout checks err is the typed timeout for a transfer
// from replica from to replica to: matched by errors.As over the SDK, and
// printed by the CLI.
func assertLeaderTransferTimeout(t *testing.T, proto, surface string, err error, from, to uint64) {
	t.Helper()
	if err == nil {
		t.Fatalf("[%s/%s] transfer to stopped replica %d succeeded", proto, surface, to)
	}
	want := fmt.Sprintf("leadership transfer away from replica %d to replica %d did not complete after", from, to)

	var cliErr *cliOutputError
	if errors.As(err, &cliErr) {
		if !strings.Contains(cliErr.output, want) {
			t.Fatalf("[%s/cli] output = %q, want it to contain %q", proto, cliErr.output, want)
		}
		return
	}

	var lte *sdk.LeaderTransferTimeoutError
	if !errors.As(err, &lte) {
		t.Fatalf("[%s/sdk] err = %v (%T), want *sdk.LeaderTransferTimeoutError", proto, err, err)
	}
	if lte.ShardID != 1 || lte.FromReplicaID != from || lte.ToReplicaID != to || lte.Timeout <= 0 {
		t.Fatalf("[%s/sdk] timeout = %+v, want shard 1 from %d to %d with a bound", proto, *lte, from, to)
	}
}

// shard1View reads the node's /status: its replica of shard 1, the leader
// it knows of, and whether it leads.
func shard1View(t *testing.T, client *http.Client, port int) (replicaID, leaderID uint64, isLeader bool) {
	t.Helper()
	status, _ := httpStatusRaw(t, client, port)
	shards, _ := status["shards"].([]interface{})
	for _, s := range shards {
		shard, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := shard["shard_id"].(float64); id != 1 {
			continue
		}
		rid, _ := shard["replica_id"].(float64)
		lid, _ := shard["leader_id"].(float64)
		leads, _ := shard["is_leader"].(bool)
		return uint64(rid), uint64(lid), leads
	}
	return 0, 0, false
}

// shard1Term reads the node's /status for the term of the last leadership
// change of shard 1 it published: on the leader, the term it was elected in.
func shard1Term(t *testing.T, client *http.Client, port int) uint64 {
	t.Helper()
	status, _ := httpStatusRaw(t, client, port)
	shards, _ := status["shards"].([]interface{})
	for _, s := range shards {
		shard, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := shard["shard_id"].(float64); id != 1 {
			continue
		}
		term, _ := shard["term"].(float64)
		if term == 0 {
			t.Fatalf("port %d reports no term for shard 1", port)
		}
		return uint64(term)
	}
	t.Fatalf("port %d reports no replica of shard 1", port)
	return 0
}

// agreedShard1Leader waits up to timeout for every live node to name the same
// leader of shard 1, and for that node to say it leads, and returns its
// index. Right after a leader change a follower learns of it only on the new
// leader's next message, and a call sent to it before then would be
// forwarded to the old one.
func agreedShard1Leader(t *testing.T, client *http.Client, nodes [3]*multiProtoNode, replicaIDs [3]uint64, live []int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if leader, ok := shard1LeaderAgreement(t, client, nodes, replicaIDs, live); ok {
			return leader
		}
		if time.Now().After(deadline) {
			t.Fatalf("live nodes %v did not agree on a leader of shard 1 within %s", live, timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// shard1LeaderAgreement is one reading of agreedShard1Leader's condition.
func shard1LeaderAgreement(t *testing.T, client *http.Client, nodes [3]*multiProtoNode, replicaIDs [3]uint64, live []int) (int, bool) {
	t.Helper()
	var agreed uint64
	for _, i := range live {
		_, leaderID, _ := shard1View(t, client, nodes[i].restPort)
		if leaderID == 0 || (agreed != 0 && leaderID != agreed) {
			return 0, false
		}
		agreed = leaderID
	}
	for _, i := range live {
		if replicaIDs[i] == agreed {
			_, _, isLeader := shard1View(t, client, nodes[i].restPort)
			return i, isLeader
		}
	}
	return 0, false
}
